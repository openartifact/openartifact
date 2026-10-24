// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import "testing"

func TestDocsURL(t *testing.T) {
	t.Parallel()

	const root = "https://openartifact.github.io/"

	tests := []struct {
		name, base, version, want string
	}{
		{"release", root, "0.4.1", root + "v0.4/"},
		{"release with v prefix", root, "v1.12.3", root + "v1.12/"},
		{"prerelease", root, "0.9.0-rc1", root + "latest/"},
		{"dev", root, "dev", root + "latest/"},
		{"empty version", root, "", root + "latest/"},
		{"base without trailing slash", "https://docs.example.com", "0.4.1", "https://docs.example.com/v0.4/"},
		{"base with many slashes", "https://docs.example.com//", "dev", "https://docs.example.com/latest/"},
		{"empty base", "", "0.4.1", ""},
		{"blank base", "  ", "0.4.1", ""},
		{"base with path", "https://example.com/docs", "2.0.0", "https://example.com/docs/v2.0/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := docsURL(tt.base, tt.version); got != tt.want {
				t.Errorf("docsURL(%q, %q) = %q, want %q", tt.base, tt.version, got, tt.want)
			}
		})
	}
}
