// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"regexp"
	"strings"
	"testing"
)

func TestOrgSlug(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"lowercase", "Acme", "acme"},
		{"underscore", "my_org", "my-org"},
		{"runs collapse", "a__--b", "a-b"},
		{"trim", "-_acme_-", "acme"},
		{"digits", "Team42", "team42"},
		{"already a slug", "my-org", "my-org"},
		{"empty", "", ""},
		{"no alphanumerics", "_-_", ""},
		{"too long stays long", strings.Repeat("a", 100), strings.Repeat("a", 100)},
	}

	valid := regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := OrgSlug(tt.in)
			if got != tt.want {
				t.Fatalf("OrgSlug(%q) = %q, want %q", tt.in, got, tt.want)
			}

			if got != "" && !valid.MatchString(got) {
				t.Errorf("slug %q is not URL-safe", got)
			}
		})
	}
}
