// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/recovery"
	"github.com/openartifact/openartifact/pkg/logger"
)

type fakeRecoverySource struct {
	st    recovery.Status
	err   error
	calls int
}

func (f *fakeRecoverySource) Status(context.Context) (recovery.Status, error) {
	f.calls++

	return f.st, f.err
}

func TestAdminRecoveryStatus(t *testing.T) {
	t.Parallel()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		anon     bool
		src      *fakeRecoverySource
		wantCode int
		wantBody string
		wantCall bool
	}{
		{"admin gets the status", false, &fakeRecoverySource{st: recovery.Status{Enabled: true, Backlog: 3}}, http.StatusOK, `"backlog":3`, true},
		{"anonymous is refused", true, &fakeRecoverySource{}, http.StatusForbidden, "", false},
		{"failure does not leak", false, &fakeRecoverySource{err: errors.New("pq: password authentication failed for user oa")}, http.StatusInternalServerError, "failed to read recovery journal status", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := chi.NewRouter()
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					p := &auth.Principal{IsLegacy: true}
					if tc.anon {
						p = &auth.Principal{IsAnonymous: true}
					}

					next.ServeHTTP(w, req.WithContext(middleware.WithPrincipal(req.Context(), p)))
				})
			})
			NewAdminRecoveryHandler(tc.src, log, nil).Register(r)

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/admin/recovery/status", http.NoBody))

			if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), tc.wantBody) || (tc.src.calls > 0) != tc.wantCall {
				t.Fatalf("code=%d body=%s calls=%d", rec.Code, rec.Body, tc.src.calls)
			}

			if strings.Contains(rec.Body.String(), "password") {
				t.Fatalf("response leaks the error: %s", rec.Body)
			}

			if tc.wantCode == http.StatusOK {
				var st recovery.Status
				if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || !st.Enabled {
					t.Fatalf("decode: %v %+v", err, st)
				}
			}
		})
	}
}
