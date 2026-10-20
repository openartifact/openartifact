// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import "testing"

func TestCompareVersions(t *testing.T) {
	less := [][2]string{
		{"1.9", "1.10"}, {"1.0-SNAPSHOT", "1.0"}, {"1.0-alpha-1", "1.0-beta-1"},
		{"1.0-rc1", "1.0"}, {"1.0", "1.0.1"}, {"1.0", "1.0-sp1"}, {"1.0-beta", "1.0-rc"},
		{"2.0.9", "2.0.10"},
	}
	for _, p := range less {
		if CompareVersions(p[0], p[1]) != -1 || CompareVersions(p[1], p[0]) != 1 {
			t.Errorf("expected %s < %s", p[0], p[1])
		}
	}

	for _, p := range [][2]string{{"1.0", "1"}, {"1.0.0", "1-ga"}, {"1.0-final", "1.0"}} {
		if CompareVersions(p[0], p[1]) != 0 {
			t.Errorf("expected %s == %s", p[0], p[1])
		}
	}
}
