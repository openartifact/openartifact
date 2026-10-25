// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"path"
	"slices"
	"strings"
	"testing"
)

// FuzzSplitArtifactPath feeds arbitrary URL paths of an Ivy or sbt repository
// to the coordinate parser shared by uploads, downloads and deletes: a path is
// valid exactly when it has four or more non-empty segments, the legacy
// reading is the first three segments and the rest, and the current reading is
// either that or the Maven layout whose file name starts with its module.
func FuzzSplitArtifactPath(f *testing.F) {
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
		cur, legacy, ok := SplitArtifactPath(p)
		parts := strings.Split(p, "/")

		plain := !slices.ContainsFunc(parts, func(s string) bool {
			return s == "" || s == "." || s == ".." || strings.ContainsAny(s, "\\\x00")
		})
		if want := len(parts) >= 4 && plain; ok != want {
			t.Fatalf("SplitArtifactPath(%q) ok = %v, want %v", p, ok, want)
		}

		if !ok {
			if cur != (ArtifactPath{}) || legacy != (ArtifactPath{}) {
				t.Fatalf("SplitArtifactPath(%q) is invalid but returned %+v, %+v", p, cur, legacy)
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
				t.Fatalf("SplitArtifactPath(%q) = %+v has an empty coordinate", p, cur)
			}
		}

		if cur != legacy {
			checkMavenReading(t, p, cur)
		}
	})
}

func checkMavenReading(t *testing.T, p string, cur ArtifactPath) {
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

// FuzzDescriptorLicenses feeds arbitrary descriptors (an Ivy ivy.xml or a
// Maven POM from an upload) to the license extraction: only .xml and .pom
// files declare licenses, names are trimmed and non-empty, and there are no
// more of them than the document has license elements.
func FuzzDescriptorLicenses(f *testing.F) {
	f.Add("ivy.xml", []byte(`<ivy-module><info organisation="o" module="m"><license name=" Apache-2.0 " url="u"/><license name=""/></info></ivy-module>`))
	f.Add("lib-1.0.pom", []byte(`<project><licenses><license><name>MIT</name></license><license><name>  </name></license></licenses></project>`))
	f.Add("a.pom", []byte(`<project xmlns="http://maven.apache.org/POM/4.0.0"><licenses><license><name><![CDATA[BSD &amp; Co]]></name></license></licenses></project>`))
	f.Add("lib.jar", []byte(`<info><license name="MIT"/></info>`))
	f.Add("ivy.xml", []byte(`<!DOCTYPE x [<!ENTITY a "&a;&a;">]><info>&a;</info>`))
	f.Add("ivy.xml", []byte(``))

	f.Fuzz(func(t *testing.T, filename string, data []byte) {
		got := DescriptorLicenses(filename, data)

		ext := strings.ToLower(path.Ext(filename))
		descriptor := ext == ".xml" || ext == ".pom"

		if !descriptor && got != nil {
			t.Fatalf("DescriptorLicenses(%q) = %q for a file that is no descriptor", filename, got)
		}

		for _, l := range got {
			if l == "" || l != strings.TrimSpace(l) {
				t.Fatalf("DescriptorLicenses(%q) returned %q among %q", filename, l, got)
			}
		}

		if len(got)*len("<license") > len(data) {
			t.Fatalf("DescriptorLicenses returned %d licenses from %d bytes", len(got), len(data))
		}

		if other := DescriptorLicenses("ivy.xml", data); descriptor && !slices.Equal(got, other) {
			t.Fatalf("DescriptorLicenses(%q) = %q but as ivy.xml %q", filename, got, other)
		}
	})
}
