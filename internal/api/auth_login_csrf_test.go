// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func loginRequest(t *testing.T, contentType string, hdr map[string]string) *http.Request {
	t.Helper()

	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://oa.example.com/api/v1/auth/login",
		strings.NewReader(`{"username":"a","password":"b"}`))
	r.Host = "oa.example.com"

	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}

	for k, v := range hdr {
		r.Header.Set(k, v)
	}

	return r
}

// handleLogin must reject these before touching the database (nil here).
func TestLoginRejectsCSRFVectors(t *testing.T) {
	h := NewAuthHandler(nil, nil, nil, nil)

	for name, r := range map[string]*http.Request{
		"form body":         loginRequest(t, "application/x-www-form-urlencoded", nil),
		"text/plain body":   loginRequest(t, "text/plain", nil),
		"no content type":   loginRequest(t, "", nil),
		"cross-site origin": loginRequest(t, "application/json", map[string]string{"Origin": "https://evil.example"}),
		"sec-fetch-site":    loginRequest(t, "application/json", map[string]string{"Sec-Fetch-Site": "cross-site"}),
		"null origin":       loginRequest(t, "application/json", map[string]string{"Origin": "null"}),
	} {
		w := httptest.NewRecorder()
		h.handleLogin(w, r)

		if w.Code != http.StatusForbidden && w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s: status %d, want 403/415", name, w.Code)
		}
	}
}

func TestLogoutRejectsCrossSite(t *testing.T) {
	h := NewAuthHandler(nil, nil, nil, nil)

	r := loginRequest(t, "", map[string]string{"Origin": "https://evil.example"})
	w := httptest.NewRecorder()
	h.handleLogout(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site logout status %d, want 403", w.Code)
	}

	// Same-origin logout without a session cookie needs no database.
	r = loginRequest(t, "", map[string]string{"Origin": "https://oa.example.com"})
	w = httptest.NewRecorder()
	h.handleLogout(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("same-origin logout status %d, want 204", w.Code)
	}
}

func TestSessionCookiesSecureFromHTTPSBaseURL(t *testing.T) {
	h := NewAuthHandler(nil, nil, nil, nil).WithBaseURL("https://oa.example.com")

	// Plain-HTTP request (TLS terminated upstream, no X-Forwarded-Proto).
	r := loginRequest(t, "", nil)
	w := httptest.NewRecorder()
	h.handleLogout(w, r)

	cookies := w.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("logout set no cookies")
	}

	for _, c := range cookies {
		if !c.Secure {
			t.Errorf("cookie %s lacks Secure although BASE_URL is https", c.Name)
		}
	}
}

func TestLimiterEvictionNeverDropsLockedKeys(t *testing.T) {
	l := newSlidingWindowLimiter(2, time.Hour)
	l.maxKeys = 3

	now := time.Now()
	l.now = func() time.Time { return now }

	// victim is locked out (at its limit).
	l.record("victim")
	l.record("victim")

	// Attacker floods fresh keys well past the cap.
	for i := range 50 {
		now = now.Add(time.Second)
		l.record("junk-" + strings.Repeat("x", i%7) + string(rune('a'+i%26)))
	}

	if _, blocked := l.retryAfter("victim"); !blocked {
		t.Fatal("the victim's lockout was evicted by key flooding")
	}

	if len(l.events) > l.maxKeys {
		t.Fatalf("limiter grew to %d keys, cap %d", len(l.events), l.maxKeys)
	}
}

func TestRequireHTTPSIdentityURL(t *testing.T) {
	for _, ok := range []string{"https://idp.example.com/realms/x", "http://localhost:8081/realms/x", "http://127.0.0.1/x", "http://[::1]/x"} {
		if err := requireHTTPSIdentityURL(ok); err != nil {
			t.Errorf("%s rejected: %v", ok, err)
		}
	}

	for _, bad := range []string{"http://idp.example.com/x", "ftp://idp.example.com", "idp.example.com", "http://10.0.0.5/x"} {
		if err := requireHTTPSIdentityURL(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestSAMLEmailVerifiedRequiresExplicitTrue(t *testing.T) {
	for attr, want := range map[string]bool{"": false, "false": false, "yes": false, "true": true, "TRUE": true} {
		if got := samlEmailVerified(attr); got != want {
			t.Errorf("samlEmailVerified(%q) = %v, want %v", attr, got, want)
		}
	}
}
