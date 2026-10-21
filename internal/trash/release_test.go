// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package trash

import "testing"

func TestIsRelease(t *testing.T) {
	t.Parallel()

	tests := []struct {
		format, version string
		want            bool
	}{
		{"maven", "1.0.0", true},
		{"maven", "1.0.0-SNAPSHOT", false},
		{"maven", "1.0.0-snapshot", false},
		{"maven", "1.0.0-rc1", true},
		{"sbt", "0.1.0-SNAPSHOT", false},
		{"sbt", "0.1.0", true},
		{"ivy", "1.0-SNAPSHOT", false},
		{"npm", "1.2.3", true},
		{"npm", "1.2.3-beta.1", false},
		{"npm", "1.2.3+build.5", true},
		{"nuget", "2.0.0-preview1", false},
		{"nuget", "2.0.0", true},
		{"cargo", "0.3.0-alpha", false},
		{"helm", "1.0.0-rc.1", false},
		{"helm", "1.0.0", true},
		{"go", "v1.2.3", true},
		{"go", "v2.0.0+incompatible", true},
		{"go", "v0.0.0-20240101000000-abcdef123456", false},
		{"pypi", "1.0", true},
		{"pypi", "1.0.post1", true},
		{"pypi", "1.0+local.1", true},
		{"pypi", "1!2.0", true},
		{"pypi", "1.0a1", false},
		{"pypi", "1.0b2", false},
		{"pypi", "1.0rc1", false},
		{"pypi", "1.0.dev3", false},
		{"pypi", "1.0.post1.dev2", false},
		{"rubygems", "1.2.3", true},
		{"rubygems", "1.2.3.pre", false},
		{"rubygems", "1.0.0.rc1", false},
		{"raw", "", true},
		{"deb", "1.0~rc1", true},
		{"rpm", "1.0", true},
		{"conda", "1.0.dev0", true},
		{"docker", "latest", true},
	}

	for _, tt := range tests {
		if got := IsRelease(tt.format, tt.version); got != tt.want {
			t.Errorf("IsRelease(%q, %q) = %v, want %v", tt.format, tt.version, got, tt.want)
		}
	}
}
