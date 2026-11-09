// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/openartifact/openartifact/pkg/logger"
)

// A peer whose token cannot be checked because the database is down must be
// told to retry (503), not that its token is refused (401): the pulling side
// would report a credentials problem for an outage.
func TestAuthenticateDatabaseDownIsUnavailable(t *testing.T) {
	// Nothing listens on port 1: every query fails to connect.
	db, err := sql.Open("pgx", "postgres://u:p@127.0.0.1:1/x?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = db.Close() })

	log, err := logger.New("fatal", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	h := NewServer(NewStore(db), nil, nil, log).Handler()

	tests := []struct {
		name       string
		token      string
		wantStatus int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"malformed token never reaches the database", "not-a-peer-token", http.StatusUnauthorized},
		{"peer token with the database down", TokenPrefix + "abcdef0123456789", http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/info", nil)
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body)
			}

			if got := rec.Header().Get("Retry-After"); (tt.wantStatus == http.StatusServiceUnavailable) != (got != "") {
				t.Errorf("Retry-After = %q for status %d", got, rec.Code)
			}
		})
	}
}
