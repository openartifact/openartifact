// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"strings"
	"testing"
)

func TestParseV1Path(t *testing.T) {
	t.Parallel()

	ref := Ref{Name: "zlib", Version: "1.3.1", User: "_", Channel: "_"}
	base := "v1/conans/zlib/1.3.1/_/_"
	files := "v1/files/zlib/1.3.1/_/_/" + testRRev

	tests := []struct {
		rel  string
		want V1Path
	}{
		{"v1/conans/search", V1Path{Kind: V1Search}},
		{base, V1Path{Kind: V1Snapshot, Ref: ref}},
		{"/" + base, V1Path{Kind: V1Snapshot, Ref: ref}},
		{base + "/search", V1Path{Kind: V1PackageSearch, Ref: ref}},
		{base + "/download_urls", V1Path{Kind: V1DownloadURLs, Ref: ref}},
		{base + "/digest", V1Path{Kind: V1Digest, Ref: ref}},
		{base + "/packages/" + testPID, V1Path{Kind: V1PackageSnapshot, Ref: ref, PID: testPID}},
		{base + "/packages/" + testPID + "/download_urls", V1Path{Kind: V1PackageDownloadURLs, Ref: ref, PID: testPID}},
		{base + "/packages/" + testPID + "/digest", V1Path{Kind: V1PackageDigest, Ref: ref, PID: testPID}},
		{files + "/export/conanfile.py", V1Path{Kind: V1File, Ref: ref, RRev: testRRev, File: "conanfile.py"}},
		{
			files + "/package/" + testPID + "/" + testPRev + "/conan_package.tgz",
			V1Path{Kind: V1PackageFile, Ref: ref, RRev: testRRev, PID: testPID, PRev: testPRev, File: "conan_package.tgz"},
		},
	}

	for _, tc := range tests {
		got, ok := ParseV1Path(tc.rel)
		if !ok || got != tc.want {
			t.Errorf("ParseV1Path(%q) = %+v, %v; want %+v", tc.rel, got, ok, tc.want)
		}

		if got.String() != strings.TrimPrefix(tc.rel, "/") {
			t.Errorf("String() of %q = %q", tc.rel, got.String())
		}
	}
}

func TestParseV1PathRejects(t *testing.T) {
	t.Parallel()

	base := "v1/conans/zlib/1.3.1/_/_"
	files := "v1/files/zlib/1.3.1/_/_/" + testRRev

	for _, rel := range []string{
		"", "v1/conans", "v1/conans/", "v1/ping", "v2/conans/search", "v1/conans/zlib/1.3.1/_",
		base + "/upload_urls", base + "/remove_files", base + "/packages/" + testPID + "/upload_urls",
		base + "/packages/" + testPID + "/other", base + "/digest/", base + "/packages/../digest", "v1/conans/../a/1/_/_/digest",
		files, files + "/export", files + "/export/", files + "/export/../x", files + "/export//x", files + "/export/a%2fb",
		files + "/package/" + testPID, files + "/package/" + testPID + "/" + testPRev, files + "/package/" + testPID + "/" + testPRev + "/",
		files + "/other/x", "v1/files/zlib/1.3.1/_/_/../export/x", "v1/files/zlib/1.3.1/_/_/a?b/export/x",
	} {
		if p, ok := ParseV1Path(rel); ok {
			t.Errorf("ParseV1Path(%q) = %+v, want rejection", rel, p)
		}
	}
}

func TestV1PathV2File(t *testing.T) {
	t.Parallel()

	ref := Ref{Name: "zlib", Version: "1.3.1", User: "_", Channel: "_"}

	got, ok := V1Path{Kind: V1PackageFile, Ref: ref, RRev: testRRev, PID: testPID, PRev: testPRev, File: "a/b"}.V2File()
	want := Path{Kind: KindFile, Ref: ref, RRev: testRRev, PID: testPID, PRev: testPRev, File: "a/b"}

	if !ok || got != want {
		t.Errorf("V2File = %+v, %v; want %+v", got, ok, want)
	}

	if _, ok := (V1Path{Kind: V1Digest, Ref: ref}).V2File(); ok {
		t.Error("a digest has a Conan 2 file")
	}
}

func TestV1FileURLEscapesAndRoundTrips(t *testing.T) {
	t.Parallel()

	ref := Ref{Name: "zlib", Version: "1.3.1", User: "_", Channel: "_"}

	got := V1FileURL("http://oa.test/repos/r/", ref, testRRev, testPID, testPRev, "sub dir/x.tgz")
	want := "http://oa.test/repos/r/v1/files/zlib/1.3.1/_/_/" + testRRev + "/package/" + testPID + "/" + testPRev + "/sub%20dir/x.tgz"

	if got != want {
		t.Errorf("V1FileURL = %q, want %q", got, want)
	}

	if got := V1FileURL(
		"http://oa.test/repos/r",
		ref,
		"0",
		"",
		"",
		"conanfile.py",
	); !strings.HasSuffix(
		got,
		"/v1/files/zlib/1.3.1/_/_/0/export/conanfile.py",
	) {
		t.Errorf("recipe file URL = %q", got)
	}
}

func TestNameOfAndRepoURL(t *testing.T) {
	t.Parallel()

	for rel, want := range map[string]string{
		"v2/conans/zlib/1.3.1/_/_/latest": "zlib", "v1/conans/zlib/1.3.1/_/_/digest": "zlib",
		"v1/files/zlib/1.3.1/_/_/0/export/conanfile.py": "zlib", "v2/conans/search": "", "v1/conans/search": "", "x": "",
	} {
		if got := NameOf(rel); got != want {
			t.Errorf("NameOf(%q) = %q, want %q", rel, got, want)
		}
	}

	if got := RepoURL("http://oa.test/", "my repo"); got != "http://oa.test/repos/my%20repo" {
		t.Errorf("RepoURL = %q", got)
	}
}

func TestParseFileMap(t *testing.T) {
	t.Parallel()

	m, err := ParseFileMap([]byte(`{"conanfile.py":"abc"}`))
	if err != nil || m["conanfile.py"] != "abc" {
		t.Errorf("ParseFileMap = %v, %v", m, err)
	}

	for _, doc := range []string{``, `null`, `[]`, `{"../x":"a"}`, `{"a":""}`, `{"a":1}`} {
		if _, err := ParseFileMap([]byte(doc)); err == nil {
			t.Errorf("ParseFileMap(%q) accepted", doc)
		}
	}
}
