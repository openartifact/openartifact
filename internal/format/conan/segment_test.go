// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import "testing"

func TestConanSegmentValidators(t *testing.T) {
	refs := map[string]bool{"zlib": true, "1.2.13": true, "_": true, "": false, ".": false, "..": false, "a/b": false, "a\\b": false, "a\x00": false}
	for in, want := range refs {
		if got := validRef(in); got != want {
			t.Errorf("validRef(%q) = %v, want %v", in, got, want)
		}
	}

	revs := map[string]bool{"": true, "abc123": true, "a.b": true, ".": false, "..": false, "a/b": false, "a\\b": false}
	for in, want := range revs {
		if got := validRevision(in); got != want {
			t.Errorf("validRevision(%q) = %v, want %v", in, got, want)
		}
	}

	files := map[string]bool{
		"conanfile.py": true, "metadata/sign/signature.json": true, "a/..b": true,
		"": false, "/a": false, "a/": false, "a//b": false, "a/./b": false, "a/../b": false, "../a": false, "a\\b": false, "a\x00": false,
	}
	for in, want := range files {
		if got := validFilename(in); got != want {
			t.Errorf("validFilename(%q) = %v, want %v", in, got, want)
		}
	}
}
