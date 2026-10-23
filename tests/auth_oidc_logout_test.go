// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

// ssoLogin performs a full OIDC callback and returns the session cookie and
// the raw ID token the IdP issued.
func ssoLogin(t *testing.T, router chi.Router, idp *flowIdP, slug, sub string) (string, string) {
	t.Helper()

	raw := idp.addCode(t, "code-"+sub, map[string]any{"sub": sub, "preferred_username": sub, "nonce": "n1"})

	rw := flowCallback(t, router, slug, "code-"+sub, callbackOpts{nonce: "n1"})
	if rw.Code != http.StatusFound {
		t.Fatalf("login failed: %d %s", rw.Code, rw.Body.String())
	}

	c := cookieNamed(rw, auth.SessionCookieName)
	if c == nil || c.Value == "" {
		t.Fatal("login did not set a session cookie")
	}

	return c.Value, raw
}

func postLogout(router chi.Router, path, cookie, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
	}

	for k, v := range hdr {
		req.Header.Set(k, v)
	}

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

func sessionAlive(t *testing.T, db *oidcFlowDB, cookie string) bool {
	t.Helper()

	s, _ := auth.ValidateSession(context.Background(), db, cookie)

	return s != nil
}

func sessionCookieCleared(rw *httptest.ResponseRecorder) bool {
	c := cookieNamed(rw, auth.SessionCookieName)
	return c != nil && c.Value == "" && c.MaxAge < 0
}

func sameOrigin() map[string]string {
	return map[string]string{"Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"}
}

// logoutFixture is a provider "lo" on an IdP with (or without) an
// end_session_endpoint.
func logoutFixture(t *testing.T, endSession bool) (*oidcFlowDB, chi.Router, *flowIdP) {
	t.Helper()

	idp := newFlowIdP(t, "", "", endSession)
	db := newOIDCFlowDB()
	seedFlowProvider(t, db, "lo", models.OIDCConfig{Issuer: idp.issuer()}, nil)
	router, _ := flowRouter(t, db)

	return db, router, idp
}

func logoutCrossSiteRejected(t *testing.T, path string) {
	t.Helper()

	db, router, idp := logoutFixture(t, true)
	cookie, _ := ssoLogin(t, router, idp, "lo", "u1")

	for name, hdr := range map[string]map[string]string{
		"sec-fetch-site":         {"Sec-Fetch-Site": "cross-site"},
		"foreign origin":         {"Origin": "https://evil.example"},
		"null origin":            {"Origin": "null"},
		"cross-site, own origin": {"Sec-Fetch-Site": "cross-site", "Origin": "http://example.com"},
	} {
		rw := postLogout(router, path, cookie, "", hdr)
		if rw.Code != http.StatusForbidden || responseCode(t, rw) != "csrf_protection" {
			t.Errorf("%s: expected 403 csrf_protection, got %d %s", name, rw.Code, rw.Body.String())
		}

		if !sessionAlive(t, db, cookie) {
			t.Fatalf("%s: a forged logout must not delete the session", name)
		}
	}
}

func logoutReturnsEndSessionURL(t *testing.T, path string) {
	t.Helper()

	db, router, idp := logoutFixture(t, true)
	cookie, raw := ssoLogin(t, router, idp, "lo", "u1")

	// The client-supplied body is ignored: the hint comes from the DB.
	rw := postLogout(router, path, cookie, `{"id_token":"attacker-token","end_session_url":"https://evil.example"}`, sameOrigin())
	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", rw.Code, rw.Body.String())
	}

	var resp struct {
		EndSessionURL string `json:"end_session_url"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	u, err := url.Parse(resp.EndSessionURL)
	if err != nil || !strings.HasPrefix(resp.EndSessionURL, idp.srv.URL+"/end-session?") {
		t.Fatalf("end_session_url = %q", resp.EndSessionURL)
	}

	q := u.Query()
	if q.Get("id_token_hint") != raw || q.Get("client_id") != "test-client" ||
		q.Get("post_logout_redirect_uri") != flowBaseURL+"/login" {
		t.Errorf("unexpected end-session params: %v", q)
	}

	if sessionAlive(t, db, cookie) || !sessionCookieCleared(rw) {
		t.Error("the local session must be deleted and its cookie cleared")
	}
}

func logoutNonSSOSession(t *testing.T, path string) {
	t.Helper()

	db, router, _ := logoutFixture(t, true)

	cookie, _, err := auth.CreateSession(context.Background(), db, "local-user", "127.0.0.1", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	rw := postLogout(router, path, cookie, "", sameOrigin())
	if rw.Code != http.StatusNoContent || sessionAlive(t, db, cookie) || !sessionCookieCleared(rw) {
		t.Errorf("expected 204 with the session gone, got %d %s", rw.Code, rw.Body.String())
	}
}

func logoutIdPWithoutEndSession(t *testing.T, path string) {
	t.Helper()

	db, router, idp := logoutFixture(t, false)
	cookie, _ := ssoLogin(t, router, idp, "lo", "u1")

	rw := postLogout(router, path, cookie, "", sameOrigin())
	if rw.Code != http.StatusNoContent || sessionAlive(t, db, cookie) {
		t.Errorf("expected 204 with the session gone, got %d %s", rw.Code, rw.Body.String())
	}
}

func logoutWithoutCookie(t *testing.T, path string) {
	t.Helper()

	_, router, _ := logoutFixture(t, true)

	if rw := postLogout(router, path, "", "", sameOrigin()); rw.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rw.Code)
	}
}

func TestOIDC_Logout(t *testing.T) {
	routes := map[string]string{
		"provider route": "/api/v1/auth/sso/oidc/lo/logout",
		"generic route":  "/api/v1/auth/logout",
	}

	scenarios := map[string]func(*testing.T, string){
		"cross-site request is rejected and keeps the session":         logoutCrossSiteRejected,
		"same-origin returns the end-session URL with the stored hint": logoutReturnsEndSessionURL,
		"non-SSO session gets 204":                                     logoutNonSSOSession,
		"IdP without end_session_endpoint gets 204":                    logoutIdPWithoutEndSession,
		"no cookie is a plain 204":                                     logoutWithoutCookie,
	}

	for routeName, path := range routes {
		for name, run := range scenarios {
			t.Run(routeName+"/"+name, func(t *testing.T) { run(t, path) })
		}
	}
}

// A session created by provider B must not be logged out of provider A's IdP,
// and a vanished provider degrades to a local logout.
func TestOIDC_Logout_ProviderBoundaries(t *testing.T) {
	idpA := newFlowIdP(t, "", "", true)
	idpB := newFlowIdP(t, "", "", true)
	db := newOIDCFlowDB()
	seedFlowProvider(t, db, "a", models.OIDCConfig{Issuer: idpA.issuer()}, nil)
	pB := seedFlowProvider(t, db, "b", models.OIDCConfig{Issuer: idpB.issuer()}, nil)
	router, _ := flowRouter(t, db)

	cookieB, _ := ssoLogin(t, router, idpB, "b", "ub")

	rw := postLogout(router, "/api/v1/auth/sso/oidc/a/logout", cookieB, "", sameOrigin())
	if rw.Code != http.StatusNoContent || sessionAlive(t, db, cookieB) {
		t.Errorf("another provider's session via provider a: expected 204 + local logout, got %d %s", rw.Code, rw.Body.String())
	}

	cookieB2, _ := ssoLogin(t, router, idpB, "b", "ub2")

	pB.Enabled = false
	if err := db.UpdateSSOProvider(context.Background(), pB); err != nil {
		t.Fatalf("UpdateSSOProvider: %v", err)
	}

	rw = postLogout(router, "/api/v1/auth/logout", cookieB2, "", sameOrigin())
	if rw.Code != http.StatusNoContent || sessionAlive(t, db, cookieB2) {
		t.Errorf("disabled provider: expected plain local logout (204), got %d %s", rw.Code, rw.Body.String())
	}
}
