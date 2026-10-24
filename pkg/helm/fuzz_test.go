// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"net/url"
	"path"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type fuzzMember struct{ name, body string }

// fuzzChartTarball builds a gzip+tar chart archive from regular-file members.
func fuzzChartTarball(members ...fuzzMember) []byte {
	var buf bytes.Buffer

	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for _, m := range members {
		_ = tw.WriteHeader(&tar.Header{Name: m.name, Mode: 0o644, Size: int64(len(m.body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(m.body))
	}

	_ = tw.Close()
	_ = gw.Close()

	return buf.Bytes()
}

// FuzzParseChart feeds arbitrary bytes to the chart tarball parser: it returns
// an error or well-typed metadata, never panics, and is deterministic.
func FuzzParseChart(f *testing.F) {
	f.Add(fuzzChartTarball(fuzzMember{"demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 1.2.3\n"}))
	f.Add(fuzzChartTarball(
		fuzzMember{"demo/charts/sub/Chart.yaml", "apiVersion: v2\nname: sub\nversion: 9.9.9\n"},
		fuzzMember{"demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 1.0.0\nkeywords: [a, b]\nmaintainers:\n- name: x\n  email: x@example.com\nannotations:\n  k: v\n"}))
	f.Add(fuzzChartTarball(fuzzMember{"./demo/Chart.yaml", "name: [unterminated"}))
	f.Add(fuzzChartTarball(fuzzMember{"/Chart.yaml", "name: abs\nversion: 1\n"}))
	f.Add(fuzzChartTarball(fuzzMember{"demo/values.yaml", "a: b\n"}))
	f.Add([]byte{0x1f, 0x8b})
	f.Add([]byte{})

	stringKeys := []string{"apiVersion", "name", "version", "appVersion", "description", "type", "home", "icon"}

	f.Fuzz(func(t *testing.T, data []byte) {
		meta, err := ParseChart(bytes.NewReader(data))
		if err != nil {
			if meta != nil {
				t.Fatalf("ParseChart returned metadata with error %v", err)
			}

			return
		}

		for _, k := range stringKeys {
			if _, ok := meta[k].(string); !ok {
				t.Fatalf("meta[%q] = %#v, want string", k, meta[k])
			}
		}

		if _, ok := meta["keywords"].([]string); !ok {
			t.Fatalf("meta[keywords] = %#v, want []string", meta["keywords"])
		}

		if _, ok := meta["sources"].([]string); !ok {
			t.Fatalf("meta[sources] = %#v, want []string", meta["sources"])
		}

		if _, ok := meta["maintainers"].([]map[string]any); !ok {
			t.Fatalf("meta[maintainers] = %#v, want []map[string]any", meta["maintainers"])
		}

		if _, ok := meta["annotations"].(map[string]string); !ok {
			t.Fatalf("meta[annotations] = %#v, want map[string]string", meta["annotations"])
		}

		again, err := ParseChart(bytes.NewReader(data))
		if err != nil || !reflect.DeepEqual(meta, again) {
			t.Fatalf("ParseChart is not deterministic: %v vs %v (%v)", meta, again, err)
		}
	})
}

// FuzzIsRootChartYAML checks that only <chart>/Chart.yaml qualifies as the root
// manifest: one relative directory level, never an absolute or parent path.
func FuzzIsRootChartYAML(f *testing.F) {
	for _, s := range []string{"demo/Chart.yaml", "./demo/Chart.yaml", "demo/charts/sub/Chart.yaml", "../Chart.yaml", "Chart.yaml", "a//Chart.yaml", "a/../Chart.yaml", ".//a/Chart.yaml", ""} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, name string) {
		if !isRootChartYAML(name) {
			return
		}

		// helm itself refuses archives holding absolute paths.
		if strings.HasPrefix(name, "/") {
			t.Fatalf("isRootChartYAML(%q) accepts an absolute path", name)
		}

		dir, file := path.Split(path.Clean(strings.TrimPrefix(name, "./")))

		dir = strings.TrimSuffix(dir, "/")
		if file != "Chart.yaml" || dir == "" || dir == "." || dir == ".." || strings.Contains(dir, "/") {
			t.Fatalf("isRootChartYAML(%q) accepts directory %q file %q", name, dir, file)
		}
	})
}

const fuzzSampleIndex = `apiVersion: v1
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

func fuzzIndexSeeds(f *testing.F) {
	f.Helper()

	f.Add([]byte(fuzzSampleIndex))
	f.Add([]byte("apiVersion: v1\nentries: {}\n"))
	f.Add([]byte("apiVersion: v1\nentries:\n  a: null\n  b: []\n  c: [1, {version: 2}, {name: x}]\n"))
	f.Add([]byte("apiVersion: v1\nentries:\n  a: &x []\n  b: *x\n"))
	f.Add([]byte("apiVersion: v1\nentries:\n  app:\n  - {name: other, version: 1.0.0, urls: [a.tgz]}\n"))
	f.Add([]byte("apiVersion: v1\nentries:\n  ? [complex]\n  : - {version: 1, urls: [a.tgz]}\n"))
	f.Add([]byte("entries: {}\n"))
	f.Add([]byte("<html></html>"))
	f.Add([]byte{})
}

func fuzzVersionIDs(ix *Index) []string {
	if ix.entries == nil {
		return nil
	}

	var out []string

	for i := 0; i+1 < len(ix.entries.Content); i += 2 {
		key := ix.entries.Content[i].Value

		for _, e := range ix.entries.Content[i+1].Content {
			if cv, ok := chartVersion(key, e); ok && cv.Version != "" {
				out = append(out, key+"\x00"+cv.Version)
			}
		}
	}

	return out
}

// fuzzEntry returns the first mapping entry of ix listed under key with version.
func fuzzEntry(ix *Index, key, version string) *yaml.Node {
	if ix.entries == nil {
		return nil
	}

	for i := 0; i+1 < len(ix.entries.Content); i += 2 {
		if ix.entries.Content[i].Value != key {
			continue
		}

		for _, e := range ix.entries.Content[i+1].Content {
			if cv, ok := chartVersion(key, e); ok && cv.Version == version {
				return e
			}
		}
	}

	return nil
}

func fuzzMustParse(t *testing.T, doc []byte) *Index {
	t.Helper()

	ix, err := ParseIndex(doc)
	if err != nil {
		t.Fatalf("ParseIndex of an accepted document failed on second parse: %v", err)
	}

	return ix
}

func fuzzReparse(t *testing.T, ix *Index) *Index {
	t.Helper()

	out, err := ix.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	again, err := ParseIndex(out)
	if err != nil {
		t.Fatalf("marshalled index no longer parses: %v\n%s", err, out)
	}

	return again
}

// FuzzParseIndex feeds arbitrary documents (an upstream's index.yaml) to the
// index parser and checks that whatever it accepts survives rewriting and
// rendering: a proxy caches the rendered index and a group parses it again.
func FuzzParseIndex(f *testing.F) {
	fuzzIndexSeeds(f)

	f.Fuzz(func(t *testing.T, doc []byte) {
		ix, err := ParseIndex(doc)
		if err != nil {
			if !errors.Is(err, ErrNotIndex) {
				t.Fatalf("ParseIndex error %v is not ErrNotIndex", err)
			}

			return
		}

		want := slices.Collect(ix.Versions())

		// Render and read back: same versions.
		if got := slices.Collect(fuzzReparse(t, ix).Versions()); !reflect.DeepEqual(got, want) {
			t.Fatalf("versions changed by render+parse:\n got %+v\nwant %+v", got, want)
		}

		// An identity rewrite keeps exactly the versions that have a URL.
		idem := fuzzMustParse(t, doc)
		idem.RewriteURLs(func(cv ChartVersion) []string { return cv.URLs })

		var wantServed []ChartVersion

		for _, cv := range want {
			if len(cv.URLs) > 0 {
				wantServed = append(wantServed, cv)
			}
		}

		if got := slices.Collect(idem.Versions()); !reflect.DeepEqual(got, wantServed) {
			t.Fatalf("identity rewrite changed versions:\n got %+v\nwant %+v", got, wantServed)
		}

		// Pointing every version at one URL keeps every version, with that URL,
		// and the result is again a readable index.
		pointed := fuzzMustParse(t, doc)
		pointed.RewriteURLs(func(ChartVersion) []string { return []string{"https://oa.test/charts/x.tgz"} })

		got := slices.Collect(fuzzReparse(t, pointed).Versions())
		if len(got) != len(want) {
			t.Fatalf("rewrite kept %d versions, want %d", len(got), len(want))
		}

		for i, cv := range got {
			if !slices.Equal(cv.URLs, []string{"https://oa.test/charts/x.tgz"}) || cv.Name != want[i].Name || cv.Version != want[i].Version || cv.Digest != want[i].Digest {
				t.Fatalf("version %d after rewrite = %+v, from %+v", i, cv, want[i])
			}
		}
	})
}

// FuzzMergeIndexes merges two arbitrary indexes (two members' index.yaml).
func FuzzMergeIndexes(f *testing.F) {
	f.Add([]byte(fuzzSampleIndex), []byte("apiVersion: v1\nentries:\n  app:\n  - {name: app, version: 1.0.0, digest: other, urls: [b.tgz]}\n  - {name: app, version: 2.0.0, urls: [c.tgz]}\n"))
	f.Add([]byte("apiVersion: v1\nentries: {}\n"), []byte(fuzzSampleIndex))
	f.Add([]byte("apiVersion: v1\nentries:\n  a: [{version: 1}, {version: 1}]\n  a: [{version: 1}]\n"), []byte("apiVersion: v2\n"))
	f.Add([]byte("apiVersion: v1\nentries:\n  x: [{name: y, version: 1}]\n"), []byte("apiVersion: v1\nentries:\n  y: [{name: y, version: 1}]\n"))

	f.Fuzz(func(t *testing.T, docA, docB []byte) {
		a, errA := ParseIndex(docA)
		b, errB := ParseIndex(docB)

		if errA != nil || errB != nil {
			return
		}

		merged := MergeIndexes([]*Index{a, b})
		ids := fuzzVersionIDs(merged)

		// Every (chart, version) once; exactly the union of the inputs.
		union := slices.Concat(fuzzVersionIDs(a), fuzzVersionIDs(b))
		slices.Sort(union)

		union = slices.Compact(union)

		sorted := slices.Sorted(slices.Values(ids))
		if !slices.Equal(sorted, union) {
			t.Fatalf("merged ids %q, want the union %q", sorted, union)
		}

		// The first index that lists a version supplies its entry.
		for i := 0; i+1 < len(merged.entries.Content); i += 2 {
			key := merged.entries.Content[i].Value

			for _, e := range merged.entries.Content[i+1].Content {
				version := scalarOf(e, "version")

				first := fuzzEntry(a, key, version)
				if first == nil {
					first = fuzzEntry(b, key, version)
				}

				if first != e {
					t.Fatalf("entry %s@%s does not come from the first index that lists it", key, version)
				}
			}
		}

		// Rendering is deterministic, readable, and merging the result again
		// changes nothing.
		first, err := merged.Marshal()
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}

		again := MergeIndexes([]*Index{fuzzMustParse(t, docA), fuzzMustParse(t, docB)})

		second, err := again.Marshal()
		if err != nil || !bytes.Equal(first, second) {
			t.Fatalf("merge is not deterministic (%v):\n%s\n---\n%s", err, first, second)
		}

		reread := fuzzReparse(t, merged)
		if got := fuzzVersionIDs(reread); !slices.Equal(got, ids) {
			t.Fatalf("ids changed by render+parse: %q vs %q", got, ids)
		}

		if got := fuzzVersionIDs(MergeIndexes([]*Index{reread})); !slices.Equal(got, ids) {
			t.Fatalf("merging a merged index changed it: %q vs %q", got, ids)
		}
	})
}

// FuzzChartFile checks that a chart URL taken from an upstream index can only
// name a plain file: the proxy and the group serve it as charts/<file>.
func FuzzChartFile(f *testing.F) {
	for _, s := range []string{
		"https://example.com/charts/app-1.0.0.tgz", "app-1.0.0.tgz", "../app-1.0.0.tgz", "a/b/..", "a/b/", "",
		"oci://registry.example/app:1.0.0", "https://h/a%2Fb", "https://h/a%5Cb", "https://h/a%00b", "//h/x.tgz", "https://h/%ff.tgz", "?x", "\x7f",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		file, ok := RelativeChartFile(raw)
		if !ok {
			if file != "" {
				t.Fatalf("RelativeChartFile(%q) = %q with ok=false", raw, file)
			}

			return
		}

		if file == "" || file == "." || file == ".." || strings.ContainsAny(file, "/\\") {
			t.Fatalf("RelativeChartFile(%q) = %q, not a plain file name", raw, file)
		}

		for _, r := range file {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("RelativeChartFile(%q) = %q holds a control character", raw, file)
			}
		}

		if escaped := url.PathEscape(file); strings.Contains(escaped, "/") {
			t.Fatalf("escaped file name %q spans path segments", escaped)
		}

		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("RelativeChartFile accepted %q that does not parse: %v", raw, err)
		}

		if u.Scheme != "" {
			if u.Scheme != "http" && u.Scheme != "https" {
				t.Fatalf("RelativeChartFile(%q) serves a %q URL", raw, u.Scheme)
			}

			if got, ok := ChartFile(u); !ok || got != file {
				t.Fatalf("ChartFile(%q) = %q, %v; RelativeChartFile = %q", raw, got, ok, file)
			}
		}
	})
}
