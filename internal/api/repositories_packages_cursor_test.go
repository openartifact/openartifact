// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// cursorListDB serves a fixed newest-first listing by offset or by keyset, the
// way the real store does.
type cursorListDB struct {
	repoLifecycleDB

	rows     []*models.Component
	searched []database.ComponentListOptions
}

func newCursorListDB(n int) *cursorListDB {
	db := &cursorListDB{}
	db.targets = adminTargets()
	db.repo = &models.Repository{ID: "repo-1", Name: "pkgs", Format: "npm"}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		// pairs of rows share a created_at: the id breaks the tie
		db.rows = append(db.rows, &models.Component{
			ID:        fmt.Sprintf("00000000-0000-0000-0000-%012d", i),
			Name:      "pkg",
			Version:   strconv.Itoa(i),
			CreatedAt: base.Add(-time.Duration(i/2) * time.Minute),
		})
	}

	return db
}

func (*cursorListDB) ArtifactSecurity(context.Context, string, []string) (map[string]models.ArtifactSecurity, error) {
	return map[string]models.ArtifactSecurity{}, nil
}

func (db *cursorListDB) ListComponentsByRepo(_ context.Context, _, _ string, page, limit int) ([]*models.Component, int, error) {
	return db.page((page-1)*limit, limit), len(db.rows), nil
}

func (db *cursorListDB) SearchComponents(
	_ context.Context,
	_ string,
	opts database.ComponentListOptions,
) ([]*models.Component, int, error) {
	db.searched = append(db.searched, opts)

	if opts.After == nil {
		return db.page((opts.Page-1)*opts.Limit, opts.Limit), len(db.rows), nil
	}

	start := len(db.rows)

	for i, r := range db.rows {
		if r.CreatedAt.Before(opts.After.CreatedAt) || (r.CreatedAt.Equal(opts.After.CreatedAt) && r.ID > opts.After.ID) {
			start = i
			break
		}
	}

	return db.page(start, opts.Limit), 0, nil
}

func (db *cursorListDB) page(offset, limit int) []*models.Component {
	if offset >= len(db.rows) {
		return nil
	}

	return db.rows[offset:min(offset+limit, len(db.rows))]
}

func getPackages(t *testing.T, db *cursorListDB, query string) (int, map[string]any) {
	t.Helper()

	log := newFixTestLogger(t)
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))

	router := chi.NewRouter()
	router.Get("/api/v1/repositories/{name}/packages", h.handlePackages)

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, adminRequest(nil, http.MethodGet, "/api/v1/repositories/pkgs/packages?"+query, ""))

	var body map[string]any
	if rw.Code == http.StatusOK {
		if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %q: %v", rw.Body.String(), err)
		}
	}

	return rw.Code, body
}

func TestPackagesCursorRejected(t *testing.T) {
	good := encodeComponentCursor(&models.Component{ID: "00000000-0000-0000-0000-000000000001", CreatedAt: time.Now()})

	for name, query := range map[string]string{
		"not base64":    "cursor=!!!",
		"not a cursor":  "cursor=aGVsbG8",
		"group=package": "group=package&cursor=" + good,
		"sort=name":     "sort=name&cursor=" + good,
		"forged out of range timestamp": "cursor=" + encodeAuditCursor(&models.AuditCursor{
			CreatedAt: time.Date(20000, 1, 1, 0, 0, 0, 0, time.UTC), ID: "00000000-0000-0000-0000-000000000001",
		}),
	} {
		t.Run(name, func(t *testing.T) {
			db := newCursorListDB(5)

			code, _ := getPackages(t, db, query)
			if code != http.StatusBadRequest || len(db.searched) != 0 {
				t.Fatalf("status %d searched %v, want 400 without reaching the store", code, db.searched)
			}
		})
	}
}

func TestPackagesOffsetPageHasMoreAndNextCursor(t *testing.T) {
	db := newCursorListDB(5)

	code, body := getPackages(t, db, "page=1&limit=2")
	if code != http.StatusOK || body["total"] != float64(5) || body["has_more"] != true {
		t.Fatalf("status %d body %v, want total 5 and has_more", code, body)
	}

	next, _ := body["next_cursor"].(string)
	if next != encodeComponentCursor(db.rows[1]) {
		t.Fatalf("next_cursor %q is not the cursor of the page's last row", next)
	}

	// the last offset page has no more and no cursor
	_, body = getPackages(t, db, "page=3&limit=2")
	if body["total"] != float64(5) || body["has_more"] != false || body["next_cursor"] != nil {
		t.Fatalf("last page body %v, want has_more=false and no next_cursor", body)
	}

	// sort=name has no cursor to switch to
	_, body = getPackages(t, db, "sort=name&page=1&limit=2")
	if body["has_more"] != true || body["next_cursor"] != nil {
		t.Fatalf("sort=name body %v, want has_more and no next_cursor", body)
	}
}

func TestPackagesCursorWalk(t *testing.T) {
	db := newCursorListDB(7)

	var seen []string

	query := "limit=3"

	for range 5 {
		code, body := getPackages(t, db, query)
		if code != http.StatusOK {
			t.Fatalf("status %d", code)
		}

		if _, has := body["total"]; has && query != "limit=3" {
			t.Fatalf("a cursor request must not carry total: %v", body)
		}

		for _, p := range body["packages"].([]any) {
			seen = append(seen, p.(map[string]any)["id"].(string))
		}

		if body["has_more"] != true {
			if _, has := body["next_cursor"]; has {
				t.Fatalf("last page must not carry next_cursor: %v", body)
			}

			break
		}

		query = "limit=3&cursor=" + body["next_cursor"].(string)
	}

	if len(seen) != 7 {
		t.Fatalf("walked %d rows, want 7: %v", len(seen), seen)
	}

	for i, id := range seen {
		if id != db.rows[i].ID {
			t.Fatalf("row %d is %s, want %s (ties must not skip or repeat)", i, id, db.rows[i].ID)
		}
	}

	last := db.searched[len(db.searched)-1]
	if last.After == nil || !last.NoTotal || last.Limit != 4 || last.Page != 1 {
		t.Fatalf("cursor request asked the store for %+v, want After, NoTotal and limit+1", last)
	}
}

func TestPackagesCursorExactLastPage(t *testing.T) {
	db := newCursorListDB(6)

	_, body := getPackages(t, db, "limit=3")
	_, last := getPackages(t, db, "limit=3&cursor="+body["next_cursor"].(string))

	if last["has_more"] != false || last["next_cursor"] != nil || len(last["packages"].([]any)) != 3 {
		t.Fatalf("exact last page %v, want 3 rows and has_more=false", last)
	}

	if _, has := last["total"]; has {
		t.Fatalf("total must be absent with a cursor: %v", last)
	}
}
