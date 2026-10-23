// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

func TestOIDC_Login_DefaultScopesAndHostedDomainHint(t *testing.T) {
	tests := []struct {
		name       string
		cfg        models.OIDCConfig
		wantScope  string
		wantHD     string
		wantNonce  bool
		wantNonceC bool
	}{
		{name: "defaults have no groups scope", wantScope: "openid email profile", wantNonce: true, wantNonceC: true},
		{name: "groups is opt-in through scopes", cfg: models.OIDCConfig{Scopes: []string{"openid", "email", "groups"}}, wantScope: "openid email groups", wantNonce: true, wantNonceC: true},
		{name: "hosted domain adds the hd hint", cfg: models.OIDCConfig{HostedDomain: "example.com"}, wantScope: "openid email profile", wantHD: "example.com", wantNonce: true, wantNonceC: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idp := newFlowIdP(t, "", "", false)
			db := newOIDCFlowDB()
			tc.cfg.Issuer = idp.issuer()
			seedFlowProvider(t, db, "p", tc.cfg, nil)

			router, _ := flowRouter(t, db)
			rw := httptest.NewRecorder()
			router.ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/auth/sso/oidc/p/login", nil))

			if rw.Code != http.StatusFound {
				t.Fatalf("expected 302, got %d %s", rw.Code, rw.Body.String())
			}

			loc, err := url.Parse(rw.Header().Get("Location"))
			if err != nil {
				t.Fatalf("parse location: %v", err)
			}

			q := loc.Query()
			if got := q.Get("scope"); got != tc.wantScope {
				t.Errorf("scope = %q, want %q", got, tc.wantScope)
			}

			if got := q.Get("hd"); got != tc.wantHD {
				t.Errorf("hd = %q, want %q", got, tc.wantHD)
			}

			if (q.Get("nonce") != "") != tc.wantNonce || (cookieNamed(rw, "oa_oidc_nonce_p") != nil) != tc.wantNonceC {
				t.Errorf("nonce param/cookie presence wrong: %q", loc.RawQuery)
			}
		})
	}
}

func TestOIDC_Callback_GroupsPresence(t *testing.T) {
	mapping := map[string]string{"devs": "local-devs", "ops": "local-ops"}

	tests := []struct {
		name       string
		groups     any // nil: claim absent
		wantSynced bool
		wantNames  []string
	}{
		{name: "absent claim keeps memberships (no sync)", groups: nil, wantSynced: false},
		{name: "empty list clears SSO memberships", groups: []string{}, wantSynced: true, wantNames: []string{}},
		{name: "present list syncs mapped groups only", groups: []string{"devs", "unmapped"}, wantSynced: true, wantNames: []string{"local-devs"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idp := newFlowIdP(t, "", "", false)
			db := newOIDCFlowDB()
			seedFlowProvider(t, db, "g", models.OIDCConfig{Issuer: idp.issuer()}, mapping)

			claims := map[string]any{"sub": "grp-user", "preferred_username": "grpuser", "nonce": "n1"}
			if tc.groups != nil {
				claims["groups"] = tc.groups
			}

			idp.addCode(t, "c1", claims)

			router, _ := flowRouter(t, db)

			rw := flowCallback(t, router, "g", "c1", callbackOpts{nonce: "n1"})
			if rw.Code != http.StatusFound {
				t.Fatalf("expected 302, got %d %s", rw.Code, rw.Body.String())
			}

			user := db.userByExternalID("grp-user")
			if user == nil {
				t.Fatal("user not provisioned")
			}

			names, synced := db.ssoGroupNames(user.ID)
			if synced != tc.wantSynced || !slices.Equal(names, tc.wantNames) {
				t.Errorf("synced=%v names=%v, want synced=%v names=%v", synced, names, tc.wantSynced, tc.wantNames)
			}
		})
	}
}

// fakeGraph is a Microsoft Graph stand-in for POST /v1.0/me/getMemberObjects.
type fakeGraph struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []graphRequest
	// handler answers page n (1-based).
	handler func(w http.ResponseWriter, page int, self string)
}

type graphRequest struct {
	method, path, auth, body string
}

func newFakeGraph(t *testing.T, handler func(w http.ResponseWriter, page int, self string)) *fakeGraph {
	t.Helper()

	g := &fakeGraph{handler: handler}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		g.mu.Lock()
		g.requests = append(g.requests, graphRequest{r.Method, r.URL.Path, r.Header.Get("Authorization"), string(body)})
		page := len(g.requests)
		g.mu.Unlock()

		g.handler(w, page, g.srv.URL)
	}))
	t.Cleanup(g.srv.Close)

	return g
}

func (g *fakeGraph) recorded() []graphRequest {
	g.mu.Lock()
	defer g.mu.Unlock()

	return slices.Clone(g.requests)
}

func graphPage(w http.ResponseWriter, ids []string, next string) {
	out := map[string]any{"value": ids}
	if next != "" {
		out["@odata.nextLink"] = next
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func TestOIDC_Callback_EntraGroupsOverage(t *testing.T) {
	mapping := map[string]string{"gid-1": "local-a", "gid-2": "local-b"}

	overageNames := map[string]any{"groups": "src1"}
	sources := map[string]any{"src1": map[string]any{"endpoint": "https://graph.windows.net/t/users/u/getMemberObjects"}}

	tests := []struct {
		name       string
		claims     map[string]any
		graphOn    bool
		graph      func(w http.ResponseWriter, page int, self string)
		wantSynced bool
		wantNames  []string
		wantGraph  int
		wantWarn   string
	}{
		{
			name:   "overage without graph_groups keeps memberships",
			claims: map[string]any{"_claim_names": overageNames, "_claim_sources": sources},
			graph:  func(w http.ResponseWriter, _ int, _ string) { graphPage(w, []string{"gid-1"}, "") },
			// graph must not be contacted
			wantSynced: false, wantGraph: 0, wantWarn: "overage",
		},
		{
			name:    "overage with graph_groups resolves paginated ids",
			claims:  map[string]any{"_claim_names": overageNames, "_claim_sources": sources},
			graphOn: true,
			graph: func(w http.ResponseWriter, page int, self string) {
				if page == 1 {
					graphPage(w, []string{"gid-1", "other"}, self+"/v1.0/me/getMemberObjects?$skiptoken=2")
					return
				}

				graphPage(w, []string{"gid-2"}, "")
			},
			wantSynced: true, wantNames: []string{"local-a", "local-b"}, wantGraph: 2,
		},
		{
			name:       "hasgroups=true is an overage too",
			claims:     map[string]any{"hasgroups": true},
			graphOn:    true,
			graph:      func(w http.ResponseWriter, _ int, _ string) { graphPage(w, []string{"gid-2"}, "") },
			wantSynced: true, wantNames: []string{"local-b"}, wantGraph: 1,
		},
		{
			name:    "graph failure never fails the login and keeps memberships",
			claims:  map[string]any{"_claim_names": overageNames, "_claim_sources": sources},
			graphOn: true,
			graph:   func(w http.ResponseWriter, _ int, _ string) { http.Error(w, "boom", http.StatusInternalServerError) },
			// one attempt, no retry
			wantSynced: false, wantGraph: 1, wantWarn: "Graph failed",
		},
		{
			name:    "graph forbidden (missing consent) keeps memberships",
			claims:  map[string]any{"_claim_names": overageNames, "_claim_sources": sources},
			graphOn: true,
			graph:   func(w http.ResponseWriter, _ int, _ string) { http.Error(w, "denied", http.StatusForbidden) },
			// one attempt, no retry
			wantSynced: false, wantGraph: 1, wantWarn: "Graph failed",
		},
		{
			name:    "a nextLink on another host is never followed",
			claims:  map[string]any{"_claim_names": overageNames, "_claim_sources": sources},
			graphOn: true,
			graph: func(w http.ResponseWriter, _ int, _ string) {
				graphPage(w, []string{"gid-1"}, "http://attacker.invalid/v1.0/me/getMemberObjects")
			},
			wantSynced: false, wantGraph: 1, wantWarn: "Graph failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			graph := newFakeGraph(t, tc.graph)
			idp := newFlowIdP(t, "", "", false)
			db := newOIDCFlowDB()

			cfg := models.OIDCConfig{Issuer: idp.issuer(), GraphGroups: tc.graphOn, GraphEndpoint: graph.srv.URL}
			seedFlowProvider(t, db, "e", cfg, mapping)

			tc.claims["sub"] = "entra-user"
			tc.claims["preferred_username"] = "entrauser"
			tc.claims["nonce"] = "n1"
			idp.addCode(t, "c1", tc.claims)

			router, logs := flowRouter(t, db)

			rw := flowCallback(t, router, "e", "c1", callbackOpts{nonce: "n1"})
			if rw.Code != http.StatusFound {
				t.Fatalf("login must succeed, got %d %s", rw.Code, rw.Body.String())
			}

			user := db.userByExternalID("entra-user")
			if user == nil {
				t.Fatal("user not provisioned")
			}

			names, synced := db.ssoGroupNames(user.ID)
			if synced != tc.wantSynced || !slices.Equal(names, tc.wantNames) {
				t.Errorf("synced=%v names=%v, want synced=%v names=%v", synced, names, tc.wantSynced, tc.wantNames)
			}

			reqs := graph.recorded()
			if len(reqs) != tc.wantGraph {
				t.Fatalf("graph requests = %d, want %d", len(reqs), tc.wantGraph)
			}

			for _, r := range reqs {
				if r.method != http.MethodPost || r.path != "/v1.0/me/getMemberObjects" ||
					r.auth != "Bearer fake-access-token" || !strings.Contains(r.body, `"securityEnabledOnly":true`) {
					t.Errorf("unexpected graph request %+v", r)
				}
			}

			text := logText(logs)
			if tc.wantWarn != "" && !strings.Contains(text, tc.wantWarn) {
				t.Errorf("expected a warning mentioning %q, logs:\n%s", tc.wantWarn, text)
			}

			if strings.Contains(text, "fake-access-token") {
				t.Errorf("the access token must never be logged:\n%s", text)
			}
		})
	}
}

func TestOIDC_Callback_LoginPolicy(t *testing.T) {
	tests := []struct {
		name   string
		cfg    models.OIDCConfig
		claims map[string]any
		deny   bool
	}{
		{name: "verified email passes", cfg: models.OIDCConfig{RequireEmailVerified: true}, claims: map[string]any{"email": "a@example.com", "email_verified": true}},
		{name: "unverified email rejected", cfg: models.OIDCConfig{RequireEmailVerified: true}, claims: map[string]any{"email": "a@example.com", "email_verified": false}, deny: true},
		{name: "missing email_verified rejected", cfg: models.OIDCConfig{RequireEmailVerified: true}, claims: map[string]any{"email": "a@example.com"}, deny: true},
		{name: "unverified email fine when not required", claims: map[string]any{"email": "a@example.com", "email_verified": false}},
		{name: "hd matches case-insensitively", cfg: models.OIDCConfig{HostedDomain: "Example.COM"}, claims: map[string]any{"hd": "example.com"}},
		{name: "hd mismatch rejected", cfg: models.OIDCConfig{HostedDomain: "example.com"}, claims: map[string]any{"hd": "evil.example"}, deny: true},
		{name: "missing hd rejected", cfg: models.OIDCConfig{HostedDomain: "example.com"}, claims: map[string]any{"email": "a@example.com", "email_verified": true}, deny: true},
		{name: "hd suffix is not a match", cfg: models.OIDCConfig{HostedDomain: "example.com"}, claims: map[string]any{"hd": "example.com.evil.example"}, deny: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idp := newFlowIdP(t, "", "", false)
			db := newOIDCFlowDB()
			tc.cfg.Issuer = idp.issuer()
			seedFlowProvider(t, db, "pol", tc.cfg, nil)

			tc.claims["sub"] = "pol-user"
			tc.claims["preferred_username"] = "poluser"
			tc.claims["nonce"] = "n1"
			idp.addCode(t, "c1", tc.claims)

			router, logs := flowRouter(t, db)
			rw := flowCallback(t, router, "pol", "c1", callbackOpts{nonce: "n1"})

			if !tc.deny {
				if rw.Code != http.StatusFound {
					t.Fatalf("expected 302, got %d %s", rw.Code, rw.Body.String())
				}

				return
			}

			requireDenied(t, db, rw)

			if db.userByExternalID("pol-user") != nil {
				t.Error("a denied login must not provision a user")
			}

			if strings.Contains(rw.Body.String(), "hosted_domain") || strings.Contains(rw.Body.String(), "email_verified") {
				t.Errorf("the response must not reveal the rejection reason: %s", rw.Body.String())
			}

			if !strings.Contains(logText(logs), "sso login denied") {
				t.Errorf("the reason must be logged, logs:\n%s", logText(logs))
			}
		})
	}
}

func TestOIDC_Callback_EntraMultiTenant(t *testing.T) {
	const (
		tenantA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		tenantB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)

	tests := []struct {
		name   string
		claims func(srv string) map[string]any
		deny   bool
	}{
		{name: "allowed tenant", claims: func(srv string) map[string]any {
			return map[string]any{"tid": tenantA, "iss": srv + "/" + tenantA + "/v2.0"}
		}},
		{name: "tid is not a guid", deny: true, claims: func(srv string) map[string]any {
			return map[string]any{"tid": "not-a-guid", "iss": srv + "/not-a-guid/v2.0"}
		}},
		{name: "missing tid", deny: true, claims: func(srv string) map[string]any {
			return map[string]any{"iss": srv + "/" + tenantA + "/v2.0"}
		}},
		{name: "tenant not allowed", deny: true, claims: func(srv string) map[string]any {
			return map[string]any{"tid": tenantB, "iss": srv + "/" + tenantB + "/v2.0"}
		}},
		{name: "iss names another tenant than tid", deny: true, claims: func(srv string) map[string]any {
			return map[string]any{"tid": tenantA, "iss": srv + "/" + tenantB + "/v2.0"}
		}},
		{name: "iss keeps the placeholder", deny: true, claims: func(srv string) map[string]any {
			return map[string]any{"tid": tenantA, "iss": srv + "/{tenantid}/v2.0"}
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idp := newFlowIdP(t, "/common/v2.0", "%s/{tenantid}/v2.0", false)
			db := newOIDCFlowDB()
			// Allowed tenants are matched case-insensitively.
			seedFlowProvider(t, db, "ent", models.OIDCConfig{
				Issuer: idp.issuer(), AllowedTenants: []string{strings.ToUpper(tenantA)},
			}, nil)

			claims := tc.claims(idp.srv.URL)
			claims["sub"] = "entra-sub"
			claims["preferred_username"] = "entrasub"
			claims["nonce"] = "n1"
			idp.addCode(t, "c1", claims)

			router, _ := flowRouter(t, db)
			rw := flowCallback(t, router, "ent", "c1", callbackOpts{nonce: "n1"})

			if tc.deny {
				if rw.Code == http.StatusFound {
					t.Fatalf("login must be denied, got 302")
				}

				if len(db.authSessions) != 0 || len(db.authUsers) != 0 {
					t.Errorf("a denied login must neither create a session nor a user")
				}

				return
			}

			if rw.Code != http.StatusFound {
				t.Fatalf("expected 302, got %d %s", rw.Code, rw.Body.String())
			}

			if db.userByExternalID(tenantA+":entra-sub") == nil {
				t.Errorf("the external id must be namespaced by tenant (%s:sub)", tenantA)
			}
		})
	}
}

func TestOIDC_Callback_EntraMultiTenantRequiresAllowedTenants(t *testing.T) {
	idp := newFlowIdP(t, "/common/v2.0", "%s/{tenantid}/v2.0", false)
	db := newOIDCFlowDB()
	seedFlowProvider(t, db, "ent", models.OIDCConfig{Issuer: idp.issuer()}, nil)

	idp.addCode(t, "c1", map[string]any{"tid": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "nonce": "n1"})

	router, _ := flowRouter(t, db)
	rw := flowCallback(t, router, "ent", "c1", callbackOpts{nonce: "n1"})

	if rw.Code == http.StatusFound || len(db.authSessions) != 0 {
		t.Fatalf("a multi-tenant provider without allowed_tenants must fail closed, got %d", rw.Code)
	}
}

func TestOIDC_Callback_RotatesSessionAndKeepsIDTokenForLogout(t *testing.T) {
	idp := newFlowIdP(t, "", "", true)
	db := newOIDCFlowDB()
	seedFlowProvider(t, db, "rot", models.OIDCConfig{Issuer: idp.issuer()}, nil)

	ctx := context.Background()

	oldCookie, _, err := auth.CreateSession(ctx, db, "planted-user", "127.0.0.1", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	raw := idp.addCode(t, "c1", map[string]any{"sub": "rot-user", "preferred_username": "rotuser", "nonce": "n1"})

	router, _ := flowRouter(t, db)

	rw := flowCallback(t, router, "rot", "c1", callbackOpts{nonce: "n1", sessionCk: oldCookie})
	if rw.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d %s", rw.Code, rw.Body.String())
	}

	if s, _ := auth.ValidateSession(ctx, db, oldCookie); s != nil {
		t.Error("the pre-login session must be revoked by the login")
	}

	newCookie := cookieNamed(rw, auth.SessionCookieName)
	if newCookie == nil || newCookie.Value == "" || newCookie.Value == oldCookie {
		t.Fatalf("a new, different session cookie must be issued, got %+v", newCookie)
	}

	if csrf := cookieNamed(rw, "oa_csrf"); csrf == nil || csrf.Value == "" {
		t.Error("a fresh CSRF token must be issued")
	}

	if s, err := auth.ValidateSession(ctx, db, newCookie.Value); err != nil || s == nil {
		t.Fatalf("the new session must be valid: %v", err)
	}

	info, err := auth.LookupSessionSSO(ctx, db, newCookie.Value)
	if err != nil || info == nil {
		t.Fatalf("session must carry its SSO record: %v %v", info, err)
	}

	if info.ProviderSlug != "rot" || info.IDToken != raw {
		t.Errorf("stored SSO record = %+v, want slug rot and the raw ID token", info)
	}
}

func TestOIDC_Callback_EmailAlreadyInUseIsGeneric(t *testing.T) {
	idp := newFlowIdP(t, "", "", false)
	db := newOIDCFlowDB()
	seedFlowProvider(t, db, "dup", models.OIDCConfig{Issuer: idp.issuer()}, nil)

	email := "owner@example.com"
	if _, err := db.CreateUser(context.Background(), &models.User{
		ID: "local-owner", Username: "owner", Email: &email, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	idp.addCode(t, "c1", map[string]any{
		"sub": "dup-user", "preferred_username": "dupuser", "nonce": "n1",
		"email": email, "email_verified": true,
	})

	router, logs := flowRouter(t, db)
	rw := flowCallback(t, router, "dup", "c1", callbackOpts{nonce: "n1"})

	requireDenied(t, db, rw)

	body := strings.ToLower(rw.Body.String())
	for _, leak := range []string{"owner@example.com", "already", "in use", "email"} {
		if strings.Contains(body, leak) {
			t.Errorf("response reveals %q: %s", leak, rw.Body.String())
		}
	}

	if !strings.Contains(logText(logs), "email already in use") {
		t.Errorf("the cause belongs in the server log:\n%s", logText(logs))
	}
}
