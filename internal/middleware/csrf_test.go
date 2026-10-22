// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
)

func csrfNext(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*called = true
		w.WriteHeader(http.StatusNoContent)
	})
}

func TestCSRFMiddlewareAllowsMatchingToken(t *testing.T) {
	called := false
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/repositories", nil)
	r.AddCookie(&http.Cookie{Name: CSRFTokenCookieName, Value: "token"})
	r.Header.Set("X-Csrf-Token", "token")
	w := httptest.NewRecorder()

	CSRFMiddleware("")(csrfNext(&called)).ServeHTTP(w, r)

	if !called || w.Code != http.StatusNoContent {
		t.Fatalf("expected matching token to reach handler, got called=%v status=%d", called, w.Code)
	}
}

func TestCSRFMiddlewareRejectsMissingOrMismatchedToken(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cookie string
		header string
	}{
		{name: "missing"},
		{name: "mismatched", cookie: "token", header: "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			r := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/api/v1/repositories/example", nil)
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: CSRFTokenCookieName, Value: tc.cookie})
			}
			if tc.header != "" {
				r.Header.Set("X-Csrf-Token", tc.header)
			}
			w := httptest.NewRecorder()

			CSRFMiddleware("")(csrfNext(&called)).ServeHTTP(w, r)

			if called || w.Code != http.StatusForbidden {
				t.Fatalf("expected rejection, got called=%v status=%d", called, w.Code)
			}

			var body map[string]string
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["error"] != "csrf_protection" {
				t.Fatalf("expected csrf_protection error, got %q", body["error"])
			}
		})
	}
}

func TestCSRFMiddlewareSeedsCookieOnSafeMethod(t *testing.T) {
	called := false
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/repositories", nil)

	CSRFMiddleware("")(csrfNext(&called)).ServeHTTP(w, r)

	if !called || w.Code != http.StatusNoContent {
		t.Fatalf("expected safe method to reach handler, got called=%v status=%d", called, w.Code)
	}

	var csrf *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == CSRFTokenCookieName {
			csrf = c
			break
		}
	}
	if csrf == nil || csrf.Value == "" {
		t.Fatal("expected CSRF cookie to be seeded on safe method")
	}
}

func TestCSRFMiddlewareAllowsSafeMethods(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			called := false
			w := httptest.NewRecorder()
			r := httptest.NewRequestWithContext(context.Background(), method, "/api/v1/repositories", nil)

			CSRFMiddleware("")(csrfNext(&called)).ServeHTTP(w, r)

			if !called || w.Code != http.StatusNoContent {
				t.Fatalf("expected safe method to reach handler, got called=%v status=%d", called, w.Code)
			}
		})
	}
}

func TestCSRFMiddlewareAllowsSAMLACS(t *testing.T) {
	called := false
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/auth/sso/saml/example/acs", nil)

	CSRFMiddleware("")(csrfNext(&called)).ServeHTTP(w, r)

	if !called || w.Code != http.StatusNoContent {
		t.Fatalf("expected SAML ACS to reach handler, got called=%v status=%d", called, w.Code)
	}
}

func TestCSRFMiddlewareBasicCrossSiteRejected(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"cli no browser headers", nil, true},
		{"same origin", map[string]string{"Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"}, true},
		{"cross-site fetch", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "http://evil.test"}, false},
		{"foreign origin only", map[string]string{"Origin": "http://evil.test"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/repositories", nil)
			r.Host = "example.com"

			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}

			r = r.WithContext(WithPrincipal(r.Context(), &auth.Principal{Credential: auth.CredentialBasicToken}))
			CSRFMiddleware("")(csrfNext(&called)).ServeHTTP(httptest.NewRecorder(), r)

			if called != tc.want {
				t.Fatalf("called=%v want %v", called, tc.want)
			}
		})
	}
}

func TestCSRFMiddlewareSeededCookieSecureForHTTPSBaseURL(t *testing.T) {
	var called bool

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/repositories", nil)
	w := httptest.NewRecorder()
	CSRFMiddleware("https://oa.example.com")(csrfNext(&called)).ServeHTTP(w, r)

	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("seeded CSRF cookie must be Secure behind an https base URL, got %+v", cookies)
	}
}
