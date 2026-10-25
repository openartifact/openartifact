// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"strings"
	"testing"
)

func TestCleanPresignedPath(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"a/b/c.bin", "a/b/c.bin", true},
		{"/a/b/c.bin", "a/b/c.bin", true},
		{"a/..b", "a/..b", true},
		{"", "", false},
		{"/", "", false},
		{"a/", "", false},
		{"a//b", "", false},
		{"a/./b", "", false},
		{"a/../b", "", false},
		{"../b", "", false},
		{"a\\b", "", false},
		{"a/b\x00", "", false},
		{strings.Repeat("a", maxPresignedPathBytes+1), "", false},
	}

	for _, tc := range tests {
		got, ok := cleanPresignedPath(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("cleanPresignedPath(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
