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
	"strings"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

const ghToken = "gh-token"

type apiHit struct {
	method, path, query, accept, apiVersion, auth string
}

// fakeAPI is a loopback upstream (GitHub API or an OAuth 2.0 server) that
// records every request.
type fakeAPI struct {
	srv *httptest.Server

	mu   sync.Mutex
	hits []apiHit
}

func newFakeAPI(t *testing.T, routes map[string]http.HandlerFunc) *fakeAPI {
	t.Helper()

	// The upstream listens on loopback, which the SSRF dial guard blocks.
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits = append(f.hits, apiHit{
			r.Method, r.URL.Path, r.URL.RawQuery,
			r.Header.Get("Accept"), r.Header.Get("X-Github-Api-Version"), r.Header.Get("Authorization"),
		})
		f.mu.Unlock()

		if h, ok := routes[r.URL.Path]; ok {
			h(w, r)
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(f.srv.Close)

	return f
}

func (f *fakeAPI) hitsOn(path string) []apiHit {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []apiHit

	for _, h := range f.hits {
		if h.path == path {
			out = append(out, h)
		}
	}

	return out
}

func jsonRoute(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

// bearerRoute only answers requests carrying the expected access token.
func bearerRoute(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+ghToken {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}

		h(w, r)
	}
}

// tokenRoute redeems "good-code" (with the PKCE verifier and client secret the
// handler must send) for ghToken.
func tokenRoute(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()

	if r.FormValue("code") != "good-code" || r.FormValue("code_verifier") != "fake-verifier" ||
		r.FormValue("client_secret") != "test-secret" || r.FormValue("client_id") != "test-client" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))

		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"access_token":"` + ghToken + `","token_type":"bearer","scope":"read:user"}`))
}

type ghEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func ghMembership(login string) map[string]any {
	return map[string]any{"state": "active", "organization": map[string]any{"login": login}}
}

func ghTeam(org, slug string) map[string]any {
	return map[string]any{"slug": slug, "organization": map[string]any{"login": org}}
}

// pagedRoute serves pages[n-1] for ?page=n and a Link rel=next header while
// more pages remain.
func pagedRoute(srvURL func() string, pages [][]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			_, _ = fmt.Sscan(p, &page)
		}

		if page < len(pages) {
			next := fmt.Sprintf("%s%s?state=active&per_page=100&page=%d", srvURL(), r.URL.Path, page+1)
			w.Header().Set("Link", `<`+next+`>; rel="next", <`+next+`>; rel="last"`)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pages[page-1])
	}
}

type githubFixture struct {
	user   map[string]any
	emails []ghEmail
	orgs   [][]any
	teams  [][]any
	token  http.HandlerFunc
}

func (g githubFixture) routes(f **fakeAPI) map[string]http.HandlerFunc {
	srvURL := func() string { return (*f).srv.URL }

	token := g.token
	if token == nil {
		token = tokenRoute
	}

	teams := g.teams
	if teams == nil {
		teams = [][]any{{}}
	}

	return map[string]http.HandlerFunc{
		"/login/oauth/access_token": token,
		"/user":                     bearerRoute(jsonRoute(g.user)),
		"/user/emails":              bearerRoute(jsonRoute(g.emails)),
		"/user/memberships/orgs":    bearerRoute(pagedRoute(srvURL, g.orgs)),
		"/user/teams":               bearerRoute(pagedRoute(srvURL, teams)),
	}
}

func seedGitHub(t *testing.T, db *oidcFlowDB, fx githubFixture, orgs []string, mapping map[string]string) *fakeAPI {
	t.Helper()

	var api *fakeAPI

	api = newFakeAPI(t, fx.routes(&api))

	seedFlowProvider(t, db, "gh", models.OIDCConfig{
		Type: models.OIDCTypeGitHub, AuthBaseURL: api.srv.URL, APIBaseURL: api.srv.URL, AllowedOrgs: orgs,
	}, mapping)

	return api
}

func octocat() map[string]any {
	return map[string]any{"id": 583231, "login": "octocat", "name": "The Octocat"}
}

func TestGitHub_Login_RedirectsWithPKCEAndDefaultScopes(t *testing.T) {
	db := newOIDCFlowDB()
	api := seedGitHub(t, db, githubFixture{user: octocat(), orgs: [][]any{{}}}, []string{"acme"}, nil)
	router, _ := flowRouter(t, db)

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/auth/sso/oidc/gh/login", nil))

	if rw.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d %s", rw.Code, rw.Body.String())
	}

	loc, err := url.Parse(rw.Header().Get("Location"))
	if err != nil || !strings.HasPrefix(loc.String(), api.srv.URL+"/login/oauth/authorize?") {
		t.Fatalf("unexpected authorize URL %q", rw.Header().Get("Location"))
	}

	q := loc.Query()
	if q.Get("scope") != "read:user user:email read:org" {
		t.Errorf("scope = %q", q.Get("scope"))
	}

	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" {
		t.Errorf("PKCE and state are required: %v", q)
	}

	if q.Get("nonce") != "" || cookieNamed(rw, "oa_oidc_nonce_gh") != nil {
		t.Error("an OAuth 2.0 provider has no ID token, hence no nonce")
	}

	if cookieNamed(rw, "oa_oidc_pkce_gh") == nil || cookieNamed(rw, "oa_oidc_state_gh") == nil {
		t.Error("pkce and state cookies must be set")
	}
}

func TestGitHub_Callback_Identity(t *testing.T) {
	tests := []struct {
		name      string
		emails    []ghEmail
		wantEmail string
	}{
		{
			name:      "primary and verified email is used",
			emails:    []ghEmail{{"other@example.com", false, true}, {"octo@example.com", true, true}},
			wantEmail: "octo@example.com",
		},
		{
			name:      "unverified primary is not used, even with a verified secondary",
			emails:    []ghEmail{{"octo@example.com", true, false}, {"verified@example.com", false, true}},
			wantEmail: "",
		},
		{name: "no emails at all", emails: nil, wantEmail: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newOIDCFlowDB()
			fx := githubFixture{user: octocat(), emails: tc.emails, orgs: [][]any{{ghMembership("Acme")}}}
			api := seedGitHub(t, db, fx, []string{"acme"}, nil)
			router, _ := flowRouter(t, db)

			rw := flowCallback(t, router, "gh", "good-code", callbackOpts{})
			if rw.Code != http.StatusFound {
				t.Fatalf("expected 302, got %d %s", rw.Code, rw.Body.String())
			}

			user := db.userByExternalID("583231")
			if user == nil {
				t.Fatal("the numeric GitHub id must be the external id")
			}

			if user.Username != "octocat" {
				t.Errorf("username = %q, want the GitHub login", user.Username)
			}

			gotEmail := ""
			if user.Email != nil {
				gotEmail = *user.Email
			}

			if gotEmail != tc.wantEmail {
				t.Errorf("email = %q, want %q", gotEmail, tc.wantEmail)
			}

			for _, h := range api.hitsOn("/user") {
				if h.accept != "application/vnd.github+json" || h.apiVersion != "2022-11-28" || h.auth != "Bearer "+ghToken {
					t.Errorf("unexpected API headers %+v", h)
				}
			}
		})
	}
}

func TestGitHub_Callback_OrganizationGateAndGroups(t *testing.T) {
	mapping := map[string]string{"acme": "l-acme", "acme/dev": "l-dev", "other/ops": "l-ops"}

	tests := []struct {
		name      string
		orgs      [][]any
		teams     [][]any
		wantDeny  bool
		wantNames []string
	}{
		{
			name:      "allowed org (case-insensitive) with team groups",
			orgs:      [][]any{{ghMembership("Acme"), ghMembership("other")}},
			teams:     [][]any{{ghTeam("Acme", "dev"), ghTeam("other", "ops")}},
			wantNames: []string{"l-acme", "l-dev"},
		},
		{
			name:     "member of no allowed org is denied",
			orgs:     [][]any{{ghMembership("other")}},
			teams:    [][]any{{ghTeam("other", "ops")}},
			wantDeny: true,
		},
		{
			name:     "no organizations at all is denied",
			orgs:     [][]any{{}},
			wantDeny: true,
		},
		{
			name:      "organizations are paginated through the Link header",
			orgs:      [][]any{{ghMembership("a")}, {ghMembership("b")}, {ghMembership("acme")}},
			wantNames: []string{"l-acme"},
		},
		{
			name:      "teams are paginated too",
			orgs:      [][]any{{ghMembership("acme")}},
			teams:     [][]any{{ghTeam("acme", "x")}, {ghTeam("acme", "dev")}},
			wantNames: []string{"l-acme", "l-dev"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newOIDCFlowDB()
			seedGitHub(t, db, githubFixture{user: octocat(), orgs: tc.orgs, teams: tc.teams}, []string{"ACME"}, mapping)
			router, logs := flowRouter(t, db)

			rw := flowCallback(t, router, "gh", "good-code", callbackOpts{})

			if tc.wantDeny {
				requireDenied(t, db, rw)

				if db.userByExternalID("583231") != nil {
					t.Error("a denied login must not provision a user")
				}

				if !strings.Contains(logText(logs), "allowed organizations") {
					t.Errorf("the reason belongs in the log:\n%s", logText(logs))
				}

				return
			}

			if rw.Code != http.StatusFound {
				t.Fatalf("expected 302, got %d %s", rw.Code, rw.Body.String())
			}

			names, synced := db.ssoGroupNames(db.userByExternalID("583231").ID)
			if !synced || !slices.Equal(names, tc.wantNames) {
				t.Errorf("groups synced=%v names=%v, want %v", synced, names, tc.wantNames)
			}
		})
	}
}

func TestGitHub_Callback_Failures(t *testing.T) {
	tests := []struct {
		name     string
		fixture  githubFixture
		code     string
		opts     callbackOpts
		wantCode int
	}{
		{
			name:     "token endpoint refuses the code",
			fixture:  githubFixture{user: octocat(), orgs: [][]any{{ghMembership("acme")}}},
			code:     "bad-code",
			wantCode: http.StatusBadGateway,
		},
		{
			name: "token endpoint errors",
			fixture: githubFixture{user: octocat(), orgs: [][]any{{ghMembership("acme")}}, token: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "down", http.StatusInternalServerError)
			}},
			wantCode: http.StatusBadGateway,
		},
		{
			name:     "state mismatch",
			fixture:  githubFixture{user: octocat(), orgs: [][]any{{ghMembership("acme")}}},
			opts:     callbackOpts{state: "forged-state"},
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newOIDCFlowDB()
			api := seedGitHub(t, db, tc.fixture, []string{"acme"}, nil)
			router, _ := flowRouter(t, db)

			code := tc.code
			if code == "" {
				code = "good-code"
			}

			rw := flowCallback(t, router, "gh", code, tc.opts)
			if rw.Code != tc.wantCode {
				t.Fatalf("expected %d, got %d %s", tc.wantCode, rw.Code, rw.Body.String())
			}

			if len(db.authSessions) != 0 || len(db.authUsers) != 0 {
				t.Error("a failed login must not create a session or a user")
			}

			if tc.wantCode == http.StatusBadRequest && len(api.hitsOn("/login/oauth/access_token")) != 0 {
				t.Error("the code must not be redeemed when the state does not match")
			}

			if strings.Contains(rw.Body.String(), "bad_verification_code") {
				t.Errorf("upstream error details must not reach the client: %s", rw.Body.String())
			}
		})
	}
}

func TestGitHub_Callback_OrganizationAPIFailureFailsClosed(t *testing.T) {
	db := newOIDCFlowDB()

	var api *fakeAPI

	fx := githubFixture{user: octocat(), orgs: [][]any{{ghMembership("acme")}}}
	routes := fx.routes(&api)
	routes["/user/memberships/orgs"] = func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}

	api = newFakeAPI(t, routes)
	seedFlowProvider(t, db, "gh", models.OIDCConfig{
		Type: models.OIDCTypeGitHub, AuthBaseURL: api.srv.URL, APIBaseURL: api.srv.URL, AllowedOrgs: []string{"acme"},
	}, nil)

	router, _ := flowRouter(t, db)

	rw := flowCallback(t, router, "gh", "good-code", callbackOpts{})
	if rw.Code != http.StatusBadGateway || len(db.authSessions) != 0 {
		t.Fatalf("an unreadable org list must not let the user in: %d %s", rw.Code, rw.Body.String())
	}
}

func TestOAuth2Generic_Callback(t *testing.T) {
	tests := []struct {
		name       string
		cfg        models.OIDCConfig
		userinfo   map[string]any
		wantID     string
		wantDeny   bool
		wantFail   bool
		wantSynced bool
		wantNames  []string
	}{
		{
			name:     "sub is the subject",
			userinfo: map[string]any{"sub": "abc", "id": 7, "preferred_username": "alice"},
			wantID:   "abc",
		},
		{
			name:     "numeric id is the fallback subject",
			userinfo: map[string]any{"id": 4242, "preferred_username": "bob"},
			wantID:   "4242",
		},
		{
			name:     "no subject at all fails",
			userinfo: map[string]any{"preferred_username": "carol"},
			wantFail: true,
		},
		{
			name:       "claim mapping and present groups",
			cfg:        models.OIDCConfig{UsernameClaim: "login", GroupsClaim: "roles"},
			userinfo:   map[string]any{"sub": "m1", "login": "mapped", "roles": []string{"admin", "x"}},
			wantID:     "m1",
			wantSynced: true,
			wantNames:  []string{"l-admin"},
		},
		{
			name:       "absent groups claim does not sync",
			cfg:        models.OIDCConfig{GroupsClaim: "roles"},
			userinfo:   map[string]any{"sub": "m2", "preferred_username": "nogroups"},
			wantID:     "m2",
			wantSynced: false,
		},
		{
			name:       "empty groups claim syncs an empty set",
			cfg:        models.OIDCConfig{GroupsClaim: "roles"},
			userinfo:   map[string]any{"sub": "m3", "preferred_username": "emptygroups", "roles": []string{}},
			wantID:     "m3",
			wantSynced: true,
			wantNames:  []string{},
		},
		{
			name:     "require_email_verified denies an unverified address",
			cfg:      models.OIDCConfig{RequireEmailVerified: true},
			userinfo: map[string]any{"sub": "v1", "preferred_username": "v", "email": "v@example.com"},
			wantDeny: true,
		},
		{
			name:     "require_email_verified accepts a verified address",
			cfg:      models.OIDCConfig{RequireEmailVerified: true},
			userinfo: map[string]any{"sub": "v2", "preferred_username": "v2", "email": "v2@example.com", "email_verified": true},
			wantID:   "v2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAPI(t, map[string]http.HandlerFunc{
				"/token":    tokenRoute,
				"/userinfo": bearerRoute(jsonRoute(tc.userinfo)),
			})

			db := newOIDCFlowDB()
			tc.cfg.Type = models.OIDCTypeOAuth2
			tc.cfg.AuthorizationURL = api.srv.URL + "/authorize"
			tc.cfg.TokenURL = api.srv.URL + "/token"
			tc.cfg.UserinfoURL = api.srv.URL + "/userinfo"
			seedFlowProvider(t, db, "gen", tc.cfg, map[string]string{"admin": "l-admin"})

			router, _ := flowRouter(t, db)
			rw := flowCallback(t, router, "gen", "good-code", callbackOpts{})

			switch {
			case tc.wantDeny:
				requireDenied(t, db, rw)
			case tc.wantFail:
				if rw.Code != http.StatusBadGateway || len(db.authSessions) != 0 {
					t.Fatalf("expected 502 without a session, got %d %s", rw.Code, rw.Body.String())
				}
			default:
				if rw.Code != http.StatusFound {
					t.Fatalf("expected 302, got %d %s", rw.Code, rw.Body.String())
				}

				user := db.userByExternalID(tc.wantID)
				if user == nil {
					t.Fatalf("no user with external id %q", tc.wantID)
				}

				names, synced := db.ssoGroupNames(user.ID)
				if synced != tc.wantSynced || !slices.Equal(names, tc.wantNames) {
					t.Errorf("groups synced=%v names=%v, want synced=%v names=%v", synced, names, tc.wantSynced, tc.wantNames)
				}
			}
		})
	}
}
