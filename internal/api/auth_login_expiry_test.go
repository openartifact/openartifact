// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

type loginExpiryDB struct {
	databasetest.BaseMockDB

	user      *models.User
	expiresAt *time.Time
	sessions  int
}

func (m *loginExpiryDB) GetUserByUsername(context.Context, string) (*models.User, error) {
	return m.user, nil
}

func (m *loginExpiryDB) GetPasswordExpiry(context.Context, string) (*time.Time, error) {
	return m.expiresAt, nil
}

func (m *loginExpiryDB) CreateSession(_ context.Context, s *models.Session) (*models.Session, error) {
	m.sessions++

	return s, nil
}

func (m *loginExpiryDB) UpdateUserLastLogin(context.Context, string, time.Time) error { return nil }

func TestLoginRejectsExpiredGeneratedPassword(t *testing.T) {
	const password = "generated-password-123"

	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}

	expiry := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)

	cases := map[string]struct {
		now       time.Time
		expiresAt *time.Time
		wantOK    bool
	}{
		"before expiry": {now: expiry.Add(-time.Second), expiresAt: &expiry, wantOK: true},
		"at expiry":     {now: expiry, expiresAt: &expiry, wantOK: false},
		"after expiry":  {now: expiry.Add(time.Hour), expiresAt: &expiry, wantOK: false},
		"no expiry":     {now: expiry.Add(1000 * time.Hour), expiresAt: nil, wantOK: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			log, err := logger.New("error", "json", "stdout")
			if err != nil {
				t.Fatal(err)
			}

			db := &loginExpiryDB{
				user:      &models.User{ID: "u1", Username: "admin", PasswordHash: &hash, MustChangePassword: true, Kind: models.UserKindHuman},
				expiresAt: tc.expiresAt,
			}
			h := NewAuthHandler(db, log, nil, nil)
			h.now = func() time.Time { return tc.now }

			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://oa.example.com/api/v1/auth/login",
				strings.NewReader(`{"username":"admin","password":"`+password+`"}`))
			r.Host = "oa.example.com"
			r.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			h.handleLogin(w, r)

			if tc.wantOK {
				if w.Code != http.StatusOK || db.sessions != 1 {
					t.Fatalf("status %d sessions %d, want 200 and one session", w.Code, db.sessions)
				}

				return
			}

			if w.Code != http.StatusUnauthorized || db.sessions != 0 {
				t.Fatalf("status %d sessions %d, want 401 and no session", w.Code, db.sessions)
			}

			// The client gets the same generic error as for a wrong password.
			if !strings.Contains(w.Body.String(), "invalid_credentials") || strings.Contains(w.Body.String(), "expire") {
				t.Errorf("response leaks the expiry: %s", w.Body.String())
			}
		})
	}
}
