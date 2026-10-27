// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/conda"
)

func repodataSeeds() []string {
	return []string{
		doc(`{"subdir":"noarch"}`, []string{"a-1.0-0.tar.bz2=a"}, []string{"b-1.0-0.conda=b", "c-d-2.0-1.conda=c-d"}),
		doc(`{"subdir":"linux-64","base_url":"https://example.com"}`, nil, nil),
		`{"packages":null,"packages.conda":{}}`,
		`{"packages":{"a-1-0.tar.bz2":{"name":"a"}},"packages":{"a-1-0.tar.bz2":{"name":"b"}}}`,
		`{"packages":{"a\"b-1-0.conda":{}},"info":[]}`,
		`{"packages.conda":{"x-1-0.conda":{"name":"\u0000","depends":["a"]}}}`,
		`{"info":{},"packages":{"a-1-0.tar.bz2":{}}} trailing`,
		`{"packages":{"a-1-0.tar.bz2":`,
		`[]`,
		``,
	}
}

// FuzzScan feeds arbitrary bytes to the repodata scanner: it must not panic,
// must classify its failures, and must only accept documents encoding/json
// accepts too.
func FuzzScan(f *testing.F) {
	for _, s := range repodataSeeds() {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		err := conda.Scan(bytes.NewReader(data), 1<<20, conda.Visitor{
			Info: func(json.RawMessage) error { return nil },
			Entry: func(_ conda.Section, _ string, raw json.RawMessage) error {
				if !json.Valid(raw) || raw[0] != '{' {
					t.Fatalf("entry %q is not a JSON object", raw)
				}

				return nil
			},
		})

		switch {
		case err == nil:
			var v map[string]json.RawMessage
			if jerr := json.Unmarshal(data, &v); jerr != nil {
				t.Fatalf("accepted %q that encoding/json rejects: %v", data, jerr)
			}
		case errors.Is(err, conda.ErrMalformed), errors.Is(err, conda.ErrTooLarge):
		default:
			t.Fatalf("unclassified error %v for %q", err, data)
		}

		_ = conda.ValidateIndex(bytes.NewReader(data), conda.CompNone, 1<<20)
		_ = conda.ValidateJSONObject(bytes.NewReader(data), 1<<20)
	})
}

// FuzzMerge merges two arbitrary members. Whatever they hold, the result is a
// JSON document that lists each file once, only in the section of its
// extension, and never under a hidden package name.
func FuzzMerge(f *testing.F) {
	seeds := repodataSeeds()
	for i, s := range seeds {
		f.Add([]byte(s), []byte(seeds[(i+1)%len(seeds)]), "a")
	}

	f.Fuzz(func(t *testing.T, one, two []byte, hidden string) {
		b, err := conda.NewBuilder("noarch", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer b.Close()

		opts := conda.MemberOptions{MaxBytes: 1 << 20, Hidden: func(name string) bool { return name == hidden }}

		for _, m := range [][]byte{one, two} {
			if _, err := b.Add(bytes.NewReader(m), opts); err != nil {
				return
			}
		}

		d, err := b.Finish()
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()

		var out bytes.Buffer
		if _, err := d.WriteTo(&out); err != nil || int64(out.Len()) != d.Size() {
			t.Fatalf("write: %v (%d of %d bytes)", err, out.Len(), d.Size())
		}

		var got struct {
			Packages map[string]json.RawMessage `json:"packages"`
			Conda    map[string]json.RawMessage `json:"packages.conda"` //nolint:tagliatelle // conda repodata wire format
		}

		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("merged document is not JSON: %v\n%s", err, out.String())
		}

		for section, files := range map[string]map[string]json.RawMessage{"packages": got.Packages, "packages.conda": got.Conda} {
			for file := range files {
				p, ok := conda.ParseFilename(file)
				if !ok || p.Name == hidden || (section == "packages") != strings.HasSuffix(file, conda.ExtTarBz2) {
					t.Fatalf("%q in %s of %s", file, section, out.String())
				}
			}
		}
	})
}

// FuzzLookup looks arbitrary file names up in arbitrary documents; a hit must
// carry well-formed digests.
func FuzzLookup(f *testing.F) {
	for _, s := range repodataSeeds() {
		f.Add([]byte(s), "a-1.0-0.tar.bz2")
		f.Add([]byte(s), "b-1.0-0.conda")
	}

	f.Fuzz(func(t *testing.T, data []byte, name string) {
		e, err := conda.Lookup(bytes.NewReader(data), name, 1<<20)
		if err != nil {
			return
		}

		if (e.SHA256 != "" && len(e.SHA256) != 64) || (e.MD5 != "" && len(e.MD5) != 32) || e.Size < -1 {
			t.Fatalf("malformed digests %+v for %q", e, name)
		}
	})
}

// FuzzParsePath checks that a parsed path can only name a file directly below
// a plain subdirectory (or the root channeldata.json): no traversal.
func FuzzParsePath(f *testing.F) {
	for _, s := range []string{
		"linux-64/repodata.json", "noarch/current_repodata.json.zst", "channeldata.json", "linux-64/a-1-0.conda",
		"../x", "a/../b/c.conda", "linux-64//repodata.json", "linux-64/repodata.json/", "\x00/repodata.json",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		p, ok := conda.ParsePath(in)
		if !ok {
			return
		}

		key := p.Key()
		if strings.Contains(key, "..") && p.Subdir != "" && (p.Subdir == ".." || p.File == "..") {
			t.Fatalf("%q parsed to traversal %+v", in, p)
		}

		if p.Subdir != "" && (strings.Contains(p.Subdir, "/") || strings.Contains(p.File, "/") || !conda.ValidSubdir(p.Subdir)) {
			t.Fatalf("%q parsed to %+v", in, p)
		}

		if again, ok := conda.ParsePath(key); !ok || again != p {
			t.Fatalf("%q: key %q parses to %+v, %v, not %+v", in, key, again, ok, p)
		}
	})
}
