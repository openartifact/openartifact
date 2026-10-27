// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"strings"
	"testing"
)

const (
	testRRev = "cac0f6daea041b0ccf42934163defb20"
	testPID  = "a0ac3e7b3ba0b8c3da4d3b1fbe3dbe1e0e0e0e0e"
	testPRev = "0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f"
)

func TestParsePathKinds(t *testing.T) {
	t.Parallel()

	ref := Ref{Name: "zlib", Version: "1.3.1", User: "_", Channel: "_"}
	base := "v2/conans/zlib/1.3.1/_/_"
	rrev := base + "/revisions/" + testRRev
	prev := rrev + "/packages/" + testPID

	tests := []struct {
		rel  string
		want Path
	}{
		{"v2/conans/search", Path{Kind: KindSearch}},
		{"/v2/conans/search", Path{Kind: KindSearch}},
		{base + "/latest", Path{Kind: KindRecipeLatest, Ref: ref}},
		{base + "/revisions", Path{Kind: KindRecipeRevisions, Ref: ref}},
		{base + "/search", Path{Kind: KindPackageSearch, Ref: ref}},
		{rrev + "/search", Path{Kind: KindPackageSearch, Ref: ref, RRev: testRRev}},
		{rrev + "/files", Path{Kind: KindFiles, Ref: ref, RRev: testRRev}},
		{rrev + "/files/conanfile.py", Path{Kind: KindFile, Ref: ref, RRev: testRRev, File: "conanfile.py"}},
		{rrev + "/files/metadata/sign/s.json", Path{Kind: KindFile, Ref: ref, RRev: testRRev, File: "metadata/sign/s.json"}},
		{prev + "/latest", Path{Kind: KindPackageLatest, Ref: ref, RRev: testRRev, PID: testPID}},
		{prev + "/revisions", Path{Kind: KindPackageRevisions, Ref: ref, RRev: testRRev, PID: testPID}},
		{prev + "/revisions/" + testPRev + "/files", Path{Kind: KindFiles, Ref: ref, RRev: testRRev, PID: testPID, PRev: testPRev}},
		{
			prev + "/revisions/" + testPRev + "/files/conan_package.tgz",
			Path{Kind: KindFile, Ref: ref, RRev: testRRev, PID: testPID, PRev: testPRev, File: "conan_package.tgz"},
		},
	}

	for _, tc := range tests {
		got, ok := ParsePath(tc.rel)
		if !ok || got != tc.want {
			t.Errorf("ParsePath(%q) = %+v, %v; want %+v", tc.rel, got, ok, tc.want)
		}

		if got.String() != strings.TrimPrefix(tc.rel, "/") {
			t.Errorf("String() of %q = %q", tc.rel, got.String())
		}
	}
}

func TestParsePathRejects(t *testing.T) {
	t.Parallel()

	base := "v2/conans/zlib/1.3.1/_/_"
	rrev := base + "/revisions/" + testRRev

	for _, rel := range []string{
		"", "v2/conans", "v2/conans/", "v2/ping", "v1/conans/zlib/1.3.1/_/_",
		base, base + "/", base + "/latest/", base + "/other", base + "/revisions/", base + "/revisions/" + testRRev,
		rrev + "/files/", rrev + "/files/../x", rrev + "/files//x", rrev + "/files/a%2fb", rrev + "/files/a?b",
		rrev + "/packages/" + testPID, rrev + "/packages/" + testPID + "/other", rrev + "/other",
		"v2/conans/../zlib/1.3.1/_/_/latest", "v2/conans/zl ib/1.3.1/_/_/latest", "v2/conans/.zlib/1.3.1/_/_/latest",
		"v2/conans/zlib/1.3.1/_/_/revisions/../files", "v2/conans/zlib/1.3.1/_/_/revisions/..%2f/files",
		"v2/conans/" + strings.Repeat("a", 101) + "/1/_/_/latest",
		rrev + "/files/" + strings.Repeat("a", 600),
		"v2/conans/search/extra",
	} {
		if p, ok := ParsePath(rel); ok {
			t.Errorf("ParsePath(%q) = %+v, want rejection", rel, p)
		}
	}
}

func TestValidRevision(t *testing.T) {
	t.Parallel()

	for s, want := range map[string]bool{
		testRRev: true, "0": true, "a.b_c-d": true, "": false,
		"..": false, ".": false, "a/b": false, "a b": false, strings.Repeat("a", 65): false,
	} {
		if got := ValidRevision(s); got != want {
			t.Errorf("ValidRevision(%q) = %v, want %v", s, got, want)
		}
	}
}
