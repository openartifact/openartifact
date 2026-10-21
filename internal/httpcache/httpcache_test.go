// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package httpcache

import "testing"

func TestETagMatches(t *testing.T) {
	tests := []struct {
		name   string
		header string
		etag   string
		want   bool
	}{
		{"empty header", "", `"a"`, false},
		{"exact", `"a"`, `"a"`, true},
		{"other", `"b"`, `"a"`, false},
		{"list with match", `"x", "a" ,"y"`, `"a"`, true},
		{"list without match", `"x", "y"`, `"a"`, false},
		{"weak header", `W/"a"`, `"a"`, true},
		{"weak etag", `"a"`, `W/"a"`, true},
		{"wildcard", `*`, `"a"`, true},
		{"prefix is not a match", `"ab"`, `"a"`, false},
		{"unquoted is not a match", `a`, `"a"`, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ETagMatches(tc.header, tc.etag); got != tc.want {
				t.Errorf("ETagMatches(%q, %q) = %v, want %v", tc.header, tc.etag, got, tc.want)
			}
		})
	}
}

func TestCacheControl(t *testing.T) {
	tests := []struct {
		name   string
		shared bool
		maxAge int
		want   string
	}{
		{"private ignores max-age", false, 300, "private, no-cache"},
		{"private", false, 0, "private, no-cache"},
		{"public with max-age", true, 300, "public, max-age=300"},
		{"public revalidating", true, 0, "public, no-cache"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CacheControl(tc.shared, tc.maxAge); got != tc.want {
				t.Errorf("CacheControl(%v, %d) = %q, want %q", tc.shared, tc.maxAge, got, tc.want)
			}
		})
	}
}
