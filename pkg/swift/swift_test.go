// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package swift_test

import (
	"bytes"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/swift"
	"github.com/openartifact/openartifact/pkg/swift/swifttest"
)

func TestValidIdentity(t *testing.T) {
	t.Parallel()

	for s, want := range map[string]bool{
		"mona": true, "Mona-1": true, "a": true, strings.Repeat("a", 39): true,
		"": false, "-a": false, "a-": false, "a--b": false, "a_b": false, "a.b": false, strings.Repeat("a", 40): false,
	} {
		if got := swift.ValidScope(s); got != want {
			t.Errorf("ValidScope(%q) = %v, want %v", s, got, want)
		}
	}

	for s, want := range map[string]bool{
		"LinkedList": true, "a_b-c": true, "a1": true, strings.Repeat("a", 100): true,
		"": false, "_a": false, "a_": false, "a-_b": false, "a__b": false, "a.b": false, strings.Repeat("a", 101): false,
	} {
		if got := swift.ValidName(s); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", s, got, want)
		}
	}

	for v, want := range map[string]bool{
		"1.0.0": true, "0.0.1-alpha.1+b5": true, "1.0.0-rc.1": true,
		"1.0": false, "v1.0.0": false, "01.0.0": false, "1.0.0-": false, "": false,
	} {
		if got := swift.ValidVersion(v); got != want {
			t.Errorf("ValidVersion(%q) = %v, want %v", v, got, want)
		}
	}

	if swift.ValidPublishVersion("1.0.0-x.zip") || swift.ValidPublishVersion("1.0.0-x.json") || !swift.ValidPublishVersion("1.0.0-x") {
		t.Error("ValidPublishVersion must reject versions ending in .zip or .json")
	}

	if scope, name, ok := swift.SplitID("mona.Linked_List"); !ok || scope != "mona" || name != "Linked_List" {
		t.Errorf("SplitID = %q %q %v", scope, name, ok)
	}

	if _, _, ok := swift.SplitID("mona"); ok {
		t.Error("SplitID accepted an id with no dot")
	}

	if swift.Key("Mona", "LinkedList") != "mona.linkedlist" {
		t.Error("Key is not lower case")
	}
}

func TestCompareAndSort(t *testing.T) {
	t.Parallel()

	vs := []string{"1.0.0", "1.10.0", "1.2.0", "2.0.0-rc.1", "2.0.0", "1.0.0-alpha", "1.0.0+b"}
	swift.SortNewestFirst(vs)

	want := []string{"2.0.0", "2.0.0-rc.1", "1.10.0", "1.2.0", "1.0.0+b", "1.0.0", "1.0.0-alpha"}
	if !slices.Equal(vs, want) {
		t.Errorf("SortNewestFirst = %v, want %v", vs, want)
	}
}

func TestParsePath(t *testing.T) {
	t.Parallel()

	for rel, want := range map[string]swift.Path{
		"identifiers":                         {Kind: swift.KindIdentifiers},
		"/login":                              {Kind: swift.KindLogin},
		"mona/LinkedList":                     {Kind: swift.KindList, Scope: "mona", Name: "LinkedList"},
		"/mona/LinkedList.json":               {Kind: swift.KindList, Scope: "mona", Name: "LinkedList"},
		"mona/LinkedList/1.1.1":               {Kind: swift.KindRelease, Scope: "mona", Name: "LinkedList", Version: "1.1.1"},
		"mona/LinkedList/1.1.1.json":          {Kind: swift.KindRelease, Scope: "mona", Name: "LinkedList", Version: "1.1.1"},
		"mona/LinkedList/1.1.1.zip":           {Kind: swift.KindArchive, Scope: "mona", Name: "LinkedList", Version: "1.1.1"},
		"mona/LinkedList/1.1.1-rc.1.zip":      {Kind: swift.KindArchive, Scope: "mona", Name: "LinkedList", Version: "1.1.1-rc.1"},
		"mona/LinkedList/1.1.1/Package.swift": {Kind: swift.KindManifest, Scope: "mona", Name: "LinkedList", Version: "1.1.1"},
	} {
		got, ok := swift.ParsePath(rel)
		if !ok || got != want {
			t.Errorf("ParsePath(%q) = %+v, %v; want %+v", rel, got, ok, want)
		}
	}

	for _, rel := range []string{
		"", "mona", "mona/", "mona/-x", "-mona/x", "mona/x/1.0", "mona/x/1.0.0/package.swift", "mona/x/1.0.0/Package.swift/y",
		"mona/x/1.0.0.zip/Package.swift", "mona/x/y/z/w/v", "mona/x/..", "mona/x/1.0.0.tar.gz",
	} {
		if got, ok := swift.ParsePath(rel); ok {
			t.Errorf("ParsePath(%q) = %+v, want not ok", rel, got)
		}
	}
}

func TestCheckAccept(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		accept []string
		want   int
	}{
		{nil, http.StatusOK},
		{[]string{"*/*"}, http.StatusOK},
		{[]string{"application/json"}, http.StatusOK},
		{[]string{swift.MediaJSON}, http.StatusOK},
		{[]string{"application/vnd.swift.registry+json"}, http.StatusOK},
		{[]string{"application/vnd.swift.registry.v2+json, application/vnd.swift.registry.v1+json;q=0.5"}, http.StatusOK},
		{[]string{"application/vnd.swift.registry.v2+json"}, http.StatusUnsupportedMediaType},
		{[]string{"application/vnd.swift.registry.vX+json"}, http.StatusBadRequest},
		{[]string{"application/vnd.swift.registry.1+json"}, http.StatusBadRequest},
		{[]string{"application/vnd.swift.registry.v+json"}, http.StatusBadRequest},
	} {
		if got := swift.CheckAccept(tc.accept); got != tc.want {
			t.Errorf("CheckAccept(%q) = %d, want %d", tc.accept, got, tc.want)
		}
	}
}

func TestLinks(t *testing.T) {
	t.Parallel()

	in := swift.Link{
		URL: "https://x/mona/L/1.0.0/Package.swift?swift-version=4", Rel: swift.RelAlternate,
		Params: map[string]string{"filename": "Package@swift-4.swift", "swift-tools-version": "4.0"},
	}

	got := swift.ParseLinks([]string{in.String() + ", <https://x/a>; rel=\"latest-version\"", "garbage, <broken"})
	if len(got) != 2 || got[0].URL != in.URL || got[0].Rel != "alternate" ||
		got[0].Params["filename"] != "Package@swift-4.swift" || got[0].Params["swift-tools-version"] != "4.0" ||
		got[1].URL != "https://x/a" || got[1].Rel != "latest-version" {
		t.Errorf("ParseLinks = %+v", got)
	}

	want := `<https://x/y>; rel="alternate"; filename="a\"b"`
	if s := (swift.Link{URL: "https://x/y", Rel: "alternate", Params: map[string]string{"filename": `a"b`}}).String(); s != want {
		t.Errorf("String() = %s, want %s", s, want)
	}
}

func TestParseReleaseDocs(t *testing.T) {
	t.Parallel()

	list, err := swift.ParseReleaseList(
		[]byte(`{"releases":{"1.0.0":{"url":"https://x/1"},"bad":{},"2.0.0":{"problem":{"detail":"gone"}}}}`),
	)
	if err != nil {
		t.Fatal(err)
	}

	if vs := list.Versions(); !slices.Equal(vs, []string{"2.0.0", "1.0.0"}) {
		t.Errorf("Versions = %v", vs)
	}

	for _, bad := range []string{``, `[]`, `{}`, `null`, `{"releases":null}`} {
		if _, err := swift.ParseReleaseList([]byte(bad)); !errors.Is(err, swift.ErrInvalidDoc) {
			t.Errorf("ParseReleaseList(%q) error = %v", bad, err)
		}
	}

	sum := strings.Repeat("ab", 32)
	doc := `{"id":"Mona.LinkedList","version":"1.0.0","resources":[{"name":"source-archive","type":"application/zip","checksum":"` + sum + `"}]}`

	rel, err := swift.ParseRelease([]byte(doc), "mona.linkedlist", "1.0.0")
	if err != nil || rel.Checksum() != sum {
		t.Fatalf("ParseRelease = %+v, %v", rel, err)
	}

	for _, id := range [][2]string{{"mona.other", "1.0.0"}, {"mona.linkedlist", "1.0.1"}} {
		if _, err := swift.ParseRelease([]byte(doc), id[0], id[1]); err == nil {
			t.Errorf("ParseRelease accepted %v", id)
		}
	}

	if _, err := swift.ParseRelease([]byte(strings.Replace(doc, sum, "xyz", 1)), "mona.linkedlist", "1.0.0"); err == nil {
		t.Error("ParseRelease accepted a bad checksum")
	}

	if d := swift.DigestHeader(sum); d != "sha-256=q6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6s=" {
		t.Errorf("DigestHeader = %q", d)
	}
}

func TestParseMetadata(t *testing.T) {
	t.Parallel()

	raw, m, err := swift.ParseMetadata([]byte(`{ "author": {"name":"Mona"}, "description":"d", "licenseURL":"https://x/l",
		"originalPublicationTime":"2020-01-02T03:04:05Z", "readmeURL":"https://x/r", "repositoryURLs":["https://github.com/mona/L.git"], "extra": 1 }`))
	if err != nil {
		t.Fatal(err)
	}

	if m.Description != "d" || len(m.RepositoryURLs) != 1 || !bytes.Contains(raw, []byte(`"extra":1`)) || bytes.ContainsAny(raw, " \n\t") {
		t.Errorf("metadata = %s, %+v", raw, m)
	}

	for _, bad := range []string{
		``, `[]`, `null`, `{"licenseURL":"ftp://x"}`, `{"readmeURL":"/relative"}`, `{"originalPublicationTime":"yesterday"}`,
		`{"repositoryURLs":[""]}`, `{"repositoryURLs":"x"}`, `{"description":3}`,
	} {
		if _, _, err := swift.ParseMetadata([]byte(bad)); !errors.Is(err, swift.ErrInvalidMetadata) {
			t.Errorf("ParseMetadata(%q) error = %v", bad, err)
		}
	}
}

func TestNormalizeURL(t *testing.T) {
	t.Parallel()

	for _, s := range []string{
		"https://github.com/mona/LinkedList.git", "git@github.com:mona/LinkedList", "ssh://git@github.com/mona/linkedlist/",
		"HTTP://www.GitHub.com/mona/LinkedList.git/", "https://user:pw@github.com/mona/LinkedList?x=1#y",
	} {
		if got := swift.NormalizeURL(s); got != "github.com/mona/linkedlist" {
			t.Errorf("NormalizeURL(%q) = %q", s, got)
		}
	}

	for _, s := range []string{"", "github.com", "https://", "   "} {
		if got := swift.NormalizeURL(s); got != "" {
			t.Errorf("NormalizeURL(%q) = %q, want empty", s, got)
		}
	}
}

func TestReadArchive(t *testing.T) {
	t.Parallel()

	read := func(data []byte) ([]swift.Manifest, error) {
		return swift.ReadArchive(bytes.NewReader(data), int64(len(data)))
	}

	extra := map[string]string{
		"Package@swift-4.2.swift": swifttest.Manifest("4.2", "L"),
		"Package@swift-5.swift":   swifttest.Manifest("5.0", "L"),
		"Tests/Package.swift":     "nested, ignored",
	}

	for _, dir := range []string{"", "L-1.0.0"} {
		ms, err := read(swifttest.Package(t, dir, "L", extra))
		if err != nil {
			t.Fatalf("dir %q: %v", dir, err)
		}

		var names []string
		for _, m := range ms {
			names = append(names, m.Filename)
		}

		if want := []string{"Package.swift", "Package@swift-4.2.swift", "Package@swift-5.swift"}; !slices.Equal(names, want) {
			t.Fatalf("dir %q: manifests %v, want %v", dir, names, want)
		}

		if ms[0].ToolsVersion != "5.9" || ms[0].SwiftVersion != "" || ms[1].SwiftVersion != "4.2" || ms[1].ToolsVersion != "4.2" {
			t.Errorf("dir %q: manifests %+v", dir, ms)
		}
	}

	big := strings.Repeat("x", swift.MaxManifestBytes+1)

	for name, data := range map[string][]byte{
		"not a zip":        []byte("hello"),
		"no manifest":      swifttest.Archive(t, map[string]string{"Sources/a.swift": "x"}),
		"two top dirs":     swifttest.Archive(t, map[string]string{"a/Package.swift": "x", "b/x.swift": "y"}),
		"manifest too big": swifttest.Archive(t, map[string]string{"Package.swift": big}),
		"binary manifest":  swifttest.Archive(t, map[string]string{"Package.swift": "a\xffb"}),
		"traversal":        swifttest.Archive(t, map[string]string{"Package.swift": "x", "../evil": "y"}),
		"deep manifest":    swifttest.Archive(t, map[string]string{"a/b/Package.swift": "x"}),
	} {
		if _, err := read(data); !errors.Is(err, swift.ErrInvalidArchive) {
			t.Errorf("%s: error = %v, want ErrInvalidArchive", name, err)
		}
	}

	if tv := swift.ToolsVersion("// swift-tools-version: 5.9.1\n"); tv != "5.9.1" {
		t.Errorf("ToolsVersion = %q", tv)
	}

	if tv := swift.ToolsVersion("import X\n// swift-tools-version:5.0"); tv != "" {
		t.Errorf("ToolsVersion on a later line = %q", tv)
	}
}
