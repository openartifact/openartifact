// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package importmeta_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/middleware"
)

// manageByUser grants the global "manage" permission to the named users only.
type manageByUser map[string]bool

func (m manageByUser) Evaluate(_ context.Context, p auth.Principal, repo, path, action string) (bool, error) {
	return action == "manage" && repo == "" && path == "" && m[p.Username], nil
}

// probe records what a downstream handler observes.
type probe struct {
	called    bool
	createdAt time.Time
	rawHeader string
}

func (p *probe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.called = true
	p.createdAt = importmeta.CreatedAt(r.Context())
	p.rawHeader = r.Header.Get(importmeta.HeaderCreatedAt)

	w.WriteHeader(http.StatusNoContent)
}

func run(t *testing.T, principal *auth.Principal, header string) (*probe, int) {
	t.Helper()

	next := &probe{}
	h := importmeta.Middleware(manageByUser{"admin": true})(next)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/repos/raw/r/file", nil)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), principal))

	if header != "" {
		req.Header.Set(importmeta.HeaderCreatedAt, header)
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	return next, w.Code
}

func TestMiddleware(t *testing.T) {
	t.Parallel()

	past := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	admin := &auth.Principal{Username: "admin"}
	user := &auth.Principal{Username: "alice"}
	anon := &auth.Principal{IsAnonymous: true}
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)

	tests := []struct {
		name       string
		principal  *auth.Principal
		header     string
		wantStatus int
		wantCalled bool
		wantTime   time.Time
	}{
		{"admin honored", admin, past.Format(time.RFC3339), http.StatusNoContent, true, past},
		{"admin with offset normalised to UTC", admin, "2021-03-04T06:06:07+01:00", http.StatusNoContent, true, past},
		{"no header", admin, "", http.StatusNoContent, true, time.Time{}},
		{"non-manage principal ignored", user, past.Format(time.RFC3339), http.StatusNoContent, true, time.Time{}},
		{"anonymous ignored", anon, past.Format(time.RFC3339), http.StatusNoContent, true, time.Time{}},
		{"nil principal ignored", nil, past.Format(time.RFC3339), http.StatusNoContent, true, time.Time{}},
		{"non-manage garbage is still ignored not rejected", user, "garbage", http.StatusNoContent, true, time.Time{}},
		{"admin future rejected", admin, future, http.StatusBadRequest, false, time.Time{}},
		{"admin garbage rejected", admin, "yesterday", http.StatusBadRequest, false, time.Time{}},
		{"admin epoch rejected", admin, "1970-01-01T00:00:00Z", http.StatusBadRequest, false, time.Time{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			next, status := run(t, tc.principal, tc.header)
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", status, tc.wantStatus)
			}

			if next.called != tc.wantCalled {
				t.Fatalf("handler called = %v, want %v", next.called, tc.wantCalled)
			}

			if !next.createdAt.Equal(tc.wantTime) {
				t.Errorf("created_at = %v, want %v", next.createdAt, tc.wantTime)
			}

			if next.rawHeader != "" {
				t.Errorf("raw header leaked to handler: %q", next.rawHeader)
			}
		})
	}
}
