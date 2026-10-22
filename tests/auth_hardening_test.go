// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

// grantTarget creates a permission target and assigns it to a user.
func grantTarget(t *testing.T, db *MockDatabase, userID, id, repoFilter, pathPattern string, actions ...string) *models.PermissionTarget {
	t.Helper()

	ctx := context.Background()
	now := time.Now()
	target := &models.PermissionTarget{
		ID:               id,
		Name:             id,
		RepositoryFilter: repoFilter,
		PathPattern:      pathPattern,
		Actions:          actions,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if _, err := db.CreatePermissionTarget(ctx, target); err != nil {
		t.Fatalf("CreatePermissionTarget: %v", err)
	}

	if err := db.CreatePermissionAssignment(ctx, &models.PermissionAssignment{
		PermissionTargetID: target.ID,
		PrincipalType:      "user",
		PrincipalID:        userID,
	}); err != nil {
		t.Fatalf("CreatePermissionAssignment: %v", err)
	}

	return target
}

func newBareUser(t *testing.T, db *MockDatabase, id string) *models.User {
	t.Helper()

	now := time.Now()

	user, err := db.CreateUser(context.Background(), &models.User{ID: id, Username: id, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	return user
}

func serve(h http.Handler, method, path string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	if mutate != nil {
		mutate(req)
	}

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)

	return rw
}

func bearerHeader(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

// P0 (#1): SPA-shaped URLs must never be an authentication bypass for anything
// but a browser page load.
func TestRBAC_SPAShapedPathsRequireAuthUnlessBrowserNavigation(t *testing.T) {
	db := newMockDatabase()
	log := newMWLogger(t)

	var reached, spa atomic.Int32

	spaHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		spa.Add(1)
		w.WriteHeader(http.StatusOK)
	})

	mw := newRBACMiddleware(db, middleware.RBACConfig{SPAHandler: spaHandler}, log)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	}))

	spaPaths := []string{
		"/repos/myrepo",
		"/repos/myrepo/edit",
		"/repos/myrepo/browse",
		"/repos/myrepo/graph",
		"/repos/myrepo/packages/npm/lodash",
	}

	for _, path := range spaPaths {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			rw := serve(handler, method, path, func(r *http.Request) {
				// Even with browser-looking headers a mutation is never a page load.
				r.Header.Set("Accept", "text/html")
				r.Header.Set("Sec-Fetch-Mode", "navigate")
			})
			if rw.Code != http.StatusUnauthorized {
				t.Errorf("anonymous %s %s = %d, want 401", method, path, rw.Code)
			}
		}

		// A package-manager style GET (no navigation signals) is a data request.
		rw := serve(handler, http.MethodGet, path, func(r *http.Request) { r.Header.Set("Accept", "application/json") })
		if rw.Code != http.StatusUnauthorized {
			t.Errorf("anonymous non-HTML GET %s = %d, want 401", path, rw.Code)
		}

		// A bogus bearer header does not authenticate either.
		rw = serve(handler, http.MethodGet, path, func(r *http.Request) {
			r.Header.Set("Accept", "text/html")
			bearerHeader("anything")(r)
		})
		if rw.Code != http.StatusUnauthorized {
			t.Errorf("bogus-bearer GET %s = %d, want 401", path, rw.Code)
		}
	}

	if reached.Load() != 0 {
		t.Fatalf("downstream handler reached %d times without authentication", reached.Load())
	}

	// Browser page loads are served by the SPA handler directly.
	for _, path := range spaPaths {
		rw := serve(handler, http.MethodGet, path, func(r *http.Request) {
			r.Header.Set("Accept", "text/html,application/xhtml+xml")
			r.Header.Set("Sec-Fetch-Mode", "navigate")
		})
		if rw.Code != http.StatusOK {
			t.Errorf("browser GET %s = %d, want 200", path, rw.Code)
		}
	}

	if got := spa.Load(); got != int32(len(spaPaths)) {
		t.Errorf("SPA handler served %d page loads, want %d", got, len(spaPaths))
	}

	if reached.Load() != 0 {
		t.Errorf("page loads must not reach the format handlers, reached %d", reached.Load())
	}
}

func TestRBAC_NoSPAHandlerMeansFullEvaluation(t *testing.T) {
	db := newMockDatabase()
	handler := newRBACMiddleware(db, middleware.RBACConfig{}, newMWLogger(t))(noopHandler(http.StatusOK))

	rw := serve(handler, http.MethodGet, "/repos/myrepo/edit", func(r *http.Request) {
		r.Header.Set("Accept", "text/html")
	})
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("without an SPA handler an SPA-shaped GET needs auth, got %d", rw.Code)
	}
}

func TestRBAC_StaticAssetsStayPublic(t *testing.T) {
	db := newMockDatabase()
	handler := newRBACMiddleware(db, middleware.RBACConfig{}, newMWLogger(t))(noopHandler(http.StatusOK))

	for _, path := range []string{"/", "/login", "/assets/app.js", "/favicon.ico"} {
		if rw := serve(handler, http.MethodGet, path, nil); rw.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rw.Code)
		}
	}

	// ...but not for mutations.
	if rw := serve(handler, http.MethodPost, "/login", nil); rw.Code != http.StatusUnauthorized {
		t.Errorf("POST /login anonymous = %d, want 401", rw.Code)
	}
}

func TestRBAC_RejectsPathTraversalAndEncodedSeparators(t *testing.T) {
	db := newMockDatabase()
	handler := newRBACMiddleware(db, middleware.RBACConfig{AllowAnonymousRead: true}, newMWLogger(t))(noopHandler(http.StatusOK))

	for _, path := range []string{
		"/repos/public/../private/pkg",
		"/repos/public%2F..%2Fprivate/pkg",
		"/repos/a%2Fb/pkg",
		"/v2/a%2Fb/manifests/latest",
	} {
		if rw := serve(handler, http.MethodGet, path, nil); rw.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rw.Code)
		}
	}
}

func TestRBAC_BasicAuthWithTokenPassword(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	user := newBareUser(t, db, "ci-owner")
	grantTarget(t, db, user.ID, "ci-read", ".*", ".*", "read")

	tokenStr, _, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "ci", user.ID, []string{}, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	var seen *auth.Principal

	handler := newRBACMiddleware(db, middleware.RBACConfig{}, newMWLogger(t))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = middleware.PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	basic := func(user, pass string) func(*http.Request) {
		return func(r *http.Request) {
			r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
		}
	}

	rw := serve(handler, http.MethodGet, "/repos/app/pkg", basic("ci-bot", tokenStr))
	if rw.Code != http.StatusOK {
		t.Fatalf("Basic auth with a token password = %d, want 200", rw.Code)
	}

	if seen == nil || seen.Username != user.Username || seen.Credential != auth.CredentialBasicToken || seen.TokenKind != auth.TokenKindAccess {
		t.Errorf("unexpected principal %+v", seen)
	}

	rw = serve(handler, http.MethodGet, "/repos/app/pkg", basic("ci-bot", "oa_pat_"+strings.Repeat("0", 64)))
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("Basic auth with an unknown token = %d, want 401", rw.Code)
	}

	if got := rw.Header().Values("WWW-Authenticate"); len(got) != 2 || !strings.HasPrefix(got[1], "Basic ") {
		t.Errorf("401 on /repos/ must challenge with Basic so package managers send credentials, got %v", got)
	}

	rw = serve(handler, http.MethodGet, "/api/v1/auth/me", nil)
	if got := rw.Header().Values("WWW-Authenticate"); len(got) != 1 || strings.HasPrefix(got[0], "Basic") {
		t.Errorf("/api/ must not send a Basic challenge (browser login dialog), got %v", got)
	}
}

func TestRBAC_TokenScopesIntersectOwnerPermissions(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	user := newBareUser(t, db, "scoped-owner")
	scopeA := grantTarget(t, db, user.ID, "read-a", "a", ".*", "read")
	grantTarget(t, db, user.ID, "read-b", "b", ".*", "read")

	scoped, _, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "scoped", user.ID, []string{scopeA.ID}, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	full, _, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "full", user.ID, []string{}, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	handler := newRBACMiddleware(db, middleware.RBACConfig{}, newMWLogger(t))(noopHandler(http.StatusOK))

	for _, tc := range []struct {
		name  string
		token string
		path  string
		want  int
	}{
		{"scoped token reads the scoped repo", scoped, "/repos/a/pkg", http.StatusOK},
		{"scoped token cannot read the other repo", scoped, "/repos/b/pkg", http.StatusForbidden},
		{"unscoped token reads both (a)", full, "/repos/a/pkg", http.StatusOK},
		{"unscoped token reads both (b)", full, "/repos/b/pkg", http.StatusOK},
	} {
		if rw := serve(handler, http.MethodGet, tc.path, bearerHeader(tc.token)); rw.Code != tc.want {
			t.Errorf("%s: GET %s = %d, want %d", tc.name, tc.path, rw.Code, tc.want)
		}
	}

	// A scope naming a target the owner does not hold grants nothing.
	bogus, _, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "bogus", user.ID, []string{"no-such-target"}, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	if rw := serve(handler, http.MethodGet, "/repos/a/pkg", bearerHeader(bogus)); rw.Code != http.StatusForbidden {
		t.Errorf("token with a foreign scope = %d, want 403", rw.Code)
	}
}

func TestRBAC_DeployTokenNeverManages(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	user := newBareUser(t, db, "deployer")
	grantTarget(t, db, user.ID, "everything", ".*", ".*", "read", "write", "delete", "manage")

	deploy, _, err := auth.CreateToken(ctx, db, auth.TokenKindDeploy, "deploy", user.ID, []string{}, nil)
	if err != nil {
		t.Fatalf("CreateToken deploy: %v", err)
	}

	access, _, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "access", user.ID, []string{}, nil)
	if err != nil {
		t.Fatalf("CreateToken access: %v", err)
	}

	handler := newRBACMiddleware(db, middleware.RBACConfig{}, newMWLogger(t))(noopHandler(http.StatusOK))

	if rw := serve(handler, http.MethodGet, "/api/v1/users", bearerHeader(deploy)); rw.Code != http.StatusForbidden {
		t.Errorf("deploy token on management API = %d, want 403", rw.Code)
	}

	if rw := serve(handler, http.MethodPut, "/repos/app/pkg", bearerHeader(deploy)); rw.Code != http.StatusOK {
		t.Errorf("deploy token publishing = %d, want 200", rw.Code)
	}

	if rw := serve(handler, http.MethodGet, "/api/v1/users", bearerHeader(access)); rw.Code != http.StatusOK {
		t.Errorf("access token of an admin on management API = %d, want 200", rw.Code)
	}
}

func TestRBAC_SelfServiceBypassIsSessionOnly(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	user := newBareUser(t, db, "plain-user")
	// Deliberately no manage permission anywhere.
	grantTarget(t, db, user.ID, "read-only", ".*", ".*", "read")

	cookie, _, err := auth.CreateSession(ctx, db, user.ID, "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	token, _, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "pat", user.ID, []string{}, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	handler := newRBACMiddleware(db, middleware.RBACConfig{}, newMWLogger(t))(noopHandler(http.StatusOK))
	withCookie := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie}) }

	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/auth/tokens"},
		{http.MethodPost, "/api/v1/auth/tokens"},
		{http.MethodDelete, "/api/v1/auth/tokens/t1"},
		{http.MethodPost, "/api/v1/users/" + user.ID + "/tokens"},
		{http.MethodPatch, "/api/v1/users/" + user.ID},
		{http.MethodPost, "/api/v1/auth/change-password"},
	} {
		if rw := serve(handler, tc.method, tc.path, withCookie); rw.Code != http.StatusOK {
			t.Errorf("session %s %s = %d, want 200 (self-service)", tc.method, tc.path, rw.Code)
		}

		if rw := serve(handler, tc.method, tc.path, bearerHeader(token)); rw.Code != http.StatusForbidden {
			t.Errorf("token %s %s = %d, want 403 (tokens cannot self-serve)", tc.method, tc.path, rw.Code)
		}
	}

	// Another user's account is not self-service even with a session.
	for _, tc := range []struct{ method, path string }{
		{http.MethodPatch, "/api/v1/users/someone-else"},
		{http.MethodPost, "/api/v1/users/someone-else/tokens"},
		{http.MethodGet, "/api/v1/users"},
	} {
		if rw := serve(handler, tc.method, tc.path, withCookie); rw.Code != http.StatusForbidden {
			t.Errorf("session %s %s = %d, want 403", tc.method, tc.path, rw.Code)
		}
	}

	// GET /auth/me works for tokens too.
	if rw := serve(handler, http.MethodGet, "/api/v1/auth/me", bearerHeader(token)); rw.Code != http.StatusOK {
		t.Errorf("token GET /auth/me = %d, want 200", rw.Code)
	}
}

func TestRBAC_RepoScopedAPIRoutes(t *testing.T) {
	db := newMockDatabase()
	user := newBareUser(t, db, "uploader")
	grantTarget(t, db, user.ID, "app-rw", "app", ".*", "read", "write")

	cookie, _, err := auth.CreateSession(context.Background(), db, user.ID, "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	handler := newRBACMiddleware(db, middleware.RBACConfig{}, newMWLogger(t))(noopHandler(http.StatusOK))
	withCookie := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie}) }

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/v1/repositories", http.StatusOK},
		{http.MethodGet, "/api/v1/repository/app", http.StatusOK},
		{http.MethodGet, "/api/v1/repository/other", http.StatusForbidden},
		{http.MethodGet, "/api/v1/repositories/app/packages", http.StatusOK},
		{http.MethodPost, "/api/v1/repositories/app/debs", http.StatusOK},
		{http.MethodPost, "/api/v1/repositories/other/debs", http.StatusForbidden},
		{http.MethodDelete, "/api/v1/repositories/app/debs/1", http.StatusForbidden}, // write, not delete
		{http.MethodPost, "/api/v1/repositories", http.StatusForbidden},
		{http.MethodDelete, "/api/v1/repository/app", http.StatusForbidden},
		{http.MethodGet, "/api/v1/admin/users", http.StatusForbidden},
		{http.MethodPut, "/api/v1/repositories/app/members", http.StatusForbidden},
	} {
		if rw := serve(handler, tc.method, tc.path, withCookie); rw.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rw.Code, tc.want)
		}
	}

	// An anonymous caller never gets the list-style pass-through.
	if rw := serve(handler, http.MethodGet, "/api/v1/repositories", nil); rw.Code != http.StatusUnauthorized {
		t.Errorf("anonymous GET /api/v1/repositories = %d, want 401", rw.Code)
	}
}

func TestCSRF_AppliesToSessionsButNotTokenHeaders(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	user := newBareUser(t, db, "csrf-user")
	grantTarget(t, db, user.ID, "csrf-all", ".*", ".*", "read", "write", "delete", "manage")

	cookie, _, err := auth.CreateSession(ctx, db, user.ID, "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	token, _, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "ci", user.ID, []string{}, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	rbac := newRBACMiddleware(db, middleware.RBACConfig{}, newMWLogger(t))
	handler := rbac(middleware.CSRFMiddleware("")(noopHandler(http.StatusOK)))

	withCookie := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie}) }

	if rw := serve(handler, http.MethodPost, "/api/v1/repositories", withCookie); rw.Code != http.StatusForbidden ||
		!strings.Contains(rw.Body.String(), "csrf_protection") {
		t.Errorf("session POST without a CSRF token = %d %s, want 403 csrf_protection", rw.Code, rw.Body.String())
	}

	if rw := serve(handler, http.MethodPost, "/api/v1/repositories", func(r *http.Request) {
		withCookie(r)
		r.AddCookie(&http.Cookie{Name: middleware.CSRFTokenCookieName, Value: "tok"})
		r.Header.Set("X-Csrf-Token", "tok")
	}); rw.Code != http.StatusOK {
		t.Errorf("session POST with a matching CSRF token = %d, want 200", rw.Code)
	}

	if rw := serve(handler, http.MethodPost, "/api/v1/repositories", bearerHeader(token)); rw.Code != http.StatusOK {
		t.Errorf("bearer-token POST (no cookie, no CSRF token) = %d, want 200", rw.Code)
	}

	// A cookie plus a forged bearer header must not skip CSRF: the valid
	// session is the credential, and it is the ambient one.
	if rw := serve(handler, http.MethodPost, "/api/v1/repositories", func(r *http.Request) {
		withCookie(r)
		r.Header.Set("Authorization", "Bearer not-a-token")
	}); rw.Code != http.StatusForbidden {
		t.Errorf("session + junk bearer POST without CSRF = %d, want 403", rw.Code)
	}
}

func TestRBAC_LegacyKeySkipsCSRFButNeedsEnabling(t *testing.T) {
	db := newMockDatabase()
	cfg := middleware.RBACConfig{LegacyAPIKey: "legacy-secret-key", LegacyAPIKeyEnabled: true}
	handler := newRBACMiddleware(db, cfg, newMWLogger(t))(middleware.CSRFMiddleware("")(noopHandler(http.StatusOK)))

	if rw := serve(handler, http.MethodPost, "/api/v1/repositories", func(r *http.Request) {
		r.Header.Set("X-Api-Key", "legacy-secret-key")
	}); rw.Code != http.StatusOK {
		t.Errorf("legacy key POST = %d, want 200", rw.Code)
	}

	disabled := newRBACMiddleware(db, middleware.RBACConfig{LegacyAPIKey: "legacy-secret-key"}, newMWLogger(t))(noopHandler(http.StatusOK))
	if rw := serve(disabled, http.MethodGet, "/repos/a/b", func(r *http.Request) {
		r.Header.Set("X-Api-Key", "legacy-secret-key")
	}); rw.Code != http.StatusUnauthorized {
		t.Errorf("legacy key while disabled = %d, want 401", rw.Code)
	}
}

func TestEvaluator_PatternsAreAnchoredAndEmptyOnesGrantNothing(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	log := newMWLogger(t)
	user := newBareUser(t, db, "pattern-user")
	grantTarget(t, db, user.ID, "dev-only", "dev", ".*", "read")
	grantTarget(t, db, user.ID, "empty-filter", "", ".*", "read", "write")
	grantTarget(t, db, user.ID, "empty-path", ".*", "", "delete")

	eval := auth.NewEvaluator(db, log, false)
	p := auth.Principal{UserID: &user.ID, Username: user.Username}

	for _, tc := range []struct {
		repo, path, action string
		want               bool
	}{
		{"dev", "x", "read", true},
		{"devops", "x", "read", false}, // unanchored "dev" used to match "devops"
		{"prod-dev", "x", "read", false},
		{"dev", "x", "write", false}, // empty repository_filter must not match everything
		{"anything", "x", "write", false},
		{"anything", "x", "delete", false}, // empty path_pattern must not match everything
	} {
		got, err := eval.Evaluate(ctx, p, tc.repo, tc.path, tc.action)
		if err != nil {
			t.Fatalf("Evaluate(%q,%q,%q): %v", tc.repo, tc.path, tc.action, err)
		}

		if got != tc.want {
			t.Errorf("Evaluate(repo=%q path=%q action=%q) = %v, want %v", tc.repo, tc.path, tc.action, got, tc.want)
		}
	}
}

func TestCompilePattern(t *testing.T) {
	if _, err := auth.CompilePattern(""); err == nil {
		t.Error("empty pattern must be rejected")
	}

	if _, err := auth.CompilePattern("   "); err == nil {
		t.Error("blank pattern must be rejected")
	}

	if _, err := auth.CompilePattern("(unclosed"); err == nil {
		t.Error("invalid regexp must be rejected")
	}

	star, err := auth.CompilePattern("*")
	if err != nil || !star.MatchString("anything/at/all") {
		t.Errorf("\"*\" must act as match-all, err=%v", err)
	}

	re, err := auth.CompilePattern("npm-.*")
	if err != nil {
		t.Fatalf("CompilePattern: %v", err)
	}

	if !re.MatchString("npm-public") || re.MatchString("my-npm-public") {
		t.Error("pattern must be anchored at both ends")
	}
}

func TestValidatePassword(t *testing.T) {
	for _, tc := range []struct {
		name string
		pw   string
		want error
	}{
		{"empty", "", auth.ErrPasswordTooShort},
		{"short", "elevenchars", auth.ErrPasswordTooShort},
		{"exactly the minimum", "twelve-chars", nil},
		{"long passphrase", "correct horse battery staple", nil},
		{"multibyte counts runes", "пароль-пароль", nil},
		{"oversized", strings.Repeat("a", auth.MaxPasswordLength+1), auth.ErrPasswordTooLong},
	} {
		if got := auth.ValidatePassword(tc.pw); !errors.Is(got, tc.want) {
			t.Errorf("%s: ValidatePassword = %v, want %v", tc.name, got, tc.want)
		}
	}
}

type sessionTouchCountingDB struct {
	*MockDatabase

	touches atomic.Int32
}

func (d *sessionTouchCountingDB) UpdateSessionLastSeen(ctx context.Context, id string, exp time.Time) error {
	d.touches.Add(1)
	return d.MockDatabase.UpdateSessionLastSeen(ctx, id, exp)
}

func TestSession_TouchIsThrottledAndCappedByAbsoluteLifetime(t *testing.T) {
	ctx := context.Background()
	db := &sessionTouchCountingDB{MockDatabase: newMockDatabase()}
	user := newBareUser(t, db.MockDatabase, "touch-user")

	cookie, session, err := auth.CreateSession(ctx, db, user.ID, "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Fresh session: last seen just now, nothing to write.
	if err := auth.TouchSession(ctx, db, session); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}

	if db.touches.Load() != 0 {
		t.Fatalf("a session seen moments ago was touched %d times, want 0", db.touches.Load())
	}

	// Seen two minutes ago: one write.
	session.LastSeenAt = time.Now().Add(-2 * time.Minute)

	if err := auth.TouchSession(ctx, db, session); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}

	if db.touches.Load() != 1 {
		t.Fatalf("stale session touched %d times, want 1", db.touches.Load())
	}

	// Near the absolute limit the idle extension is capped at the limit.
	abs := auth.SessionAbsoluteLifetime()
	session.CreatedAt = time.Now().Add(-abs + time.Hour)
	session.LastSeenAt = time.Now().Add(-2 * time.Minute)

	if err := auth.TouchSession(ctx, db, session); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}

	stored, _ := db.GetSessionByTokenHash(ctx, session.TokenHash)
	if limit := session.CreatedAt.Add(abs); stored.ExpiresAt.After(limit.Add(time.Second)) {
		t.Errorf("expiry %v extends past the absolute limit %v", stored.ExpiresAt, limit)
	}

	// Beyond the absolute lifetime the session is dead even if the idle expiry is in the future.
	stored.CreatedAt = time.Now().Add(-abs - time.Hour)
	stored.ExpiresAt = time.Now().Add(time.Hour)

	if got, err := auth.ValidateSession(ctx, db, cookie); err != nil || got != nil {
		t.Errorf("session past its absolute lifetime validated: %v, %v", got, err)
	}
}

func TestSession_RevokeOtherSessionsKeepsCurrent(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	user := newBareUser(t, db, "revoke-user")
	other := newBareUser(t, db, "bystander")

	keep, keepSession, err := auth.CreateSession(ctx, db, user.ID, "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	dropA, _, _ := auth.CreateSession(ctx, db, user.ID, "127.0.0.1", "ua")
	dropB, _, _ := auth.CreateSession(ctx, db, user.ID, "127.0.0.1", "ua")
	bystander, _, _ := auth.CreateSession(ctx, db, other.ID, "127.0.0.1", "ua")

	if err := auth.RevokeOtherSessions(ctx, db, user.ID, keep); err != nil {
		t.Fatalf("RevokeOtherSessions: %v", err)
	}

	valid := func(cookie string) bool {
		s, err := auth.ValidateSession(ctx, db, cookie)
		return err == nil && s != nil
	}

	if !valid(keep) || keepSession == nil {
		t.Error("the caller's own session must survive")
	}

	if valid(dropA) || valid(dropB) {
		t.Error("the user's other sessions must be revoked")
	}

	if !valid(bystander) {
		t.Error("another user's session must not be touched")
	}

	if err := auth.RevokeAllSessions(ctx, db, user.ID); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}

	if valid(keep) {
		t.Error("RevokeAllSessions must revoke every session of the user")
	}

	if !valid(bystander) {
		t.Error("RevokeAllSessions must not touch other users")
	}

	// An unusable cookie keeps nothing.
	again, _, _ := auth.CreateSession(ctx, db, user.ID, "127.0.0.1", "ua")
	if err := auth.RevokeOtherSessions(ctx, db, user.ID, "not-a-cookie"); err != nil {
		t.Fatalf("RevokeOtherSessions: %v", err)
	}

	if valid(again) {
		t.Error("with a malformed keep-cookie every session of the user is revoked")
	}
}
