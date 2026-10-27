// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

const sampleIndex = `apiVersion: v1
generated: "2026-01-01T00:00:00Z"
entries:
  app:
  - name: app
    version: 1.0.0
    digest: aaaa
    annotations:
      category: Test
    urls:
    - app-1.0.0.tgz
    - https://mirror.example/app-1.0.0.tgz
  - name: app
    version: 0.9.0
    urls:
    - oci://registry.example/app:0.9.0
  gone:
  - name: gone
    version: 1.0.0
    urls:
    - oci://registry.example/gone:1.0.0
`

func versions(t *testing.T, ix *Index) []ChartVersion {
	t.Helper()

	var out []ChartVersion
	for cv := range ix.Versions() {
		out = append(out, cv)
	}

	return out
}

func TestParseIndex_RejectsNonIndexes(t *testing.T) {
	for name, doc := range map[string]string{
		"empty":            "",
		"html":             "<html></html>",
		"scalar":           "hello",
		"list":             "- a\n- b\n",
		"no apiVersion":    "entries: {}\n",
		"entries scalar":   "apiVersion: v1\nentries: nope\n",
		"entry not a list": "apiVersion: v1\nentries:\n  a: b\n",
		"aliases":          "apiVersion: v1\nentries:\n  a: &x []\n  b: *x\n",
		"broken yaml":      "apiVersion: v1\nentries: [",
	} {
		if _, err := ParseIndex([]byte(doc)); !errors.Is(err, ErrNotIndex) {
			t.Errorf("%s: error = %v, want ErrNotIndex", name, err)
		}
	}

	for name, doc := range map[string]string{
		"empty entries": "apiVersion: v1\nentries: {}\n",
		"null entries":  "apiVersion: v1\nentries:\n",
		"no entries":    "apiVersion: v1\n",
		"json":          `{"apiVersion":"v1","entries":{"a":[{"name":"a","version":"1","urls":["a-1.tgz"]}]}}`,
	} {
		if _, err := ParseIndex([]byte(doc)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestIndex_RewriteURLsDropsUnservableVersionsAndKeepsTheRest(t *testing.T) {
	ix, err := ParseIndex([]byte(sampleIndex))
	if err != nil {
		t.Fatal(err)
	}

	ix.RewriteURLs(func(cv ChartVersion) []string {
		var out []string

		for _, raw := range cv.URLs {
			if u, err := url.Parse(raw); err == nil && u.Scheme != "oci" {
				out = append(out, "https://oa.test/charts/"+cv.Name+"-"+cv.Version+".tgz")
			}
		}

		return out
	})

	got := versions(t, ix)
	if len(got) != 1 || got[0].Name != "app" || got[0].Version != "1.0.0" || got[0].Digest != "aaaa" {
		t.Fatalf("versions after rewrite = %+v, want only app 1.0.0", got)
	}

	if len(got[0].URLs) != 2 || got[0].URLs[0] != "https://oa.test/charts/app-1.0.0.tgz" {
		t.Errorf("urls = %v", got[0].URLs)
	}

	out, err := ix.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"category: Test", "generated:", "digest: aaaa"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("rewritten index lost %q:\n%s", want, out)
		}
	}

	if strings.Contains(string(out), "gone") || strings.Contains(string(out), "oci://") {
		t.Errorf("dropped entries are still in the output:\n%s", out)
	}

	if _, err := ParseIndex(out); err != nil {
		t.Errorf("output does not parse back: %v", err)
	}
}

func TestMergeIndexes_FirstWinsPerNameAndVersion(t *testing.T) {
	a, _ := ParseIndex(
		[]byte(
			"apiVersion: v1\nentries:\n  x:\n  - {name: x, version: '1.0', digest: A, urls: [a]}\n  - {name: x, version: '2.0', digest: A2, urls: [a2]}\n",
		),
	)
	b, _ := ParseIndex(
		[]byte(
			"apiVersion: v1\nentries:\n  x:\n  - {name: x, version: '1.0', digest: B, urls: [b]}\n  - {name: x, version: '3.0', digest: B3, urls: [b3]}\n  y:\n  - {name: y, version: '1', digest: BY, urls: [by]}\n  - {name: y, urls: [noversion]}\n",
		),
	)
	empty, _ := ParseIndex([]byte("apiVersion: v1\n"))

	merged := MergeIndexes([]*Index{empty, a, b})

	digests := map[string]string{}
	for _, cv := range versions(t, merged) {
		digests[cv.Name+"@"+cv.Version] = cv.Digest
	}

	want := map[string]string{"x@1.0": "A", "x@2.0": "A2", "x@3.0": "B3", "y@1": "BY"}
	if len(digests) != len(want) {
		t.Fatalf("merged = %v, want %v", digests, want)
	}

	for k, v := range want {
		if digests[k] != v {
			t.Errorf("%s digest = %q, want %q", k, digests[k], v)
		}
	}

	out, err := merged.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ParseIndex(out); err != nil {
		t.Errorf("merged output does not parse back: %v\n%s", err, out)
	}

	again, _ := MergeIndexes([]*Index{empty, a, b}).Marshal()
	if string(again) != string(out) {
		t.Error("merging the same indexes twice gave different bytes")
	}
}

func TestChartFile(t *testing.T) {
	for raw, want := range map[string]string{
		"https://x.example/charts/app-1.0.0.tgz":          "app-1.0.0.tgz",
		"http://x.example/app-1.0.0.tgz?sig=1#frag":       "app-1.0.0.tgz",
		"https://x.example/a/foo%2Bbar-1.0.0.tgz":         "foo+bar-1.0.0.tgz",
		"https://github.com/o/r/releases/download/v1/c.t": "c.t",
		"oci://registry.example/charts/app:1.0.0":         "",
		"https://x.example/":                              "",
		"https://x.example/dir/":                          "",
		"https://x.example":                               "",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}

		got, ok := ChartFile(u)
		if got != want || ok != (want != "") {
			t.Errorf("ChartFile(%q) = %q, %v; want %q", raw, got, ok, want)
		}
	}

	if f, ok := RelativeChartFile("charts/app-1.0.0.tgz"); !ok || f != "app-1.0.0.tgz" {
		t.Errorf("RelativeChartFile(relative) = %q, %v", f, ok)
	}

	if _, ok := RelativeChartFile("oci://x/y:1"); ok {
		t.Error("RelativeChartFile accepted an oci url")
	}
}
