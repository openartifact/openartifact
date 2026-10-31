// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestIndexPathFor(t *testing.T) {
	for name, want := range map[string]string{
		"a":          "1/a",
		"ab":         "2/ab",
		"abc":        "3/a/abc",
		"abcd":       "ab/cd/abcd",
		"serde_json": "se/rd/serde_json",
	} {
		if got := IndexPathFor(name); got != want {
			t.Errorf("IndexPathFor(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestParsePath(t *testing.T) {
	ok := map[string]Path{
		"config.json":        {Kind: KindConfig},
		"/index/config.json": {Kind: KindConfig},
		"index/1/a":          {Kind: KindIndex, Crate: "a", IndexPath: "1/a"},
		"index/2/ab":         {Kind: KindIndex, Crate: "ab", IndexPath: "2/ab"},
		"index/3/a/abc":      {Kind: KindIndex, Crate: "abc", IndexPath: "3/a/abc"},
		"index/se/rd/serde":  {Kind: KindIndex, Crate: "serde", IndexPath: "se/rd/serde"},
		"api/v1/crates/Serde/1.0.0-rc.1+b/download": {Kind: KindDownload, Crate: "Serde", Version: "1.0.0-rc.1+b"},
	}

	for in, want := range ok {
		got, err := ParsePath(in)
		if err != nil || got != want {
			t.Errorf("ParsePath(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}

	for _, in := range []string{
		"", "index", "index/", "index/serde", "index/se/rd/Serde", "index/xx/rd/serde", "index/1/ab",
		"index/se/rd/serde/", "index/se/rd/se%72de", "index/../config.json", "config.json/x",
		"api/v1/crates/new", "api/v1/crates/serde/download", "api/v1/crates/serde/1.0.0/yank",
		"api/v1/crates/se rde/1.0.0/download", "api/v1/crates/serde/1.0.0/download/x",
		"api/v1/crates/" + strings.Repeat("a", 65) + "/1.0.0/download",
		"api/v1/crates/../1.0.0/download",
	} {
		if got, err := ParsePath(in); err == nil {
			t.Errorf("ParsePath(%q) = %+v, want an error", in, got)
		}
	}
}

func TestDownloadURL(t *testing.T) {
	const sum = "ABCDEF"

	for _, tc := range []struct{ dl, want string }{
		{"https://static.crates.io/crates", "https://static.crates.io/crates/Serde/1.0.0/download"},
		{"https://static.crates.io/crates/", "https://static.crates.io/crates/Serde/1.0.0/download"},
		{"https://dl.example/{crate}-{version}.crate", "https://dl.example/Serde-1.0.0.crate"},
		{"https://dl.example/{prefix}/{crate}", "https://dl.example/Se/rd/Serde"},
		{"https://dl.example/{lowerprefix}/{crate}/{sha256-checksum}", "https://dl.example/se/rd/Serde/abcdef"},
	} {
		if got := DownloadURL(tc.dl, "Serde", "1.0.0", sum); got != tc.want {
			t.Errorf("DownloadURL(%q) = %q, want %q", tc.dl, got, tc.want)
		}
	}
}

func entry(name, vers, cksum string) string {
	b, _ := json.Marshal(map[string]any{"name": name, "vers": vers, "cksum": cksum, "deps": []any{}})
	return string(b)
}

func TestParseIndexAndFindVersion(t *testing.T) {
	doc := []byte(entry("a", "1.0.0", "c1") + "\n\n" + entry("a", "1.1.0", "c2") + "\n")

	e, found, err := FindVersion(doc, "1.1.0")
	if err != nil || !found || e.Cksum != "c2" {
		t.Fatalf("FindVersion = %+v %v %v", e, found, err)
	}

	if _, found, _ := FindVersion(doc, "9.9.9"); found {
		t.Error("found a version that is not listed")
	}

	for name, bad := range map[string]string{
		"empty":       "",
		"html":        "<html>nope</html>",
		"no vers":     `{"name":"a"}`,
		"no name":     `{"vers":"1.0.0"}`,
		"array":       `[1]`,
		"second line": entry("a", "1.0.0", "c") + "\nnot json",
	} {
		if _, err := ParseIndex([]byte(bad)); err == nil {
			t.Errorf("%s: ParseIndex accepted %q", name, bad)
		}
	}
}

func TestMergeIndex_UnionByVersionFirstWins(t *testing.T) {
	first := []byte(entry("a", "1.0.0", "first") + "\n" + entry("a", "1.1.0", "first") + "\n")
	second := []byte(entry("a", "1.0.0+build", "second") + "\n" + entry("a", "2.0.0", "second") + "\n")

	out, err := MergeIndex([][]byte{first, second})
	if err != nil {
		t.Fatal(err)
	}

	entries, err := ParseIndex(out)
	if err != nil {
		t.Fatalf("merged output is not an index: %v", err)
	}

	var got []string
	for _, e := range entries {
		got = append(got, e.Vers+":"+e.Cksum)
	}

	if want := "1.0.0:first 1.1.0:first 2.0.0:second"; strings.Join(got, " ") != want {
		t.Errorf("merged = %v, want %s", got, want)
	}

	if !strings.HasSuffix(string(out), "\n") {
		t.Error("merged index does not end with a newline")
	}

	if _, err := MergeIndex([][]byte{first, []byte("garbage")}); err == nil {
		t.Error("MergeIndex accepted a member that is not an index file")
	}
}

func TestRenderConfig(t *testing.T) {
	var cfg map[string]any

	if err := json.Unmarshal(RenderConfig("http://oa.test/", "r", true), &cfg); err != nil {
		t.Fatal(err)
	}

	if cfg["dl"] != "http://oa.test/repos/r/api/v1/crates" || cfg["api"] != "http://oa.test/repos/r" {
		t.Errorf("config = %v", cfg)
	}

	if _, has := cfg["auth-required"]; has {
		t.Error("public repository demands auth")
	}

	if err := json.Unmarshal(RenderConfig("http://oa.test", "r", false), &cfg); err != nil || cfg["auth-required"] != true {
		t.Errorf("private config = %v (%v), want auth-required", cfg, err)
	}
}

func TestParseConfig(t *testing.T) {
	if c, err := ParseConfig(strings.NewReader(`{"dl":"https://x/crates","api":"y"}`)); err != nil || c.DL != "https://x/crates" {
		t.Errorf("ParseConfig = %+v, %v", c, err)
	}

	for _, bad := range []string{``, `{}`, `{"dl":""}`, `{"dl":3}`, `[]`, `<html>`} {
		if _, err := ParseConfig(strings.NewReader(bad)); err == nil {
			t.Errorf("ParseConfig accepted %q", bad)
		}
	}
}

func TestEntryPublished(t *testing.T) {
	doc := `{"name":"a","vers":"1.0.0","cksum":"x","pubtime":"2025-11-12T19:30:12Z"}
{"name":"a","vers":"1.1.0","cksum":"x"}
{"name":"a","vers":"1.2.0","cksum":"x","pubtime":"last week"}
`

	entries, err := ParseIndex([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}

	if got, want := entries[0].Published(), time.Date(2025, 11, 12, 19, 30, 12, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Published = %v, want %v", got, want)
	}

	for _, e := range entries[1:] {
		if !e.Published().IsZero() {
			t.Errorf("%s: Published = %v, want the zero time", e.Vers, e.Published())
		}
	}
}
