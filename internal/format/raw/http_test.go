// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package raw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/webhook"
)

type rawDB struct {
	*formattest.DB
	quotaFake

	art *models.RawArtifact
}

func (d *rawDB) UpsertRawArtifact(_ context.Context, a *models.RawArtifact) error {
	d.art = a
	return nil
}

func (d *rawDB) GetRawArtifact(_ context.Context, _, _ string) (*models.RawArtifact, error) {
	return d.art, nil
}

func (d *rawDB) DeleteRawArtifact(_ context.Context, _, _ string) error {
	d.art = nil
	return nil
}

func newTestHandler(t *testing.T) (*HTTPHandler, *rawDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := &rawDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	return h, db, bs, pub
}

func TestPutAndDeleteEmitEvents(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, "/repos/myrepo/dir/sub/file.txt", strings.NewReader("hello")))
	if w.Code != http.StatusCreated {
		t.Fatalf("put status = %d: %s", w.Code, w.Body.String())
	}

	if db.art == nil || db.art.Path != "dir/sub/file.txt" || db.art.SizeBytes != 5 {
		t.Fatalf("artifact = %+v", db.art)
	}

	if got := string(bs.Blobs[db.art.SHA256]); got != "hello" {
		t.Fatalf("blob = %q", got)
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events = %+v", evs)
	}

	if a := evs[0].Artifact(); a["path"] != "dir/sub/file.txt" || a["sha256"] != db.art.SHA256 {
		t.Errorf("artifact = %v", a)
	}

	if w = formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodDelete, "/repos/myrepo/dir/sub/file.txt", nil)); w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", w.Code)
	}

	evs = pub.Events()
	if len(evs) != 2 || evs[1].Type != webhook.EventArtifactDeleted {
		t.Fatalf("events after delete = %+v", evs)
	}

	if len(bs.Blobs) != 0 {
		t.Error("blob not removed after delete")
	}
}

func TestGetQuarantine(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.art = &models.RawArtifact{Path: "a/b.txt", BlobKey: "abc", SHA256: "abc", SizeBytes: 3, ContentType: "text/plain"}
	bs.Blobs["abc"] = []byte("xyz")

	get := func() int {
		return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/a/b.txt", nil)).Code
	}

	if c := get(); c != http.StatusOK {
		t.Fatalf("status = %d, want 200", c)
	}

	db.Keys["a/b.txt"] = true

	if c := get(); c != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", c)
	}
}

// manageOnly grants "manage" to the user named "admin" and nothing else.
type manageOnly struct{}

func (manageOnly) Evaluate(_ context.Context, p auth.Principal, _, _, action string) (bool, error) {
	return action == "manage" && p.Username == "admin", nil
}

// TestPutImportCreatedAt drives the real middleware in front of the raw PUT
// handler: only an administrator's X-OA-Import-Created-At reaches the stored
// artifact; a normal client's header is ignored.
func TestPutImportCreatedAt(t *testing.T) {
	origin := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

	tests := []struct {
		name string
		user string
		want time.Time
	}{
		{"admin keeps origin timestamp", "admin", origin},
		{"regular client is stamped by the server", "alice", time.Time{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, db, _, _ := newTestHandler(t)

			router := chi.NewRouter()
			router.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), &auth.Principal{Username: tc.user})))
				})
			})
			router.Use(importmeta.Middleware(manageOnly{}))
			router.Route("/repos/{name}", h.RegisterRepoRoutes)

			req := formattest.Request(http.MethodPut, "/repos/myrepo/a/b.txt", strings.NewReader("hello"))
			req.Header.Set(importmeta.HeaderCreatedAt, origin.Format(time.RFC3339))

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}

			if !db.art.CreatedAt.Equal(tc.want) {
				t.Errorf("created_at = %v, want %v", db.art.CreatedAt, tc.want)
			}
		})
	}
}

type listDB struct {
	*formattest.DB

	paths []string
}

// ListRawArtifacts mirrors the store: prefix is a plain string prefix.
func (d *listDB) ListRawArtifacts(_ context.Context, _, prefix string) ([]*models.RawArtifact, error) {
	out := []*models.RawArtifact{}

	for _, p := range d.paths {
		if strings.HasPrefix(p, prefix) {
			out = append(out, &models.RawArtifact{Path: p})
		}
	}

	return out, nil
}

// FMTA-10: listing "docs/" must not include siblings sharing the string prefix.
func TestListDirectoryMatchesWholeSegment(t *testing.T) {
	db := &listDB{DB: formattest.NewDB(), paths: []string{"docs/a.txt", "docs-old/b.txt", "docs/sub/c.txt", "docsx"}}
	h := NewHTTPHandler(formattest.NewLogger(t), db, formattest.NewBlobStore(), "http://localhost")

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/docs/", nil))
	body := w.Body.String()

	if !strings.Contains(body, "docs/a.txt") || !strings.Contains(body, "docs/sub/c.txt") {
		t.Errorf("listing misses children: %s", body)
	}

	if strings.Contains(body, "docs-old") || strings.Contains(body, "docsx") {
		t.Errorf("listing leaks sibling directories: %s", body)
	}
}

func TestHeadQuarantine(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	db.art = &models.RawArtifact{Path: "a/b.txt", BlobKey: "abc", SHA256: "abc", SizeBytes: 3, ContentType: "text/plain"}

	head := func() *httptest.ResponseRecorder {
		return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodHead, "/repos/myrepo/a/b.txt", nil))
	}

	if w := head(); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	db.Keys["a/b.txt"] = true

	w := head()
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}

	if w.Header().Get("X-Checksum-Sha256") != "" {
		t.Error("quarantined HEAD leaked checksum")
	}
}
