// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import "testing"

func TestParseMavenFilename(t *testing.T) {
	cases := []struct {
		file, art, ver string
		snap           bool
		cls, ext, ts   string
		build          int
		ok             bool
	}{
		{"foo-1.0.jar", "foo", "1.0", false, "", "jar", "", 0, true},
		{"foo-1.0-bin.tar.gz", "foo", "1.0", false, "bin", "tar.gz", "", 0, true},
		{"foo-1.0.jar.asc", "foo", "1.0", false, "", "jar.asc", "", 0, true},
		{"foo-1.0-sources.jar", "foo", "1.0", false, "sources", "jar", "", 0, true},
		{"foo-1.0-SNAPSHOT.jar", "foo", "1.0-SNAPSHOT", true, "", "jar", "", 0, true},
		{"foo-1.0-20240101.120000-3-tests.jar", "foo", "1.0-SNAPSHOT", true, "tests", "jar", "20240101.120000", 3, true},
		{"other-1.0.jar", "foo", "1.0", false, "", "", "", 0, false},
	}
	for _, c := range cases {
		got, ok := parseMavenFilename(c.file, c.art, c.ver, c.snap)
		if got.classifier != c.cls || got.ext != c.ext || got.timestamp != c.ts || got.build != c.build || ok != c.ok {
			t.Errorf("%s: got (%q,%q,%q,%d,%v)", c.file, got.classifier, got.ext, got.timestamp, got.build, ok)
		}
	}
}
