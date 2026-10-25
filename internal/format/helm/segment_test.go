// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import "testing"

func TestValidChartIdentSegments(t *testing.T) {
	for in, want := range map[string]bool{
		"nginx": true, "1.2.3-rc.1+b5": true, "..x": true,
		"": false, ".": false, "..": false, "a/b": false, "a\\b": false, "a b": false, "a\x00": false, "a\x7f": false,
	} {
		if got := validChartIdent(in); got != want {
			t.Errorf("validChartIdent(%q) = %v, want %v", in, got, want)
		}
	}
}
