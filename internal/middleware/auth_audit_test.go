// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
)

// authAuditDB captures audit rows and serves a minimal user/session store.
type authAuditDB struct {
	databasetest.BaseMockDB

	events   chan *models.AuditLog
	sessions map[string]*models.Session
	users    map[string]*models.User
}

func newAuthAuditDB() *authAuditDB {
	return &authAuditDB{
		events:   make(chan *models.AuditLog, 8),
		sessions: map[string]*models.Session{},
		users:    map[string]*models.User{},
	}
}

func (db *authAuditDB) InsertAuditLog(_ context.Context, event *models.AuditLog) error {
	db.events <- event
	return nil
}

func (db *authAuditDB) CreateSession(_ context.Context, s *models.Session) (*models.Session, error) {
	s.ID = "sess-" + s.TokenHash[:8]
	db.sessions[s.TokenHash] = s

	return s, nil
}

func (db *authAuditDB) GetSessionByTokenHash(_ context.Context, hash string) (*models.Session, error) {
	return db.sessions[hash], nil
}

func (db *authAuditDB) DeleteSession(_ context.Context, hash string) error {
	delete(db.sessions, hash)
	return nil
}

func (db *authAuditDB) GetUserByID(_ context.Context, id string) (*models.User, error) {
	return db.users[id], nil
}

func (db *authAuditDB) next(t *testing.T) *models.AuditLog {
	t.Helper()

	select {
	case e := <-db.events:
		return e
	case <-time.After(time.Second):
		t.Fatal("audit event was not recorded")
		return nil
	}
}

func (db *authAuditDB) expectNone(t *testing.T) {
	t.Helper()

	select {
	case e := <-db.events:
		t.Fatalf("unexpected audit event %q", e.Action)
	case <-time.After(100 * time.Millisecond):
	}
}

func authAuditHarness(t *testing.T, h http.HandlerFunc) (*authAuditDB, http.Handler) {
	t.Helper()

	db := newAuthAuditDB()
	logger := audit.NewLogger(db, nil, nil, 8)
	t.Cleanup(func() { _ = logger.Shutdown(context.Background()) })

	return db, AuthAuditMiddleware(logger, db)(h)
}

func TestAuthAuditLoginSuccessAndFailure(t *testing.T) {
	var cookie string

	db, handler := authAuditHarness(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"password":"hunter2hunter2"`) {
			t.Errorf("handler must still receive the full request body, got %q", body)
		}

		if cookie == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		http.SetCookie(w, &http.Cookie{Name: auth.SessionCookieName, Value: cookie, Path: "/"})
		w.WriteHeader(http.StatusOK)
	})

	login := func() {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/auth/login",
			strings.NewReader(`{"username":"mallory","password":"hunter2hunter2"}`))
		req.RemoteAddr = "203.0.113.9:1111"
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	login()

	failed := db.next(t)
	if failed.Action != "auth.login_failed" {
		t.Fatalf("failed login action = %q, want auth.login_failed", failed.Action)
	}

	if !strings.Contains(string(failed.DetailsJSON), `"actor":"mallory"`) {
		t.Errorf("failed login must record the attempted username, got %s", failed.DetailsJSON)
	}

	if strings.Contains(string(failed.DetailsJSON), "hunter2") {
		t.Errorf("password must never reach the audit log, got %s", failed.DetailsJSON)
	}

	if failed.IPAddress == nil || *failed.IPAddress != "203.0.113.9" {
		t.Errorf("IPAddress = %v, want 203.0.113.9", failed.IPAddress)
	}

	db.users["u1"] = &models.User{ID: "u1", Username: "mallory"}

	raw, _, err := auth.CreateSession(context.Background(), db, "u1", "203.0.113.9", "ua")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	cookie = raw

	login()

	ok := db.next(t)
	if ok.Action != "auth.login" {
		t.Fatalf("login action = %q, want auth.login", ok.Action)
	}

	if ok.ActorID == nil || *ok.ActorID != "u1" || !strings.Contains(string(ok.DetailsJSON), `"actor":"mallory"`) {
		t.Errorf("login must record the authenticated user, got actor_id=%v details=%s", ok.ActorID, ok.DetailsJSON)
	}
}

func TestAuthAuditSkipsRateLimitedLogin(t *testing.T) {
	db, handler := authAuditHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"username":"x","password":"y"}`))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	db.expectNone(t)
}

func TestAuthAuditLogoutAndSSO(t *testing.T) {
	var ssoCookie string

	db, handler := authAuditHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/logout"):
			http.SetCookie(w, &http.Cookie{Name: auth.SessionCookieName, Value: "", Path: "/", MaxAge: -1})
			w.WriteHeader(http.StatusOK)
		default:
			if ssoCookie != "" {
				http.SetCookie(w, &http.Cookie{Name: auth.SessionCookieName, Value: ssoCookie, Path: "/"})
			}

			http.Redirect(w, r, "/", http.StatusFound)
		}
	})

	db.users["u1"] = &models.User{ID: "u1", Username: "alice"}

	raw, _, err := auth.CreateSession(context.Background(), db, "u1", "198.51.100.1", "ua")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Logout: the actor is resolved before the handler deletes the session.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: raw})
	handler.ServeHTTP(httptest.NewRecorder(), req)

	out := db.next(t)
	if out.Action != "auth.logout" || out.ActorID == nil || *out.ActorID != "u1" {
		t.Errorf("logout event = %q actor=%v, want auth.logout for u1", out.Action, out.ActorID)
	}

	// SSO callback that redirects without creating a session is a failure.
	req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/auth/sso/oidc/corp/callback?code=x&state=y", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if failed := db.next(t); failed.Action != "auth.login_failed" {
		t.Errorf("sso callback without a session = %q, want auth.login_failed", failed.Action)
	}

	// SSO callback that sets a session cookie is a successful sso login.
	ssoCookie = raw

	req = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/auth/sso/saml/corp/acs", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	sso := db.next(t)
	if sso.Action != "auth.sso_login" || sso.ActorType == nil || *sso.ActorType != "sso" {
		t.Errorf("sso login = %q type=%v, want auth.sso_login/sso", sso.Action, sso.ActorType)
	}
}

func TestAuthAuditIgnoresOtherRoutes(t *testing.T) {
	db, handler := authAuditHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/auth/login"},
		{http.MethodPost, "/api/v1/repositories"},
		{http.MethodGet, "/repos/x/y"},
	} {
		handler.ServeHTTP(httptest.NewRecorder(),
			httptest.NewRequestWithContext(context.Background(), tc.method, tc.path, nil))
	}

	db.expectNone(t)
}

func TestRouteToActionAuthEvents(t *testing.T) {
	for _, tc := range []struct {
		method, route, want string
	}{
		{"POST", "/api/v1/auth/change-password", "auth.password_changed"},
		{"POST", "/api/v1/auth/tokens", "auth.token_created"},
		{"POST", "/api/v1/users/{id}/tokens", "auth.token_created"},
		{"DELETE", "/api/v1/auth/tokens/{id}", "auth.token_revoked"},
		{"DELETE", "/api/v1/users/{id}/tokens/{tokenId}", "auth.token_revoked"},
		{"POST", "/api/v1/repositories", "repository.created"},
		{"DELETE", "/api/v1/repository/{name}", "repository.deleted"},
		// The trash handlers audit restore/purge themselves (internal/trash).
		{"POST", "/api/v1/trash/{kind}/{id}/restore", ""},
		{"DELETE", "/api/v1/trash/{kind}/{id}", ""},
		{"GET", "/api/v1/auth/tokens", ""},
	} {
		if got := routeToAction(tc.method, tc.route); got != tc.want {
			t.Errorf("routeToAction(%s, %s) = %q, want %q", tc.method, tc.route, got, tc.want)
		}
	}
}
