// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubygems

import "testing"

func TestValidGemIdentSegments(t *testing.T) {
	for in, want := range map[string]bool{
		"rails": true, "7.1.0.rc1": true, "..x": true,
		"": false, ".": false, ".. ": false, "..": false, "a/b": false, "a\\b": false, "a b": false, "a\x00": false, "\xff": false,
	} {
		if got := validGemIdent(in, 64); got != want {
			t.Errorf("validGemIdent(%q) = %v, want %v", in, got, want)
		}
	}
}
