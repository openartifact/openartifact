// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"crypto/tls"
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

	CSRFMiddleware("", nil)(csrfNext(&called)).ServeHTTP(w, r)

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

			CSRFMiddleware("", nil)(csrfNext(&called)).ServeHTTP(w, r)

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

	CSRFMiddleware("", nil)(csrfNext(&called)).ServeHTTP(w, r)

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

			CSRFMiddleware("", nil)(csrfNext(&called)).ServeHTTP(w, r)

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

	CSRFMiddleware("", nil)(csrfNext(&called)).ServeHTTP(w, r)

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
			CSRFMiddleware("", nil)(csrfNext(&called)).ServeHTTP(httptest.NewRecorder(), r)

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
	CSRFMiddleware("https://oa.example.com", nil)(csrfNext(&called)).ServeHTTP(w, r)

	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("seeded CSRF cookie must be Secure behind an https base URL, got %+v", cookies)
	}
}

func csrfWithPrincipal(r *http.Request, credential string) *http.Request {
	return r.WithContext(WithPrincipal(r.Context(), &auth.Principal{Credential: credential}))
}

func TestCSRFMiddlewareSessionTokenIsBoundToSession(t *testing.T) {
	const sessionA, sessionB = "aaaa", "bbbb"

	for _, tc := range []struct {
		name    string
		path    string
		cookie  string
		header  string
		session string
		want    int
	}{
		{"api: own token", "/api/v1/repositories", CSRFTokenForSession(sessionA), CSRFTokenForSession(sessionA), sessionA, http.StatusNoContent},
		{"api: header alone is enough", "/api/v1/repositories", "", CSRFTokenForSession(sessionA), sessionA, http.StatusNoContent},
		{"api: token of another session", "/api/v1/repositories", CSRFTokenForSession(sessionB), CSRFTokenForSession(sessionB), sessionA, http.StatusForbidden},
		{"api: planted random pair", "/api/v1/repositories", "planted", "planted", sessionA, http.StatusForbidden},
		{"repos: own token", "/repos/r/f", CSRFTokenForSession(sessionA), CSRFTokenForSession(sessionA), sessionA, http.StatusNoContent},
		{"repos: no header", "/repos/r/f", CSRFTokenForSession(sessionA), "", sessionA, http.StatusForbidden},
		{"v2: no header", "/v2/r/img/blobs/uploads/", "", "", sessionA, http.StatusForbidden},
		{"v2: token of another session", "/v2/r/img/blobs/uploads/", "", CSRFTokenForSession(sessionB), sessionA, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, tc.path, nil)
			r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tc.session})

			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: CSRFTokenCookieName, Value: tc.cookie})
			}

			if tc.header != "" {
				r.Header.Set("X-Csrf-Token", tc.header)
			}

			w := httptest.NewRecorder()
			CSRFMiddleware("", nil)(csrfNext(&called)).ServeHTTP(w, csrfWithPrincipal(r, auth.CredentialSession))

			if w.Code != tc.want || called != (tc.want == http.StatusNoContent) {
				t.Fatalf("status=%d called=%v, want status %d", w.Code, called, tc.want)
			}
		})
	}
}

func TestCSRFMiddlewareNonSessionWritesOutsideAPIPass(t *testing.T) {
	for _, tc := range []struct {
		name       string
		credential string
	}{
		{"anonymous", auth.CredentialNone},
		{"bearer", auth.CredentialBearerToken},
		{"basic", auth.CredentialBasicToken},
	} {
		for _, path := range []string{"/repos/r/f", "/v2/r/img/blobs/uploads/"} {
			t.Run(tc.name+" "+path, func(t *testing.T) {
				called := false
				r := httptest.NewRequestWithContext(context.Background(), http.MethodPut, path, nil)
				r.Header.Set("Origin", "http://evil.test")
				w := httptest.NewRecorder()

				CSRFMiddleware("", nil)(csrfNext(&called)).ServeHTTP(w, csrfWithPrincipal(r, tc.credential))

				if !called {
					t.Fatalf("expected the request to reach the handler, status=%d", w.Code)
				}
			})
		}
	}
}

func csrfCookieOf(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == CSRFTokenCookieName {
			return c
		}
	}

	return nil
}

func TestCSRFMiddlewareSeedsBoundCookieForSession(t *testing.T) {
	const session = "cafe"

	want := CSRFTokenForSession(session)

	for _, tc := range []struct {
		name      string
		cookie    string
		wantReset bool
	}{
		{"missing", "", true},
		{"random pre-binding value", "legacy-random", true},
		{"token of another session", CSRFTokenForSession("beef"), true},
		{"already bound", want, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/auth/me", nil)
			r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})

			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: CSRFTokenCookieName, Value: tc.cookie})
			}

			w := httptest.NewRecorder()
			CSRFMiddleware("", nil)(csrfNext(&called)).ServeHTTP(w, csrfWithPrincipal(r, auth.CredentialSession))

			c := csrfCookieOf(w)
			if tc.wantReset != (c != nil) || (c != nil && c.Value != want) {
				t.Fatalf("cookie=%+v, want reset=%v to %q", c, tc.wantReset, want)
			}
		})
	}
}

func TestCSRFMiddlewareDoesNotSeedOutsideAPI(t *testing.T) {
	called := false
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/r/f", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "cafe"})
	w := httptest.NewRecorder()

	CSRFMiddleware("", nil)(csrfNext(&called)).ServeHTTP(w, csrfWithPrincipal(r, auth.CredentialSession))

	if c := csrfCookieOf(w); c != nil {
		t.Fatalf("download responses must not set cookies, got %+v", c)
	}
}

func TestCSRFCookieSecureFollowsTrustedProxy(t *testing.T) {
	trusted := NewTrustedProxies([]string{"10.0.0.0/8"})

	for _, tc := range []struct {
		name       string
		baseURL    string
		remoteAddr string
		proto      string
		tls        bool
		want       bool
	}{
		{"trusted proxy says https", "", "10.1.2.3:4000", "https", false, true},
		{"trusted proxy says http", "", "10.1.2.3:4000", "http", false, false},
		{"trusted proxy, first hop wins", "", "10.1.2.3:4000", "https, http", false, true},
		{"untrusted peer forges https", "", "203.0.113.9:4000", "https", false, false},
		{"direct TLS", "", "203.0.113.9:4000", "", true, true},
		{"https base URL", "https://oa.example.com", "203.0.113.9:4000", "", false, true},
		{"plain http", "", "203.0.113.9:4000", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/repositories", nil)
			r.RemoteAddr = tc.remoteAddr

			if tc.proto != "" {
				r.Header.Set("X-Forwarded-Proto", tc.proto)
			}

			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}

			w := httptest.NewRecorder()
			CSRFMiddleware(tc.baseURL, trusted)(csrfNext(&called)).ServeHTTP(w, r)

			c := csrfCookieOf(w)
			if c == nil || c.Secure != tc.want {
				t.Fatalf("cookie=%+v, want Secure=%v", c, tc.want)
			}
		})
	}
}

func TestCSRFBasicCrossSiteBehindProxy(t *testing.T) {
	trusted := NewTrustedProxies([]string{"10.0.0.0/8"})

	for _, tc := range []struct {
		name       string
		baseURL    string
		remoteAddr string
		host       string
		headers    map[string]string
		want       bool // the request reaches the handler
	}{
		{
			"same origin through a trusted proxy",
			"", "10.1.2.3:4000", "backend:8080",
			map[string]string{"X-Forwarded-Host": "oa.example.com", "X-Forwarded-Proto": "https", "Origin": "https://oa.example.com"},
			true,
		},
		{
			"foreign origin through a trusted proxy",
			"", "10.1.2.3:4000", "backend:8080",
			map[string]string{"X-Forwarded-Host": "oa.example.com", "X-Forwarded-Proto": "https", "Origin": "https://evil.test"},
			false,
		},
		{
			"wrong scheme through a trusted proxy",
			"", "10.1.2.3:4000", "backend:8080",
			map[string]string{"X-Forwarded-Host": "oa.example.com", "X-Forwarded-Proto": "http", "Origin": "https://oa.example.com"},
			false,
		},
		{
			"forged forwarded host from an untrusted peer",
			"", "203.0.113.9:4000", "oa.example.com",
			map[string]string{"X-Forwarded-Host": "evil.test", "X-Forwarded-Proto": "https", "Origin": "https://evil.test"},
			false,
		},
		{
			"base URL origin behind an untrusted proxy",
			"https://oa.example.com/artifacts", "203.0.113.9:4000", "backend:8080",
			map[string]string{"Origin": "https://oa.example.com"},
			true,
		},
		{
			"foreign origin with a base URL",
			"https://oa.example.com", "203.0.113.9:4000", "backend:8080",
			map[string]string{"Origin": "https://evil.test"},
			false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/repositories", nil)
			r.RemoteAddr = tc.remoteAddr
			r.Host = tc.host

			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}

			CSRFMiddleware(tc.baseURL, trusted)(csrfNext(&called)).ServeHTTP(
				httptest.NewRecorder(), csrfWithPrincipal(r, auth.CredentialBasicToken))

			if called != tc.want {
				t.Fatalf("called=%v want %v", called, tc.want)
			}
		})
	}
}
