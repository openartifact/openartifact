// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package artifactpath

import (
	"slices"
	"strings"
	"testing"
)

// FuzzSplit feeds arbitrary URL paths of an Ivy or sbt repository
// to the coordinate parser shared by uploads, downloads and deletes: a path is
// valid exactly when it has four or more non-empty segments, the legacy
// reading is the first three segments and the rest, and the current reading is
// either that or the Maven layout whose file name starts with its module.
func FuzzSplit(f *testing.F) {
	for _, s := range []string{
		"org.example/mod/1.0/mod-1.0.jar",
		"org.example/mod/1.0/jars/mod-1.0.jar",
		"com/example/lib/1.0/lib-1.0.jar",
		"com/example/lib/1.0-SNAPSHOT/lib-1.0-20240101.120000-1.jar",
		"a/b/c",
		"a/b/c/d\x00",
		"a/b/c/..\\d",
		"a//c/d",
		"/a/b/c/d",
		"a/b/c/d/",
		"../../x/y/z/w",
		"",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, p string) {
		cur, legacy, ok := Split(p)
		parts := strings.Split(p, "/")

		plain := !slices.ContainsFunc(parts, func(s string) bool {
			return s == "" || s == "." || s == ".." || strings.ContainsAny(s, "\\\x00")
		})
		if want := len(parts) >= 4 && plain; ok != want {
			t.Fatalf("Split(%q) ok = %v, want %v", p, ok, want)
		}

		if !ok {
			if cur != (Path{}) || legacy != (Path{}) {
				t.Fatalf("Split(%q) is invalid but returned %+v, %+v", p, cur, legacy)
			}

			return
		}

		if got := strings.Join([]string{legacy.Group, legacy.Module, legacy.Version, legacy.Filename}, "/"); got != p {
			t.Fatalf("legacy reading %+v of %q does not rejoin to it: %q", legacy, p, got)
		}

		if len(parts) == 4 && cur != legacy {
			t.Fatalf("four-segment path %q reads as %+v and %+v", p, cur, legacy)
		}

		for _, v := range []string{cur.Group, cur.Module, cur.Version, cur.Filename} {
			if v == "" {
				t.Fatalf("Split(%q) = %+v has an empty coordinate", p, cur)
			}
		}

		if cur != legacy {
			checkMavenReading(t, p, cur)
		}
	})
}

func checkMavenReading(t *testing.T, p string, cur Path) {
	t.Helper()

	tail := "/" + cur.Module + "/" + cur.Version + "/" + cur.Filename
	group, found := strings.CutSuffix(p, tail)

	switch {
	case !found, strings.ReplaceAll(group, "/", ".") != cur.Group:
		t.Fatalf("Maven reading %+v of %q does not match the path", cur, p)
	case strings.Contains(cur.Module+cur.Version+cur.Filename, "/"):
		t.Fatalf("Maven reading %+v of %q holds a slash in a coordinate", cur, p)
	case !strings.HasPrefix(cur.Filename, cur.Module+"-"):
		t.Fatalf("Maven reading %+v of %q: the file does not start with its module", cur, p)
	}
}
