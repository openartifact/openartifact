// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzParsePath checks that every accepted registry path is canonical: index
// paths follow cargo's layout exactly and only hold lowercase names, download
// paths only hold name and version characters, nothing can hold a parent
// reference or an empty segment.
func FuzzParsePath(f *testing.F) {
	for _, s := range []string{
		"config.json", "/config.json", "index/config.json", "index/se/rd/serde", "index/1/a", "index/2/ab", "index/3/a/abc",
		"index/3/a/abd", "index/Se/rd/Serde", "index/se/rd/serde/", "index/../etc/passwd", "index//se/rd/serde", "index/se/rd/ser_de-x",
		"api/v1/crates/serde/1.0.0/download", "api/v1/crates/Serde/1.0.0+build/download", "api/v1/crates/serde/../download",
		"api/v1/crates/serde/1.0.0/download/", "api/v1/crates/a/b/c/d", "", "/", "index/",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, rel string) {
		p, err := ParsePath(rel)
		if err != nil {
			if !errors.Is(err, ErrBadPath) {
				t.Fatalf("ParsePath(%q) error %v is not ErrBadPath", rel, err)
			}

			return
		}

		fuzzCheckParsedPath(t, rel, p)

		canonical := strings.TrimPrefix(rel, "/")
		if again, err := ParsePath(canonical); err != nil || again != p {
			t.Fatalf("ParsePath is not stable for %q: %+v, %v", canonical, again, err)
		}
	})
}

// fuzzCheckParsedPath checks that p is the canonical reading of rel.
func fuzzCheckParsedPath(t *testing.T, rel string, p Path) {
	t.Helper()

	canonical := strings.TrimPrefix(rel, "/")

	switch p.Kind {
	case KindConfig:
		if canonical != "config.json" && canonical != "index/config.json" {
			t.Fatalf("ParsePath(%q) = config", rel)
		}
	case KindIndex:
		if !indexNameRE.MatchString(p.Crate) || len(p.Crate) > maxNameLen || p.Crate != strings.ToLower(p.Crate) {
			t.Fatalf("ParsePath(%q) index crate %q", rel, p.Crate)
		}

		if p.IndexPath != IndexPathFor(p.Crate) || canonical != "index/"+p.IndexPath {
			t.Fatalf("ParsePath(%q) index path %q for crate %q", rel, p.IndexPath, p.Crate)
		}
	case KindDownload:
		if !downloadNameRE.MatchString(p.Crate) || len(p.Crate) > maxNameLen || !versionRE.MatchString(p.Version) || len(p.Version) > 100 {
			t.Fatalf("ParsePath(%q) download %q %q", rel, p.Crate, p.Version)
		}

		if canonical != "api/v1/crates/"+p.Crate+"/"+p.Version+"/download" {
			t.Fatalf("ParsePath(%q) download is not canonical", rel)
		}
	default:
		t.Fatalf("ParsePath(%q) kind %d", rel, p.Kind)
	}
}

// FuzzIndexPathFor checks that the layout function and the path parser agree
// for every name a registry can hold.
func FuzzIndexPathFor(f *testing.F) {
	for _, s := range []string{"a", "ab", "abc", "abcd", "serde", "a-b_c", "A", "", "é", strings.Repeat("a", maxNameLen+1)} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, name string) {
		rel := IndexPathFor(name)
		if (rel == "") != (name == "") {
			t.Fatalf("IndexPathFor(%q) = %q", name, rel)
		}

		if !indexNameRE.MatchString(name) || len(name) > maxNameLen {
			return
		}

		p, err := ParsePath("index/" + rel)
		if err != nil || p.Kind != KindIndex || p.Crate != name || p.IndexPath != rel {
			t.Fatalf("ParsePath(index/%s) = %+v, %v for crate %q", rel, p, err, name)
		}
	})
}

func fuzzIndexSeeds(f *testing.F) {
	f.Helper()

	line := `{"name":"serde","vers":"1.0.0","deps":[],"cksum":"` + strings.Repeat("a", 64) + `","features":{},"yanked":false}`

	f.Add([]byte(line + "\n"))
	f.Add([]byte(line + "\n" + strings.Replace(line, "1.0.0", "1.0.0+build", 1) + "\n\n" + strings.Replace(line, "1.0.0", "1.1.0", 1)))
	f.Add([]byte("  " + line + "\r\n"))
	f.Add([]byte(`{"name":"a","vers":"1"} trailing`))
	f.Add([]byte(`{"name":null,"vers":"1"}`))
	f.Add([]byte(`{"NAME":"a","VERS":"1","cksum":5}`))
	f.Add([]byte("\u00a0" + `{"name":"a","vers":"1"}` + "\u0085"))
	f.Add([]byte("[]"))
	f.Add([]byte("\n\n"))
	f.Add([]byte{})
}

// FuzzParseIndex feeds arbitrary bytes (an upstream's sparse index file) to the
// index parser and checks the entries it returns.
func FuzzParseIndex(f *testing.F) {
	fuzzIndexSeeds(f)

	f.Fuzz(func(t *testing.T, doc []byte) {
		entries, err := ParseIndex(doc)

		verr := VerifyIndex(bytes.NewReader(doc))
		if (err == nil) != (verr == nil) {
			t.Fatalf("ParseIndex error %v but VerifyIndex error %v", err, verr)
		}

		if err != nil {
			return
		}

		if len(entries) == 0 {
			t.Fatal("ParseIndex accepted a file without entries")
		}

		for i, e := range entries {
			if bytes.ContainsAny(e.Line, "\n") || len(e.Line) == 0 {
				t.Fatalf("entry line %q holds a newline or is empty", e.Line)
			}

			var probe struct {
				Name *string `json:"name"`
				Vers *string `json:"vers"`
			}

			if err := json.Unmarshal(e.Line, &probe); err != nil || probe.Name == nil || probe.Vers == nil || *probe.Vers != e.Vers {
				t.Fatalf("entry %+v does not hold the line's name and vers (%v)", e, err)
			}

			// FindVersion rescans the file: check the ends only, to stay linear.
			if i > 2 && i != len(entries)-1 {
				continue
			}

			got, ok, err := FindVersion(doc, e.Vers)
			if err != nil || !ok || got.Vers != e.Vers {
				t.Fatalf("FindVersion(%q) = %+v, %v, %v", e.Vers, got, ok, err)
			}

			if first := slices.IndexFunc(entries, func(o Entry) bool { return o.Vers == e.Vers }); !bytes.Equal(entries[first].Line, got.Line) {
				t.Fatalf("FindVersion(%q) is not the first entry", e.Vers)
			}
		}

		if _, ok, err := FindVersion(doc, "\x00no such version"); err != nil || ok {
			t.Fatalf("FindVersion of an absent version = %v, %v", ok, err)
		}
	})
}

// FuzzMergeIndex merges two arbitrary index files (two members' answers for
// one crate): each version once, the first file's entry wins, and the result
// is itself a stable index file.
func FuzzMergeIndex(f *testing.F) {
	line := func(vers, cksum string) string {
		return `{"name":"serde","vers":"` + vers + `","cksum":"` + cksum + `"}` + "\n"
	}

	f.Add([]byte(line("1.0.0", "aa")+line("1.1.0", "bb")), []byte(line("1.0.0+x", "cc")+line("2.0.0", "dd")))
	f.Add([]byte(line("1.0.0", "aa")+line("1.0.0", "bb")), []byte(line("1.0.0", "cc")))
	f.Add([]byte(line("1.0.0", "aa")), []byte("not an index"))
	f.Add([]byte(line("1.0.0+a+b", "aa")), []byte(line("1.0.0", "bb")))

	f.Fuzz(func(t *testing.T, docA, docB []byte) {
		a, errA := ParseIndex(docA)
		b, errB := ParseIndex(docB)

		out, err := MergeIndex([][]byte{docA, docB})
		if (errA != nil || errB != nil) != (err != nil) {
			t.Fatalf("MergeIndex error %v with parse errors %v, %v", err, errA, errB)
		}

		if err != nil {
			return
		}

		merged, err := ParseIndex(out)
		if err != nil || !bytes.HasSuffix(out, []byte("\n")) {
			t.Fatalf("merged index is not an index file (%v): %q", err, out)
		}

		seen := map[string]struct{}{}

		for _, e := range merged {
			key := stripBuild(e.Vers)
			if _, dup := seen[key]; dup {
				t.Fatalf("version %q listed twice in %q", key, out)
			}

			seen[key] = struct{}{}
		}

		// Exactly the versions of the inputs (build metadata aside), each taken
		// from the first file that lists it.
		want := map[string]Entry{}

		for _, e := range slices.Concat(a, b) {
			if _, ok := want[stripBuild(e.Vers)]; !ok {
				want[stripBuild(e.Vers)] = e
			}
		}

		if len(want) != len(merged) {
			t.Fatalf("merged %d versions, want %d", len(merged), len(want))
		}

		for _, e := range merged {
			if w := want[stripBuild(e.Vers)]; !bytes.Equal(w.Line, e.Line) {
				t.Fatalf("version %q is %q, want the first file's %q", e.Vers, e.Line, w.Line)
			}
		}

		if again, err := MergeIndex([][]byte{out}); err != nil || !bytes.Equal(again, out) {
			t.Fatalf("merging a merged file changed it (%v): %q vs %q", err, again, out)
		}
	})
}

// FuzzParseConfig feeds arbitrary bytes (an upstream's config.json) to the
// config reader and expands the download template it yields.
func FuzzParseConfig(f *testing.F) {
	f.Add([]byte(`{"dl":"https://static.crates.io/crates","api":"https://crates.io"}`))
	f.Add([]byte(`{"dl":"https://dl.example/{prefix}/{crate}/{version}-{sha256-checksum}"}`))
	f.Add([]byte(`{"dl":""}`))
	f.Add([]byte(`{"dl":5}`))
	f.Add([]byte(`{"dl":"x"} trailing`))
	f.Add([]byte(`[]`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, doc []byte) {
		c, err := ParseConfig(bytes.NewReader(doc))
		verr := VerifyConfig(bytes.NewReader(doc))

		if (err == nil) != (verr == nil) {
			t.Fatalf("ParseConfig error %v but VerifyConfig error %v", err, verr)
		}

		if err != nil {
			return
		}

		if c.DL == "" {
			t.Fatal("ParseConfig accepted a config without dl")
		}

		if got := DownloadURL(c.DL, "serde", "1.0.0", strings.Repeat("A", 64)); got == "" {
			t.Fatalf("DownloadURL(%q) is empty", c.DL)
		}
	})
}

// FuzzDownloadURL checks the download template expansion: a template without
// markers is a base URL that gets the cargo download path appended, a template
// with markers is expanded in place, and nothing panics for any template.
func FuzzDownloadURL(f *testing.F) {
	f.Add("https://static.crates.io/crates/", "serde", "1.0.0", "abc")
	f.Add("https://dl.example/{lowerprefix}/{crate}/{crate}-{version}.crate?c={sha256-checksum}", "Serde", "1.0.0+b", "ABC")
	f.Add("{{crate}}", "crate", "{version}", "")
	f.Add("", "", "", "")

	markers := []string{"{crate}", "{version}", "{prefix}", "{lowerprefix}", "{sha256-checksum}"}

	f.Fuzz(func(t *testing.T, dl, crate, version, cksum string) {
		got := DownloadURL(dl, crate, version, cksum)

		if slices.ContainsFunc(markers, func(m string) bool { return strings.Contains(dl, m) }) {
			return
		}

		if want := strings.TrimRight(dl, "/") + "/" + crate + "/" + version + "/download"; got != want {
			t.Fatalf("DownloadURL(%q) = %q, want %q", dl, got, want)
		}
	})
}

// FuzzRenderConfig checks that the config.json a proxy or group serves is
// always one a cargo client (and this package) reads back.
func FuzzRenderConfig(f *testing.F) {
	f.Add("https://oa.example/", "crates", true)
	f.Add("http://localhost:8080", "my repo", false)
	f.Add("", "", false)
	f.Add("<&>\"", "\\\n", true)

	f.Fuzz(func(t *testing.T, baseURL, repoName string, public bool) {
		out := RenderConfig(baseURL, repoName, public)

		if !bytes.HasSuffix(out, []byte("\n")) || !json.Valid(out) {
			t.Fatalf("RenderConfig = %q, not a JSON line", out)
		}

		c, err := ParseConfig(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("RenderConfig output is refused by ParseConfig: %v: %q", err, out)
		}

		if !utf8.ValidString(baseURL) || !utf8.ValidString(repoName) {
			return
		}

		if want := strings.TrimRight(baseURL, "/") + "/repos/" + repoName + "/api/v1/crates"; c.DL != want {
			t.Fatalf("dl = %q, want %q", c.DL, want)
		}

		var full map[string]any
		if err := json.Unmarshal(out, &full); err != nil {
			t.Fatal(err)
		}

		if _, has := full["auth-required"]; has == public {
			t.Fatalf("auth-required present=%v for public=%v", has, public)
		}
	})
}
