// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package artifactpath

import "testing"

func TestSplit(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		cur        Path
		legacyDiff bool // legacy reading differs from cur
		ok         bool
	}{
		{
			name: "four segments",
			path: "org.example/mod/1.0/mod-1.0.jar",
			cur:  Path{"org.example", "mod", "1.0", "mod-1.0.jar"},
			ok:   true,
		},
		{
			name: "ivy layout keeps its type directory in the file name",
			path: "org.example/mod/1.0/jars/mod-1.0.jar",
			cur:  Path{"org.example", "mod", "1.0", "jars/mod-1.0.jar"},
			ok:   true,
		},
		{
			name:       "maven layout, two-segment group",
			path:       "com/example/lib/1.0/lib-1.0.jar",
			cur:        Path{"com.example", "lib", "1.0", "lib-1.0.jar"},
			legacyDiff: true,
			ok:         true,
		},
		{
			name:       "maven layout, deep group and classifier",
			path:       "org/apache/commons/commons-lang3/3.14.0/commons-lang3-3.14.0-sources.jar",
			cur:        Path{"org.apache.commons", "commons-lang3", "3.14.0", "commons-lang3-3.14.0-sources.jar"},
			legacyDiff: true,
			ok:         true,
		},
		{
			name:       "maven layout, checksum file",
			path:       "com/example/lib/1.0/lib-1.0.pom.sha1",
			cur:        Path{"com.example", "lib", "1.0", "lib-1.0.pom.sha1"},
			legacyDiff: true,
			ok:         true,
		},
		{
			name:       "maven layout, timestamped snapshot",
			path:       "com/example/lib/1.1-SNAPSHOT/lib-1.1-20250101.120000-1.jar",
			cur:        Path{"com.example", "lib", "1.1-SNAPSHOT", "lib-1.1-20250101.120000-1.jar"},
			legacyDiff: true,
			ok:         true,
		},
		{name: "too short", path: "a/b/c"},
		{name: "empty segment", path: "a//c/d.jar"},
		{name: "trailing slash", path: "a/b/c/"},
		{name: "empty", path: ""},
		{name: "dot-dot segment", path: "org/../mod/1.0/mod-1.0.jar"},
		{name: "dot-dot file name", path: "org/mod/1.0/.."},
		{name: "dot segment", path: "org/mod/./1.0/mod-1.0.jar"},
		{name: "dot-dot in Ivy subdirectory", path: "org/mod/1.0/jars/../../x.jar"},
		{name: "backslash", path: `org/mod/1.0/..\x.jar`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cur, legacy, ok := Split(tc.path)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}

			if !ok {
				return
			}

			if cur != tc.cur {
				t.Errorf("cur = %+v, want %+v", cur, tc.cur)
			}

			if (cur != legacy) != tc.legacyDiff {
				t.Errorf("legacy = %+v differs from cur = %v, want %v", legacy, cur != legacy, tc.legacyDiff)
			}
		})
	}
}
