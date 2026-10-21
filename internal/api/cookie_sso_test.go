// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSafeRedirectPathRejectsOpenRedirects(t *testing.T) {
	for _, bad := range []string{
		"//evil.com", "/\\evil.com", "https://evil.com", "http://evil.com/x", "evil.com", "",
		"/ok\\..\\evil", "/a\r\nSet-Cookie: x=y", "javascript:alert(1)",
	} {
		if got := safeRedirectPath(bad); got != "/" {
			t.Errorf("safeRedirectPath(%q) = %q, want /", bad, got)
		}

		if isOIDCRelativePath(bad) && bad != "/" {
			t.Errorf("isOIDCRelativePath(%q) = true", bad)
		}
	}

	for _, good := range []string{"/", "/repos/foo?x=1", "/ui/settings#frag"} {
		if got := safeRedirectPath(good); got != good {
			t.Errorf("safeRedirectPath(%q) = %q", good, got)
		}
	}
}

func TestSSOStateCookieSecureFollowsBaseURL(t *testing.T) {
	cases := []struct {
		name, baseURL string
		forwardHTTPS  bool
		wantSecure    bool
	}{
		{"https base url", "https://oa.example.com", false, true},
		{"http base url", "http://localhost:8080", false, false},
		{"forwarded https", "http://internal", true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)
			if tc.forwardHTTPS {
				req.Header.Set("X-Forwarded-Proto", "https")
			}

			for _, issue := range []func(http.ResponseWriter){
				func(w http.ResponseWriter) { setSSOStateCookie(w, req, tc.baseURL, "c", "v") },
				func(w http.ResponseWriter) { clearSSOStateCookie(w, req, tc.baseURL, "c") },
			} {
				rw := httptest.NewRecorder()
				issue(rw)

				cookies := rw.Result().Cookies()
				if len(cookies) != 1 {
					t.Fatalf("cookies = %d", len(cookies))
				}

				c := cookies[0]
				if c.Secure != tc.wantSecure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
					t.Errorf("cookie = %+v, want Secure=%v HttpOnly SameSite=Lax", c, tc.wantSecure)
				}
			}
		})
	}
}
