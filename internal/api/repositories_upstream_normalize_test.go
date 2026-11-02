// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import "testing"

func TestNormalizeUpstreamURL(t *testing.T) {
	for _, tc := range []struct{ format, in, want string }{
		{"pypi", "https://pypi.org", "https://pypi.org/simple"},
		{"pypi", "https://pypi.org/", "https://pypi.org/simple"},
		{"pypi", "https://PyPI.org", "https://PyPI.org/simple"},
		{"pypi", "https://pypi.org/simple", "https://pypi.org/simple"},
		{"pypi", "https://pypi.org/simple/", "https://pypi.org/simple/"},
		{"pypi", "https://mirror.example.com", "https://mirror.example.com"},
		{"pypi", "https://mirror.example.com/root/pypi/+simple/", "https://mirror.example.com/root/pypi/+simple/"},
		{"npm", "https://pypi.org", "https://pypi.org"},
	} {
		if got := normalizeUpstreamURL(tc.format, tc.in); got != tc.want {
			t.Errorf("normalizeUpstreamURL(%q, %q) = %q, want %q", tc.format, tc.in, got, tc.want)
		}
	}
}
