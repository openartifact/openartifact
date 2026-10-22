// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"regexp"
	"strings"
	"testing"
)

var localUsernameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,64}$`)

func TestSSOUsernameAlwaysMatchesLocalAlphabet(t *testing.T) {
	cases := []SSOClaims{
		{Username: "system/legacy-api-key", ExternalID: "s1"},
		{Username: "  spaced name\x00\n", ExternalID: "s1"},
		{Username: strings.Repeat("a", 500), ExternalID: "s1"},
		{Username: "", Email: "alice.smith@example.com", ExternalID: "s1"},
		{Username: "ab", Email: "", ExternalID: "x"},
		{Username: "", Email: "", ExternalID: "auth0|123/456"},
		{Username: "///", Email: "", ExternalID: "!!", ProviderSlug: "p"},
	}

	for _, c := range cases {
		got := ssoUsername(c)
		if !localUsernameRe.MatchString(got) {
			t.Errorf("ssoUsername(%+v) = %q: outside the local username alphabet", c, got)
		}
	}

	if got := ssoUsername(SSOClaims{Username: "system/legacy-api-key"}); strings.Contains(got, "/") {
		t.Errorf("system principal lookalike survived: %q", got)
	}
}

func TestSanitizeSSOEmail(t *testing.T) {
	for in, want := range map[string]string{
		"a@b.example":                   "a@b.example",
		" a@b.example ":                 "a@b.example",
		"no-at-sign":                    "",
		"a b@c.example":                 "",
		"a@b\n.example":                 "",
		strings.Repeat("a", 300) + "@x": "",
	} {
		if got := sanitizeSSOEmail(in); got != want {
			t.Errorf("sanitizeSSOEmail(%q) = %q, want %q", in, got, want)
		}
	}
}
