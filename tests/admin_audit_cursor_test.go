// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

type auditListBody struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	Page       *int    `json:"page"`
	PerPage    int     `json:"per_page"`
	Total      *int    `json:"total"`
	TotalExact *bool   `json:"total_exact"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

type auditListClient struct {
	t         *testing.T
	router    chi.Router
	principal *auth.Principal
}

func (c auditListClient) get(query string) (int, auditListBody) {
	c.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/admin/audit?"+query, nil)
	req = withPrincipal(req, c.principal)
	rw := httptest.NewRecorder()
	c.router.ServeHTTP(rw, req)

	var body auditListBody
	_ = json.NewDecoder(rw.Body).Decode(&body)

	return rw.Code, body
}

// newAuditListClient seeds five entries (newest first: ids ...0 to ...4) and
// returns a client for the audit handler plus those ids in listing order.
func newAuditListClient(t *testing.T) (auditListClient, []string) {
	t.Helper()

	db := newMockDatabase()
	router, principal := setupAdminAuditRouter(t, db)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	var ids []string

	for i := range 5 {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		ids = append(ids, id)

		row := &models.AuditLog{ID: id, Action: "auth.login", DetailsJSON: detailsJSON("admin", ""), CreatedAt: base.Add(-time.Duration(i) * time.Second)}
		if err := db.InsertAuditLog(context.Background(), row); err != nil {
			t.Fatalf("InsertAuditLog: %v", err)
		}
	}

	return auditListClient{t: t, router: router, principal: principal}, ids
}

func TestAdminAudit_OffsetResponseKeepsTotalAndAddsCursor(t *testing.T) {
	c, _ := newAuditListClient(t)

	code, body := c.get("per_page=2&page=2")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}

	if body.Page == nil || *body.Page != 2 || body.Total == nil || *body.Total != 5 || body.TotalExact == nil || !*body.TotalExact {
		t.Errorf("offset fields = page %v total %v exact %v", body.Page, body.Total, body.TotalExact)
	}

	if !body.HasMore || body.NextCursor == nil {
		t.Errorf("page 2 of 3 must report has_more with a next_cursor, got %+v", body)
	}
}

func TestAdminAudit_KeysetWalkVisitsEachRowOnce(t *testing.T) {
	c, want := newAuditListClient(t)

	var got []string

	query := "per_page=2&cursor=" // empty cursor: keyset mode from the newest entry

	for range 10 {
		code, body := c.get(query)
		if code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}

		if body.Total != nil || body.Page != nil {
			t.Errorf("keyset mode must not compute total/page, got total=%v page=%v", body.Total, body.Page)
		}

		for _, e := range body.Data {
			got = append(got, e.ID)
		}

		if !body.HasMore {
			if body.NextCursor != nil {
				t.Errorf("last page carries next_cursor %q", *body.NextCursor)
			}

			break
		}

		query = "per_page=2&cursor=" + url.QueryEscape(*body.NextCursor)
	}

	if !slices.Equal(got, want) {
		t.Errorf("walked %v, want %v", got, want)
	}
}

func TestAdminAudit_MalformedCursorIs400(t *testing.T) {
	c, _ := newAuditListClient(t)

	if code, _ := c.get("cursor=not-a-cursor"); code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}
