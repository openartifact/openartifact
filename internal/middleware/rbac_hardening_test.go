// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
)

func TestHasAmbiguousPath(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"plain repo path", "/repos/myrepo/lodash", false},
		{"npm scoped package keeps its encoded slash", "/repos/npm-repo/@scope%2Fname", false},
		{"docker namespaced name", "/v2/org/team/img/manifests/latest", false},
		{"dot-dot segment", "/repos/public/../private/x", true},
		{"encoded dot-dot segment", "/repos/public/%2e%2e/private/x", true},
		{"encoded slash in repo segment", "/repos/public%2F..%2Fprivate/x", true},
		{"encoded slash hiding in repo name", "/repos/a%2Fb/x", true},
		{"encoded backslash in repo segment", "/repos/a%5Cb/x", true},
		{"encoded slash in isolated namespace repo", "/repos/raw/a%2fb/x", true},
		{"encoded slash anywhere in OCI path", "/v2/a%2Fb/manifests/latest", true},
		{"dot-dot in api path", "/api/v1/../v2/x", true},
		{"mixed-case encoded dot-dot", "/repos/public/%2E%2e/private/x", true},
		{"dot-dot behind an encoded backslash", "/repos/public/a%5C..%5Cb", true},
		{"dot-dot before an encoded slash", "/repos/public/..%2fprivate/x", true},
		{"dot-dot lookalike names are plain", "/repos/public/..foo/bar..", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.raw, nil)
			if got := hasAmbiguousPath(req.URL); got != tc.want {
				t.Errorf("hasAmbiguousPath(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestClassifyAPIRoute(t *testing.T) {
	tests := []struct {
		method   string
		path     string
		want     routeScope
		describe string
	}{
		{http.MethodGet, "/api/v1/repositories", routeScope{action: "manage", anyAuthenticated: true}, "list filters per repo itself; anonymous never passes"},
		{http.MethodPost, "/api/v1/repositories", routeScope{action: "manage"}, "creating a repo is administrative"},
		{http.MethodGet, "/api/v1/repository/app", routeScope{action: "read", repoName: "app"}, "get repo"},
		{http.MethodPatch, "/api/v1/repository/app", routeScope{action: "manage", delegateRepo: "app"}, "update repo: global manage or the repo's project maintainers"},
		{http.MethodDelete, "/api/v1/repository/app", routeScope{action: "manage", delegateRepo: "app"}, "delete repo: global manage or the repo's project maintainers"},
		{http.MethodGet, "/api/v1/repositories/app/packages", routeScope{action: "read", repoName: "app"}, "package list"},
		{http.MethodGet, "/api/v1/repositories/app/packages/123/download", routeScope{action: "read", repoName: "app"}, "download"},
		{http.MethodPost, "/api/v1/repositories/app/packages", routeScope{action: "write", repoName: "app"}, "rpm upload"},
		{http.MethodPost, "/api/v1/repositories/app/debs", routeScope{action: "write", repoName: "app"}, "deb upload"},
		{http.MethodPut, "/api/v1/repositories/app/comps", routeScope{action: "write", repoName: "app"}, "comps upload"},
		{http.MethodPost, "/api/v1/repositories/app/uploads", routeScope{action: "write", repoName: "app"}, "presigned upload init"},
		{http.MethodPost, "/api/v1/repositories/app/uploads/0b9c1a52-0000-4000-8000-000000000000/complete", routeScope{action: "write", repoName: "app"}, "presigned upload complete"},
		{http.MethodDelete, "/api/v1/repositories/app/debs/9", routeScope{action: "delete", repoName: "app"}, "deb delete"},
		{http.MethodDelete, "/api/v1/repositories/app/packages/9", routeScope{action: "delete", repoName: "app"}, "rpm delete"},
		{http.MethodPatch, "/api/v1/repositories/app/packages/9", routeScope{action: "manage"}, "unknown method on data route"},
		{http.MethodPut, "/api/v1/repositories/app/members", routeScope{action: "manage"}, "members stay administrative"},
		{http.MethodPost, "/api/v1/repositories/app/webhooks", routeScope{action: "manage"}, "webhooks stay administrative"},
		{http.MethodGet, "/api/v1/repositories/app/trash", routeScope{action: "delete", repoName: "app"}, "repo trash list needs repo delete"},
		{http.MethodPost, "/api/v1/repositories/app/trash/maven_artifact/0b1c2d3e-0000-4000-8000-000000000001/restore", routeScope{action: "delete", repoName: "app"}, "repo trash restore needs repo delete"},
		{http.MethodDelete, "/api/v1/repositories/app/trash/maven_artifact/0b1c2d3e-0000-4000-8000-000000000001", routeScope{action: "manage"}, "purge is never repo scoped"},
		{http.MethodDelete, "/api/v1/repositories/app/trash", routeScope{action: "manage", repoName: "app", delegateRepo: "app"}, "empty trash needs repo manage or project maintainer"},
		{http.MethodGet, "/api/v1/repositories/app/cleanup-policies", routeScope{action: "manage", delegateRepo: "app"}, "cleanup list is delegated"},
		{http.MethodPost, "/api/v1/repositories/app/cleanup-policies/7/run", routeScope{action: "manage", delegateRepo: "app"}, "cleanup run is delegated"},
		{http.MethodPut, "/api/v1/repositories/app/quota", routeScope{action: "manage", delegateRepo: "app"}, "quota write is delegated"},
		{http.MethodDelete, "/api/v1/repositories/app/quota", routeScope{action: "manage", delegateRepo: "app"}, "quota reset is delegated"},
		{http.MethodPost, "/api/v1/repositories/app/quota/reconcile", routeScope{action: "manage"}, "quota reconcile stays administrative"},
		{http.MethodPatch, "/api/v1/repository/a%2Fb", routeScope{action: "manage"}, "escaped name never delegated"},
		{http.MethodPost, "/api/v1/repositories/app/webhooks", routeScope{action: "manage"}, "webhooks stay administrative"},
		{http.MethodPost, "/api/v1/projects/p/repositories", routeScope{action: "manage", project: "p", projectAction: auth.ProjectActionRepoCreate}, "repo create in project"},
		{http.MethodPost, "/api/v1/projects/p/repositories/app/transfer", routeScope{action: "manage", project: "p", projectAction: auth.ProjectActionRepoTransfer}, "transfer needs owner"},
		{http.MethodPost, "/api/v1/projects/p/repositories/app/other", routeScope{action: "manage"}, "unknown project sub-route is manage"},
		{http.MethodDelete, "/api/v1/repositories/a%2Fb/trash", routeScope{action: "manage"}, "escaped repo name on empty trash falls back to global manage"},
		{http.MethodPost, "/api/v1/repositories/app/trash", routeScope{action: "manage"}, "unknown method on trash list"},
		{http.MethodPost, "/api/v1/repositories/app/trash/maven_artifact/restore", routeScope{action: "manage"}, "odd trash shape"},
		{http.MethodGet, "/api/v1/repositories/a%2Fb/trash", routeScope{action: "manage"}, "escaped repo name falls back to manage"},
		{http.MethodGet, "/api/v1/trash", routeScope{action: "manage"}, "global trash is administrative"},
		{http.MethodGet, "/api/v1/vuln/scans", routeScope{action: "manage"}, "the cross-repository scan overview is administrative"},
		{http.MethodGet, "/api/v1/vuln/scans/summary", routeScope{action: "manage"}, "the overview summary is administrative"},
		{http.MethodPost, "/api/v1/trash/repository/0b1c2d3e-0000-4000-8000-000000000001/restore", routeScope{action: "manage"}, "global restore is administrative"},
		{http.MethodDelete, "/api/v1/trash/repository/0b1c2d3e-0000-4000-8000-000000000001", routeScope{action: "manage"}, "global purge is administrative"},
		{http.MethodGet, "/api/v1/repositories/app/artifacts/a1/properties", routeScope{action: "read", repoName: "app"}, "read properties"},
		{http.MethodPut, "/api/v1/repositories/app/artifacts/a1/properties", routeScope{action: "write", repoName: "app"}, "replace properties"},
		{http.MethodPatch, "/api/v1/repositories/app/artifacts/a1/properties", routeScope{action: "write", repoName: "app"}, "patch properties"},
		{http.MethodDelete, "/api/v1/repositories/app/artifacts/a1/properties/build.name", routeScope{action: "write", repoName: "app"}, "removing a property is a write, not a package delete"},
		{http.MethodGet, "/api/v1/repositories/app/artifacts/search", routeScope{action: "read", repoName: "app"}, "property search"},
		{http.MethodPost, "/api/v1/repositories/app/artifacts/search", routeScope{action: "manage"}, "unexpected method on search"},
		{http.MethodDelete, "/api/v1/repositories/app/artifacts/a1/properties", routeScope{action: "manage"}, "no delete of the whole map"},
		{http.MethodGet, "/api/v1/repositories/a%2Fb/artifacts/a1/properties", routeScope{action: "manage"}, "escaped repo name falls back to manage"},
		{http.MethodPost, "/api/v1/bundles", routeScope{action: "manage", bundles: true}, "create bundle: handler authorizes"},
		{http.MethodGet, "/api/v1/bundles", routeScope{action: "manage"}, "bundle list stays administrative"},
		{http.MethodGet, "/api/v1/bundles/shop/1", routeScope{action: "manage", bundles: true}, "get bundle"},
		{http.MethodDelete, "/api/v1/bundles/shop/1", routeScope{action: "manage", bundles: true}, "delete bundle"},
		{http.MethodPost, "/api/v1/bundles/shop/1/items", routeScope{action: "manage", bundles: true}, "add items"},
		{http.MethodDelete, "/api/v1/bundles/shop/1/items/abc", routeScope{action: "manage", bundles: true}, "remove item"},
		{http.MethodPost, "/api/v1/bundles/shop/1/seal", routeScope{action: "manage", bundles: true}, "seal"},
		{http.MethodPost, "/api/v1/bundles/shop/1/promote", routeScope{action: "manage", bundles: true}, "promote"},
		{http.MethodPost, "/api/v1/bundles/shop/1/archive", routeScope{action: "manage", bundles: true}, "archive"},
		{http.MethodGet, "/api/v1/bundles/shop/1/manifest.asc", routeScope{action: "manage", bundles: true}, "signature"},
		{http.MethodGet, "/api/v1/bundles/shop/1/promotions", routeScope{action: "manage", bundles: true}, "promotion history"},
		{http.MethodPut, "/api/v1/bundles/shop/1", routeScope{action: "manage"}, "unexpected method"},
		{http.MethodGet, "/api/v1/bundles/shop/1/seal", routeScope{action: "manage"}, "GET on seal"},
		{http.MethodGet, "/api/v1/bundles/shop/1/other", routeScope{action: "manage"}, "unknown sub-route"},
		{http.MethodGet, "/api/v1/pipelines", routeScope{action: "manage"}, "pipelines stay administrative"},
		{http.MethodPost, "/api/v1/pipelines", routeScope{action: "manage"}, "pipelines stay administrative"},
		{http.MethodGet, "/api/v1/pipelines/p", routeScope{action: "manage"}, "pipelines stay administrative"},
		{http.MethodGet, "/api/v1/admin/users", routeScope{action: "manage"}, "admin api"},
		{http.MethodGet, "/api/v1/admin/declarative", routeScope{action: "manage"}, "declarative markers are administrative"},
		{http.MethodPut, "/api/v1/admin/declarative", routeScope{action: "manage"}, "declarative markers are administrative"},
		{http.MethodDelete, "/api/v1/admin/declarative", routeScope{action: "manage"}, "declarative markers are administrative"},
		{http.MethodGet, "/api/v1/projects", routeScope{action: "manage", projectsRoot: true}, "list projects: any authenticated caller, handler filters"},
		{http.MethodPost, "/api/v1/projects", routeScope{action: "manage", projectsRoot: true}, "create project: admin or self_create"},
		{http.MethodPut, "/api/v1/projects", routeScope{action: "manage"}, "unexpected method on the collection"},
		{http.MethodGet, "/api/v1/projects/acme", routeScope{action: "manage", project: "acme", projectAction: auth.ProjectActionRead}, "get project"},
		{http.MethodPatch, "/api/v1/projects/acme", routeScope{action: "manage", project: "acme", projectAction: auth.ProjectActionUpdate}, "update project"},
		{http.MethodPut, "/api/v1/projects/acme", routeScope{action: "manage"}, "PUT is not an update verb"},
		{http.MethodDelete, "/api/v1/projects/acme", routeScope{action: "manage", project: "acme", projectAction: auth.ProjectActionDelete}, "delete project"},
		{http.MethodGet, "/api/v1/projects/acme/repositories", routeScope{action: "manage", project: "acme", projectAction: auth.ProjectActionRead}, "project repositories"},
		{http.MethodGet, "/api/v1/projects/acme/members", routeScope{action: "manage", project: "acme", projectAction: auth.ProjectActionRead}, "list project members"},
		{http.MethodPost, "/api/v1/projects/acme/members", routeScope{action: "manage", project: "acme", projectAction: auth.ProjectActionMembersManage}, "add project member"},
		{http.MethodPatch, "/api/v1/projects/acme/members/u1", routeScope{action: "manage", project: "acme", projectAction: auth.ProjectActionMembersManage}, "change project member role"},
		{http.MethodPut, "/api/v1/projects/acme/members/u1", routeScope{action: "manage"}, "PUT is not a member role verb"},
		{http.MethodDelete, "/api/v1/projects/acme/members/u1", routeScope{action: "manage", project: "acme", projectAction: auth.ProjectActionMembersManage}, "remove project member"},
		{http.MethodGet, "/api/v1/projects/acme/members/u1", routeScope{action: "manage"}, "no GET on a single member"},
		{http.MethodGet, "/api/v1/projects/acme/groups", routeScope{action: "manage", project: "acme", projectAction: auth.ProjectActionRead}, "list project groups"},
		{http.MethodPut, "/api/v1/projects/acme/groups/g1", routeScope{action: "manage", project: "acme", projectAction: auth.ProjectActionMembersManage}, "attach project group"},
		{http.MethodDelete, "/api/v1/projects/acme/groups/g1", routeScope{action: "manage", project: "acme", projectAction: auth.ProjectActionMembersManage}, "detach project group"},
		{http.MethodPatch, "/api/v1/projects/acme/groups/g1", routeScope{action: "manage"}, "PATCH is not a group verb"},
		{http.MethodGet, "/api/v1/projects/acme/repositories/x", routeScope{action: "manage"}, "unknown sub-resource falls to global manage"},
		{http.MethodGet, "/api/v1/projects/acme/tokens", routeScope{action: "manage", project: "acme", projectAction: auth.ProjectActionTokens}, "list project tokens"},
		{http.MethodPost, "/api/v1/projects/acme/tokens", routeScope{action: "manage", project: "acme", projectAction: auth.ProjectActionTokens}, "create project token"},
		{http.MethodDelete, "/api/v1/projects/acme/tokens/t1", routeScope{action: "manage", project: "acme", projectAction: auth.ProjectActionTokens}, "revoke project token"},
		{http.MethodPut, "/api/v1/projects/acme/tokens", routeScope{action: "manage"}, "PUT is not a token verb"},
		{http.MethodGet, "/api/v1/projects/acme/tokens/t1", routeScope{action: "manage"}, "no GET on a single token"},
		{http.MethodGet, "/api/v1/projects/acme/members/a%2Fb", routeScope{action: "manage"}, "escaped segment falls to global manage"},
		{http.MethodGet, "/api/v1/projects/a%2Fb", routeScope{action: "manage"}, "escaped project name falls to global manage"},
		{http.MethodGet, "/api/v1/projects/acme/groups/a/b", routeScope{action: "manage"}, "extra segment falls to global manage"},
		{http.MethodGet, "/api/v1/admin/projects/settings", routeScope{action: "manage"}, "project settings are admin api"},
		{http.MethodGet, "/api/v1/repositories/a%2Fb/packages", routeScope{action: "manage"}, "escaped name falls back to manage"},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)

			if got := classifyRoute(req); got != tc.want {
				t.Errorf("classifyRoute(%s %s) = %+v, want %+v (%s)", tc.method, tc.path, got, tc.want, tc.describe)
			}
		})
	}
}

func TestIsSelfServiceRequest(t *testing.T) {
	uid := "user-1"
	session := &auth.Principal{UserID: &uid, Username: "u", Credential: auth.CredentialSession}
	bearer := &auth.Principal{UserID: &uid, Username: "u", Credential: auth.CredentialBearerToken, TokenKind: auth.TokenKindAccess}
	legacy := &auth.Principal{Username: "system/legacy-api-key", IsLegacy: true, Credential: auth.CredentialLegacy}
	anon := &auth.Principal{IsAnonymous: true, Credential: auth.CredentialNone}

	tests := []struct {
		name   string
		p      *auth.Principal
		method string
		path   string
		want   bool
	}{
		{"session reads own profile", session, http.MethodGet, "/api/v1/auth/me", true},
		{"token reads own identity", bearer, http.MethodGet, "/api/v1/auth/me", true},
		{"anonymous cannot", anon, http.MethodGet, "/api/v1/auth/me", false},
		{"session changes own password", session, http.MethodPost, "/api/v1/auth/change-password", true},
		{"token must not change password", bearer, http.MethodPost, "/api/v1/auth/change-password", false},
		{"legacy key must not change password", legacy, http.MethodPost, "/api/v1/auth/change-password", false},
		{"session logs out", session, http.MethodPost, "/api/v1/auth/logout", true},
		{"session lists own tokens", session, http.MethodGet, "/api/v1/auth/tokens", true},
		{"session creates own token", session, http.MethodPost, "/api/v1/auth/tokens", true},
		{"token must not mint tokens", bearer, http.MethodPost, "/api/v1/auth/tokens", false},
		{"token must not list tokens", bearer, http.MethodGet, "/api/v1/auth/tokens", false},
		{"session revokes own token", session, http.MethodDelete, "/api/v1/auth/tokens/abc", true},
		{"session patch own user", session, http.MethodPatch, "/api/v1/users/user-1", true},
		{"session patch other user", session, http.MethodPatch, "/api/v1/users/user-2", false},
		{"session delete own user is not self-service", session, http.MethodDelete, "/api/v1/users/user-1", false},
		{"session creates token for self", session, http.MethodPost, "/api/v1/users/user-1/tokens", true},
		{"session creates token for other user", session, http.MethodPost, "/api/v1/users/user-2/tokens", false},
		{"session revokes own user token", session, http.MethodDelete, "/api/v1/users/user-1/tokens/t1", true},
		{"session revokes other user token", session, http.MethodDelete, "/api/v1/users/user-2/tokens/t1", false},
		{"token must not mint via users route", bearer, http.MethodPost, "/api/v1/users/user-1/tokens", false},
		{"token must not patch user", bearer, http.MethodPatch, "/api/v1/users/user-1", false},
		{"listing users is never self-service", session, http.MethodGet, "/api/v1/users", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
			if got := isSelfServiceRequest(req, tc.p); got != tc.want {
				t.Errorf("isSelfServiceRequest(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

func TestAuthorizationSecrets(t *testing.T) {
	basic := func(user, pass string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	}

	tests := []struct {
		name       string
		header     string
		wantBearer string
		wantBasic  string
	}{
		{"none", "", "", ""},
		{"bearer", "Bearer oa_pat_abc", "oa_pat_abc", ""},
		{"basic password is the token", basic("ci", "oa_dep_xyz"), "", "oa_dep_xyz"},
		{"basic with empty user", basic("", "oa_pat_abc"), "", "oa_pat_abc"},
		{"basic with malformed payload", "Basic !!!notbase64", "", ""},
		{"unknown scheme", "Digest abc", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/repos/x/y", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}

			bearer, basicSecret := authorizationSecrets(req)
			if bearer != tc.wantBearer || basicSecret != tc.wantBasic {
				t.Errorf("authorizationSecrets = (%q, %q), want (%q, %q)", bearer, basicSecret, tc.wantBearer, tc.wantBasic)
			}
		})
	}
}

func TestExtractLegacyKey(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/repos/x/y", nil)
	req.Header.Set("X-Api-Key", "hdr")

	if got := extractLegacyKey(req, "bearer", "basic"); got != "hdr" {
		t.Errorf("X-Api-Key must win, got %q", got)
	}

	req.Header.Del("X-Api-Key")

	if got := extractLegacyKey(req, "bearer", "basic"); got != "bearer" {
		t.Errorf("bearer must win over basic, got %q", got)
	}

	if got := extractLegacyKey(req, "", "basic"); got != "basic" {
		t.Errorf("basic password fallback, got %q", got)
	}
}

func TestCORSMiddleware(t *testing.T) {
	handler := func(origins ...string) http.Handler {
		return CORSMiddleware(origins)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	}

	do := func(h http.Handler, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/auth/me", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}

		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		return rr
	}

	t.Run("explicit origin gets credentials", func(t *testing.T) {
		rr := do(handler("https://ui.example.com"), "https://ui.example.com")
		if rr.Header().Get("Access-Control-Allow-Origin") != "https://ui.example.com" ||
			rr.Header().Get("Access-Control-Allow-Credentials") != "true" {
			t.Errorf("explicit origin must be echoed with credentials, got %v", rr.Header())
		}

		if !strings.Contains(rr.Header().Get("Access-Control-Allow-Headers"), "X-Csrf-Token") {
			t.Errorf("X-Csrf-Token must be an allowed header, got %q", rr.Header().Get("Access-Control-Allow-Headers"))
		}
	})

	t.Run("unlisted origin gets nothing", func(t *testing.T) {
		rr := do(handler("https://ui.example.com"), "https://evil.example")
		if rr.Header().Get("Access-Control-Allow-Origin") != "" || rr.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("unlisted origin must get no CORS headers, got %v", rr.Header())
		}
	})

	t.Run("wildcard never reflects an origin with credentials", func(t *testing.T) {
		rr := do(handler("*"), "https://evil.example")
		if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("wildcard must answer a literal *, got %q", got)
		}

		if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "" {
			t.Errorf("wildcard must not allow credentials, got %q", got)
		}
	})

	t.Run("wildcard plus explicit origin keeps credentials for the explicit one", func(t *testing.T) {
		rr := do(handler("*", "https://ui.example.com"), "https://ui.example.com")
		if rr.Header().Get("Access-Control-Allow-Origin") != "https://ui.example.com" ||
			rr.Header().Get("Access-Control-Allow-Credentials") != "true" {
			t.Errorf("explicit origin must still be echoed with credentials, got %v", rr.Header())
		}
	})
}
