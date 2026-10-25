// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/pathseg"
)

func TestClassifyDebRPMPaths(t *testing.T) {
	tests := []struct {
		name   string
		format string
		path   string
		class  pathClass
		dist   string
	}{
		{"deb release", "deb", "dists/stable/Release", classMetadata, "stable"},
		{"deb packages", "deb", "dists/stable/main/binary-amd64/Packages.gz", classMetadata, "stable"},
		{"deb pool file", "deb", "pool/main/f/foo/foo_1.0_amd64.deb", classFile, ""},
		{"deb dist is dot-dot", "deb", "dists/../Release", classNone, ""},
		{"deb dist is dot-dot with packages", "deb", "dists/../main/binary-amd64/Packages", classNone, ""},
		{"deb dist is dot", "deb", "dists/./Release", classNone, ""},
		{"deb dist is empty", "deb", "dists//Release", classNone, ""},
		{"deb dist with backslash", "deb", "dists/a\\b/Release", classNone, ""},
		{"deb dotted file segment", "deb", "dists/stable/../x/Packages", classNone, ""},
		{"deb dot-dot-prefixed name stays valid", "deb", "dists/stable/main/..binary/Packages", classMetadata, "stable"},
		{"deb pool traversal", "deb", "pool/../dists/stable/Release", classNone, ""},
		{"deb pool empty segment", "deb", "pool//foo.deb", classNone, ""},
		{"deb pool trailing slash", "deb", "pool/main/", classNone, ""},
		{"deb pool backslash", "deb", "pool/main\\foo.deb", classNone, ""},
		{"deb pool NUL", "deb", "pool/main/foo\x00.deb", classNone, ""},
		{"deb outside dists and pool", "deb", "other/file", classNone, ""},
		{"rpm repomd", "rpm", "repodata/repomd.xml", classMetadata, ""},
		{"rpm package", "rpm", "Packages/f/foo-1.0.x86_64.rpm", classFile, ""},
		{"rpm package at root", "rpm", "foo-1.0.x86_64.rpm", classFile, ""},
		{"rpm package traversal", "rpm", "../foo.rpm", classNone, ""},
		{"rpm package inner traversal", "rpm", "Packages/../../foo.rpm", classNone, ""},
		{"rpm package empty segment", "rpm", "Packages//foo.rpm", classNone, ""},
		{"rpm package backslash", "rpm", "Packages\\foo.rpm", classNone, ""},
		{"rpm empty path", "rpm", "", classNone, ""},
		{"rpm repodata nested", "rpm", "repodata/sub/primary.xml.gz", classNone, ""},
		{"rpm repodata dot-dot", "rpm", "repodata/../primary.xml.gz", classNone, ""},
		{"rpm repodata backslash", "rpm", "repodata/a\\primary.xml.gz", classNone, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDebRPM(tc.format, tc.path)
			if got.class != tc.class || got.dist != tc.dist {
				t.Fatalf("classifyDebRPM(%q, %q) = class %d dist %q, want class %d dist %q", tc.format, tc.path, got.class, got.dist, tc.class, tc.dist)
			}
		})
	}
}

// FuzzClassifyDebRPM holds the classifier to the contract the group handlers
// rely on: whatever it accepts is a relative path of plain segments (except
// the fixed "public.key"), and a Debian distribution it names is one plain
// segment, so neither can be a parent reference, an empty segment or carry a
// separator the member lookup would read differently.
func FuzzClassifyDebRPM(f *testing.F) {
	for _, s := range []string{
		"public.key", "dists/stable/Release", "dists/../Release", "dists//Release", "dists/a/../b/Packages",
		"pool/main/foo.deb", "pool/../x", "repodata/repomd.xml", "repodata/../x.gz", "Packages/foo.rpm", "../x", "",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, p string) {
		for _, format := range []string{"deb", "rpm"} {
			got := classifyDebRPM(format, p)
			if got.class == classNone || p == "public.key" {
				continue
			}

			if !pathseg.Path(p) {
				t.Fatalf("%s: %q classified as %d but is not a plain path", format, p, got.class)
			}

			if got.dist != "" && !pathseg.Name(got.dist) {
				t.Fatalf("%s: %q yields distribution %q that is not a plain segment", format, p, got.dist)
			}

			if got.class == classMetadata && format == "deb" && !strings.HasPrefix(p, "dists/"+got.dist+"/") {
				t.Fatalf("deb: %q yields distribution %q that is not its dists/ segment", p, got.dist)
			}
		}
	})
}

func TestNuGetSegmentContract(t *testing.T) {
	for in, want := range map[string]bool{
		"newtonsoft.json": true, "1.0.0-rc.1": true, "": false, ".": false, "..": false,
		"a/b": false, "a\\b": false, "a\x00": false,
	} {
		if got := nugetSegment(in); got != want {
			t.Errorf("nugetSegment(%q) = %v, want %v", in, got, want)
		}
	}
}
