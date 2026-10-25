// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

type benchRBACDB struct {
	databasetest.BaseMockDB

	targets  []*models.PermissionTarget
	repos    map[string]*models.Repository
	tokens   map[string]*models.Token
	users    map[string]*models.User
	sessions map[string]*models.Session
}

func (d *benchRBACDB) GetPermissionTargetsForPrincipal(context.Context, string, []string) ([]*models.PermissionTarget, error) {
	return d.targets, nil
}

func (d *benchRBACDB) GetPermissionTargetByName(context.Context, string) (*models.PermissionTarget, error) {
	return nil, nil
}

func (d *benchRBACDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return d.repos[name], nil
}

func (d *benchRBACDB) GetTokenByHash(_ context.Context, hash string) (*models.Token, error) {
	return d.tokens[hash], nil
}

func (d *benchRBACDB) GetUserByID(_ context.Context, id string) (*models.User, error) {
	return d.users[id], nil
}

func (d *benchRBACDB) GetSessionByTokenHash(_ context.Context, hash string) (*models.Session, error) {
	return d.sessions[hash], nil
}

func (d *benchRBACDB) ListGroupsByUser(context.Context, string) ([]*models.Group, error) {
	return []*models.Group{{ID: "g1"}, {ID: "g2"}}, nil
}

func (d *benchRBACDB) CreateSession(_ context.Context, s *models.Session) (*models.Session, error) {
	s.ID = "sess-1"
	return s, nil
}

func (d *benchRBACDB) CreateToken(_ context.Context, t *models.Token) (*models.Token, error) {
	t.ID = "tok-1"
	return t, nil
}

func benchRBACTargets(n int) []*models.PermissionTarget {
	targets := make([]*models.PermissionTarget, n)

	for i := range targets {
		targets[i] = &models.PermissionTarget{
			ID:               "target-" + strconv.Itoa(i),
			RepositoryFilter: fmt.Sprintf("team-%d-.*", i),
			PathPattern:      ".*",
			Actions:          []string{"read", "write", "delete"},
		}
	}

	return targets
}

// benchRBACHarness wires RBACMiddleware over an in-memory database with a user
// holding n permission targets (only the last matches "team-<n-1>-app"), plus a
// bearer token and a session for that user.
func benchRBACHarness(b *testing.B, n int) (http.Handler, string, *http.Cookie) {
	b.Helper()

	log, err := logger.New("error", "json", "/dev/null")
	if err != nil {
		b.Fatal(err)
	}

	now := time.Now()
	db := &benchRBACDB{
		targets:  benchRBACTargets(n),
		repos:    map[string]*models.Repository{"pub": {Name: "pub", Public: true}},
		tokens:   map[string]*models.Token{},
		users:    map[string]*models.User{"user-1": {ID: "user-1", Username: "jane"}},
		sessions: map[string]*models.Session{},
	}

	ctx := context.Background()

	raw, tok, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "ci", "user-1", nil, nil)
	if err != nil {
		b.Fatal(err)
	}

	tok.LastUsedAt = &now // throttle last_used_at writes, as in steady state
	db.tokens[tok.TokenHash] = tok

	cookieValue, sess, err := auth.CreateSession(ctx, db, "user-1", "", "")
	if err != nil {
		b.Fatal(err)
	}

	db.sessions[sess.TokenHash] = sess

	evaluator := auth.NewEvaluator(db, log, false)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := RBACMiddleware(RBACConfig{}, db, evaluator, log)(next)

	return handler, raw, &http.Cookie{Name: auth.SessionCookieName, Value: cookieValue}
}

type statusWriter struct {
	header http.Header
	status int
}

func (w *statusWriter) Header() http.Header { return w.header }
func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}
func (w *statusWriter) Write(p []byte) (int, error) { return len(p), nil }

// benchServe replays req against h and fails if the outcome (allowed or
// rejected) is not the one the scenario is meant to measure.
func benchServe(b *testing.B, h http.Handler, req *http.Request, wantAllowed bool) {
	b.Helper()

	w := &statusWriter{header: http.Header{}}

	h.ServeHTTP(w, req)

	if allowed := w.status < 400; allowed != wantAllowed {
		b.Fatalf("status %d: allowed=%v, want %v", w.status, allowed, wantAllowed)
	}

	b.ReportAllocs()

	for b.Loop() {
		w.status = 0
		h.ServeHTTP(w, req)
	}
}

// BenchmarkRBACMiddleware drives full RBACMiddleware.ServeHTTP requests.
func BenchmarkRBACMiddleware(b *testing.B) {
	for _, n := range []int{1, 10, 100} {
		handler, token, cookie := benchRBACHarness(b, n)
		path := fmt.Sprintf("/repos/team-%d-app/lodash", n-1)

		bearer := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
		bearer.Header.Set("Authorization", "Bearer "+token)

		basic := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
		basic.SetBasicAuth("ci", token)

		session := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
		session.AddCookie(cookie)

		b.Run(fmt.Sprintf("targets=%d/bearer_token", n), func(b *testing.B) { benchServe(b, handler, bearer, true) })
		b.Run(fmt.Sprintf("targets=%d/basic_token", n), func(b *testing.B) { benchServe(b, handler, basic, true) })
		b.Run(fmt.Sprintf("targets=%d/session", n), func(b *testing.B) { benchServe(b, handler, session, true) })
	}

	handler, token, _ := benchRBACHarness(b, 10)

	anonPublic := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/pub/lodash", http.NoBody)
	anonDenied := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/team-0-app/lodash", http.NoBody)
	static := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/assets/app.js", http.NoBody)
	apiList := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/repositories", http.NoBody)
	apiList.Header.Set("Authorization", "Bearer "+token)
	ambiguous := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/a%2Fb/x", http.NoBody)

	b.Run("anonymous_public_repo", func(b *testing.B) { benchServe(b, handler, anonPublic, true) })
	b.Run("anonymous_denied", func(b *testing.B) { benchServe(b, handler, anonDenied, false) })
	b.Run("static_asset", func(b *testing.B) { benchServe(b, handler, static, true) })
	b.Run("api_list_any_authenticated", func(b *testing.B) { benchServe(b, handler, apiList, true) })
	b.Run("rejected_ambiguous_path", func(b *testing.B) { benchServe(b, handler, ambiguous, false) })
}

//nolint:gochecknoglobals // benchmark fixtures
var benchRoutes = []struct {
	name   string
	method string
	path   string
}{
	{"api_list", http.MethodGet, "/api/v1/repositories"},
	{"api_repo_data", http.MethodGet, "/api/v1/repositories/app/packages"},
	{"api_admin", http.MethodGet, "/api/v1/admin/users"},
	{"npm_packument", http.MethodGet, "/repos/npm-repo/@scope%2Fname"},
	{"maven_artifact", http.MethodGet, "/repos/maven/my-maven-repo/com/example/lib/1.0/lib-1.0.jar"},
	{"docker_manifest", http.MethodGet, "/v2/org/team/myimage/manifests/sha256:abcd"},
	{"raw_upload", http.MethodPut, "/repos/raw/my-raw-repo/some/nested/path.txt"},
}

func BenchmarkClassifyRoute(b *testing.B) {
	for _, rt := range benchRoutes {
		req := httptest.NewRequestWithContext(context.Background(), rt.method, rt.path, http.NoBody)

		b.Run(rt.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				_ = classifyRoute(req)
			}
		})
	}
}

func BenchmarkHasAmbiguousPath(b *testing.B) {
	for _, rt := range benchRoutes {
		req := httptest.NewRequestWithContext(context.Background(), rt.method, rt.path, http.NoBody)

		b.Run(rt.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				_ = hasAmbiguousPath(req.URL)
			}
		})
	}
}

func BenchmarkExtractRepoAndPath(b *testing.B) {
	for _, rt := range benchRoutes {
		b.Run(rt.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				_, _ = extractRepoAndPath(rt.path)
			}
		})
	}
}

func BenchmarkIsSPANavigation(b *testing.B) {
	nav := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/myrepo/packages/npm/lodash", http.NoBody)
	nav.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	nav.Header.Set("Sec-Fetch-Mode", "navigate")

	api := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/myrepo/lodash/-/lodash-1.0.0.tgz", http.NoBody)

	b.Run("page_navigation", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			_ = isSPANavigation(nav)
		}
	})

	b.Run("package_manager_request", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			_ = isSPANavigation(api)
		}
	})
}

func BenchmarkIsSelfServiceRequest(b *testing.B) {
	uid := "user-1"
	session := &auth.Principal{UserID: &uid, Username: "u", Credential: auth.CredentialSession}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/api/v1/users/user-1/tokens/abc", http.NoBody)

	b.ReportAllocs()

	for b.Loop() {
		_ = isSelfServiceRequest(req, session)
	}
}
