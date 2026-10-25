// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import "testing"

func TestUnsafeEntry(t *testing.T) {
	for name, want := range map[string]bool{
		"bin/tool": false, "tool": false, "./bin/tool": false, "a//b": false, "dir/": false, "..x/y": false,
		"": true, "/abs": true, "../x": true, "a/../x": true, "a/..": true, "a\\b": true, "a\x00b": true,
	} {
		if got := unsafeEntry(name); got != want {
			t.Errorf("unsafeEntry(%q) = %v, want %v", name, got, want)
		}
	}
}
