// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import "testing"

func TestContentTypeForFile(t *testing.T) {
	cases := []struct{ filename, want string }{
		{"foo.jar", "application/java-archive"},
		{"foo.pom", "application/xml"},
		{"foo.war", "application/java-archive"},
		{"foo.aar", "application/octet-stream"},
		{"foo.unknown", "application/octet-stream"},
	}
	for _, c := range cases {
		got := ContentTypeForFile(c.filename)
		if got != c.want {
			t.Errorf("ContentTypeForFile(%q) = %q, want %q", c.filename, got, c.want)
		}
	}
}
