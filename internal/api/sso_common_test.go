// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"testing"
)

func TestRedirectSSOError(t *testing.T) {
	for _, tt := range []struct{ code, want string }{
		{"sso_login_denied", "/login?sso_error=sso_login_denied"},
		{"saml_invalid", "/login?sso_error=saml_invalid"},
		// A code outside the closed set never reaches the URL.
		{"</script>&x=1", "/login?sso_error=internal_error"},
		{"", "/login?sso_error=internal_error"},
	} {
		rw := httptest.NewRecorder()
		redirectSSOError(rw, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/sso/saml/x/acs", nil), tt.code)

		if rw.Code != http.StatusSeeOther {
			t.Errorf("%q: status %d, want 303 so a POST is not replayed", tt.code, rw.Code)
		}

		if got := rw.Header().Get("Location"); got != tt.want {
			t.Errorf("%q: Location %q, want %q", tt.code, got, tt.want)
		}
	}
}

// Every code the server can put in /login?sso_error= has a message in the web
// UI (web/src/lib/ssoErrors.ts), and the UI knows no code the server never
// sends.
func TestSSOErrorCodesMatchWeb(t *testing.T) {
	src, err := os.ReadFile("../../web/src/lib/ssoErrors.ts")
	if err != nil {
		t.Fatal(err)
	}

	var web []string
	for _, m := range regexp.MustCompile(`(?m)^\s+([a-z0-9_]+): '[a-zA-Z.]+',?$`).FindAllSubmatch(src, -1) {
		web = append(web, string(m[1]))
	}

	got := slices.Sorted(slices.Values(web))
	want := slices.Sorted(slices.Values(ssoErrorCodes))

	if !slices.Equal(got, want) {
		t.Errorf("web/src/lib/ssoErrors.ts codes %v\n differ from ssoErrorCodes %v", got, want)
	}
}
