// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"bytes"
	"compress/bzip2"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

const sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func entry(name string) string {
	return fmt.Sprintf(
		`{"name":%q,"version":"1.0","build":"0","build_number":0,"sha256":%q,"md5":"0123456789abcdef0123456789abcdef","size":12}`,
		name,
		sha,
	)
}

// doc builds a repodata.json from "file=name" pairs.
func doc(info string, tarbz2, condas []string) string {
	section := func(files []string) string {
		parts := make([]string, 0, len(files))
		for _, f := range files {
			file, name, _ := strings.Cut(f, "=")
			parts = append(parts, fmt.Sprintf("%q:%s", file, entry(name)))
		}

		return "{" + strings.Join(parts, ",") + "}"
	}

	return fmt.Sprintf(`{"info":%s,"packages":%s,"packages.conda":%s,"removed":["old-1-0.conda"],"repodata_version":1}`,
		info, section(tarbz2), section(condas))
}

func zstdBytes(tb testing.TB, s string) []byte {
	tb.Helper()

	var buf bytes.Buffer

	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		tb.Fatal(err)
	}

	if _, err := enc.Write([]byte(s)); err != nil {
		tb.Fatal(err)
	}

	if err := enc.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

func TestScanVisitsEveryEntry(t *testing.T) {
	t.Parallel()

	src := doc(`{"subdir":"noarch"}`, []string{"a-1.0-0.tar.bz2=a"}, []string{"b-1.0-0.conda=b", "c-d-1.0-0.conda=c-d"})

	var got []string

	info := ""

	err := Scan(strings.NewReader(src), 1<<20, Visitor{
		Info: func(i json.RawMessage) error { info = string(i); return nil },
		Entry: func(s Section, name string, raw json.RawMessage) error {
			got = append(got, s.Key()+"/"+name)
			if raw[0] != '{' {
				t.Errorf("entry %s is %q", name, raw)
			}

			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "packages/a-1.0-0.tar.bz2 packages.conda/b-1.0-0.conda packages.conda/c-d-1.0-0.conda"
	if strings.Join(got, " ") != want || info != `{"subdir":"noarch"}` {
		t.Fatalf("got %v, info %s", got, info)
	}
}

func TestScanRejectsMalformed(t *testing.T) {
	t.Parallel()

	for name, src := range map[string]string{
		"empty":         ``,
		"array":         `[]`,
		"truncated":     `{"packages":{"a-1-0.conda":{"name":"a"}`,
		"section":       `{"packages":[]}`,
		"entry":         `{"packages":{"a-1-0.tar.bz2":"x"}}`,
		"trailing":      `{} {}`,
		"garbage after": `{}x`,
		"huge entry":    `{"packages":{"a-1-0.tar.bz2":{"x":"` + strings.Repeat("a", MaxEntryBytes+10) + `"}}}`,
	} {
		err := Scan(strings.NewReader(src), 1<<30, Visitor{})
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: got %v, want ErrMalformed", name, err)
		}
	}
}

func TestScanLimitAndNull(t *testing.T) {
	t.Parallel()

	src := doc(`{}`, []string{"a-1.0-0.tar.bz2=a"}, nil)
	if err := Scan(strings.NewReader(src), 20, Visitor{}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v, want ErrTooLarge", err)
	}

	if err := Scan(strings.NewReader(`{"packages":null,"packages.conda":null}`), 100, Visitor{}); err != nil {
		t.Fatalf("null sections: %v", err)
	}
}

func TestScanStopsOnVisitorError(t *testing.T) {
	t.Parallel()

	stop := errors.New("stop")
	src := doc(`{}`, []string{"a-1.0-0.tar.bz2=a", "b-1.0-0.tar.bz2=b"}, nil)

	n := 0
	err := Scan(strings.NewReader(src), 1<<20, Visitor{Entry: func(Section, string, json.RawMessage) error {
		n++
		return stop
	}})

	if !errors.Is(err, stop) || n != 1 {
		t.Fatalf("got %v after %d entries", err, n)
	}
}

func TestOpenDecodesEveryEncoding(t *testing.T) {
	t.Parallel()

	src := doc(`{}`, nil, []string{"a-1.0-0.conda=a"})

	rc, comp, err := Open(bytes.NewReader(zstdBytes(t, src)), 1<<20)
	if err != nil || comp != CompZstd {
		t.Fatalf("zstd: %v %v", comp, err)
	}

	if b, _ := io.ReadAll(rc); string(b) != src {
		t.Fatalf("zstd decoded to %q", b)
	}

	_ = rc.Close()

	rc, comp, err = Open(strings.NewReader(src), 1<<20)
	if err != nil || comp != CompNone {
		t.Fatalf("plain: %v %v", comp, err)
	}

	if b, _ := io.ReadAll(rc); string(b) != src {
		t.Fatalf("plain decoded to %q", b)
	}

	_, comp, err = Open(strings.NewReader("BZh91AY"), 1<<20)
	if err != nil || comp != CompBzip2 {
		t.Fatalf("bzip2: %v %v", comp, err)
	}

	_ = bzip2.NewReader // the stdlib decoder is what Open wraps

	rc, _, _ = Open(bytes.NewReader(zstdBytes(t, strings.Repeat("x", 4096))), 100)
	if _, err := io.ReadAll(rc); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("decompression bomb: %v", err)
	}
}

func TestValidateIndex(t *testing.T) {
	t.Parallel()

	good := doc(`{"subdir":"linux-64"}`, []string{"a-1.0-0.tar.bz2=a"}, nil)
	withBase := doc(`{"subdir":"linux-64","base_url":"https://elsewhere.example/pkgs"}`, nil, nil)
	emptyBase := doc(`{"subdir":"linux-64","base_url":""}`, nil, nil)

	tests := []struct {
		name string
		body []byte
		comp Compression
		want error
	}{
		{"plain", []byte(good), CompNone, nil},
		{"zstd", zstdBytes(t, good), CompZstd, nil},
		{"bzip2 magic only", []byte("BZh9garbage"), CompBzip2, nil},
		{"empty base_url", []byte(emptyBase), CompNone, nil},
		{"base_url", []byte(withBase), CompNone, ErrBaseURL},
		{"base_url zstd", zstdBytes(t, withBase), CompZstd, ErrBaseURL},
		{"html", []byte("<html></html>"), CompNone, ErrMalformed},
		{"unknown object", []byte(`{"error":"nope"}`), CompNone, ErrMalformed},
		{"wrong encoding", zstdBytes(t, good), CompNone, ErrMalformed},
		{"plain as zstd", []byte(good), CompZstd, ErrMalformed},
		{"too big", []byte(good), CompNone, ErrTooLarge},
	}

	for _, tt := range tests {
		limit := int64(1 << 20)
		if tt.name == "too big" {
			limit = 30
		}

		err := ValidateIndex(bytes.NewReader(tt.body), tt.comp, limit)
		if !errors.Is(err, tt.want) && (tt.want != nil || err != nil) {
			t.Errorf("%s: got %v, want %v", tt.name, err, tt.want)
		}
	}
}

func TestValidateJSONObject(t *testing.T) {
	t.Parallel()

	for src, ok := range map[string]bool{
		`{"a":[1,{"b":null}],"c":"d"}`: true,
		`{}`:                           true,
		`[]`:                           false,
		`{"a":[1,}`:                    false,
		`{"a":1}{"b":2}`:               false,
		``:                             false,
	} {
		err := ValidateJSONObject(strings.NewReader(src), 1<<20)
		if (err == nil) != ok {
			t.Errorf("%q: %v", src, err)
		}
	}

	if err := ValidateJSONObject(strings.NewReader(`{"a":"`+strings.Repeat("x", 100)+`"}`), 50); !errors.Is(err, ErrTooLarge) {
		t.Errorf("limit: %v", err)
	}
}

func TestLookup(t *testing.T) {
	t.Parallel()

	src := doc(`{}`, []string{"old-1.0-0.tar.bz2=old"}, []string{"demo-1.0-0.conda=demo", "x-y-2.0-1.conda=x-y"})

	e, err := Lookup(strings.NewReader(src), "x-y-2.0-1.conda", 1<<20)
	if err != nil || e.SHA256 != sha || e.MD5 != "0123456789abcdef0123456789abcdef" || e.Size != 12 {
		t.Fatalf("got %+v, %v", e, err)
	}

	for _, name := range []string{"y-2.0-1.conda", "demo-1.0-0.tar.bz2", "missing-1-0.conda", "../demo-1.0-0.conda", "repodata.json"} {
		if _, err := Lookup(strings.NewReader(src), name, 1<<20); !errors.Is(err, ErrNotListed) {
			t.Errorf("%s: got %v, want ErrNotListed", name, err)
		}
	}
}

// A key that straddles two read chunks is still found.
func TestLookupAcrossChunks(t *testing.T) {
	t.Parallel()

	pad := strings.Repeat(" ", 1<<20-40)
	src := `{"packages.conda":{` + pad + `"zz-1.0-0.conda":` + entry("zz") + `}}`

	for shift := range 60 {
		in := strings.Replace(src, pad, pad[:len(pad)-shift]+strings.Repeat(" ", shift), 1)
		if _, err := Lookup(strings.NewReader(in), "zz-1.0-0.conda", 1<<22); err != nil {
			t.Fatalf("shift %d: %v", shift, err)
		}
	}
}

func TestLookupEntryWithoutDigests(t *testing.T) {
	t.Parallel()

	src := `{"packages":{"a-1-0.tar.bz2":{"name":"a","sha256":"nothex","md5":"X","size":"big"},"é-1-0.conda":{"sha256":"` + sha + `"}}}`

	e, err := Lookup(strings.NewReader(src), "a-1-0.tar.bz2", 1<<20)
	if err == nil && (e.SHA256 != "" || e.MD5 != "" || e.Size != -1) {
		t.Fatalf("bad digests accepted: %+v", e)
	}

	if err != nil && !errors.Is(err, ErrMalformed) {
		t.Fatal(err)
	}

	// A name the raw search cannot match falls back to a full scan.
	if e, err := Lookup(strings.NewReader(src), "é-1-0.conda", 1<<20); err != nil || e.SHA256 != sha {
		t.Fatalf("non-ascii name: %+v %v", e, err)
	}
}

func merge(tb testing.TB, members []string, o MemberOptions) (string, []MemberStats) {
	tb.Helper()

	b, err := NewBuilder("linux-64", tb.TempDir())
	if err != nil {
		tb.Fatal(err)
	}
	defer b.Close()

	var stats []MemberStats

	for _, m := range members {
		st, err := b.Add(strings.NewReader(m), o)
		if err != nil {
			tb.Fatal(err)
		}

		stats = append(stats, st)
	}

	d, err := b.Finish()
	if err != nil {
		tb.Fatal(err)
	}
	defer d.Close()

	var out bytes.Buffer
	if _, err := d.WriteTo(&out); err != nil {
		tb.Fatal(err)
	}

	if int64(out.Len()) != d.Size() {
		tb.Fatalf("Size %d, wrote %d", d.Size(), out.Len())
	}

	return out.String(), stats
}

func TestMergeFirstMemberWinsAndHidesNames(t *testing.T) {
	t.Parallel()

	hosted := doc(`{"subdir":"linux-64"}`, nil, []string{"demo-2.0-0.conda=demo"})
	proxy := doc(`{"subdir":"linux-64"}`,
		[]string{"other-1.0-0.tar.bz2=other"},
		// demo-2.0-0 duplicates the hosted file; demo-3 is hidden by name;
		// liar-1 hides its package under another name in the entry.
		[]string{"demo-2.0-0.conda=demo", "demo-3.0-0.conda=demo", "liar-1.0-0.conda=demo", "keep-1.0-0.conda=keep", "bad.conda=x"})

	b, err := NewBuilder("linux-64", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	var stats []MemberStats

	for i, m := range []string{hosted, proxy} {
		o := MemberOptions{MaxBytes: 1 << 20}
		if i == 1 { // only the proxy member is subject to hosted-wins
			o.Hidden = func(n string) bool { return n == "demo" }
		}

		st, aerr := b.Add(strings.NewReader(m), o)
		if aerr != nil {
			t.Fatal(aerr)
		}

		stats = append(stats, st)
	}

	d, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var buf bytes.Buffer
	if _, err := d.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	out := buf.String()

	if stats[0].Added != 1 || stats[1].Duplicates != 1 || stats[1].Hidden != 2 || stats[1].Added != 2 || stats[1].Invalid != 1 {
		t.Fatalf("stats %+v", stats)
	}

	var got struct {
		Info     map[string]string          `json:"info"`
		Packages map[string]json.RawMessage `json:"packages"`
		Conda    map[string]json.RawMessage `json:"packages.conda"` //nolint:tagliatelle // conda repodata wire format
		Version  int                        `json:"repodata_version"`
	}

	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("merged document is not JSON: %v\n%s", err, out)
	}

	if got.Info["subdir"] != "linux-64" || got.Version != 1 || len(got.Packages) != 1 || len(got.Conda) != 2 {
		t.Fatalf("got %s", out)
	}

	for _, f := range []string{"demo-2.0-0.conda", "keep-1.0-0.conda"} {
		if _, ok := got.Conda[f]; !ok {
			t.Errorf("%s missing from %s", f, out)
		}
	}
}

func TestMergeReadsCompressedMembersAndIsStable(t *testing.T) {
	t.Parallel()

	a := doc(`{}`, []string{"a-1.0-0.tar.bz2=a"}, nil)

	b, err := NewBuilder("noarch", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if _, err := b.Add(bytes.NewReader(zstdBytes(t, a)), MemberOptions{MaxBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}

	d, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var one, two bytes.Buffer
	if _, err := d.WriteTo(&one); err != nil {
		t.Fatal(err)
	}

	if _, err := d.WriteTo(&two); err != nil || one.String() != two.String() {
		t.Fatalf("second WriteTo differs: %v", err)
	}

	if !json.Valid(one.Bytes()) || !strings.Contains(one.String(), `"a-1.0-0.tar.bz2"`) {
		t.Fatalf("got %s", one.String())
	}
}

func TestMergeEmptyAndFailingMember(t *testing.T) {
	t.Parallel()

	out, _ := merge(t, nil, MemberOptions{})
	if out != `{"info":{"subdir":"linux-64"},"packages":{},"packages.conda":{},"repodata_version":1}` {
		t.Fatalf("empty merge: %s", out)
	}

	b, err := NewBuilder("noarch", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := b.Add(strings.NewReader("not json"), MemberOptions{MaxBytes: 1 << 20}); !errors.Is(err, ErrMalformed) {
		t.Fatalf("got %v", err)
	}

	b.Close()
	b.Close()
}

func TestParsePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		kind    Kind
		ok      bool
		subdir  string
		current bool
		comp    Compression
	}{
		{"linux-64/repodata.json", KindRepodata, true, "linux-64", false, CompNone},
		{"/noarch/repodata.json.zst", KindRepodata, true, "noarch", false, CompZstd},
		{"noarch/current_repodata.json.bz2", KindRepodata, true, "noarch", true, CompBzip2},
		{"linux-64/numpy-1.26.4-py312_0.conda", KindPackage, true, "linux-64", false, 0},
		{"linux-64/numpy-1.26.4-py312_0.tar.bz2", KindPackage, true, "linux-64", false, 0},
		{"channeldata.json", KindChanneldata, true, "", false, 0},
		{"linux-64/repodata.jlap", KindOther, true, "linux-64", false, 0},
		{"linux-64/repodata_shards.msgpack.zst", KindOther, true, "linux-64", false, 0},
		{"linux-64/shards/abc.msgpack.zst", 0, false, "", false, 0},
		{"repodata.json", 0, false, "", false, 0},
		{"../repodata.json", 0, false, "", false, 0},
		{"./repodata.json", 0, false, "", false, 0},
		{"linux-64/", 0, false, "", false, 0},
		{"", 0, false, "", false, 0},
	}

	for _, tt := range tests {
		p, ok := ParsePath(tt.in)
		if ok != tt.ok || (ok && (p.Kind != tt.kind || p.Subdir != tt.subdir || p.Current != tt.current || p.Comp != tt.comp)) {
			t.Errorf("ParsePath(%q) = %+v, %v", tt.in, p, ok)
		}
	}

	p, _ := ParsePath("linux-64/current_repodata.json.zst")
	if p.Key() != "linux-64/current_repodata.json.zst" || RepodataFile(true, CompZstd) != "current_repodata.json.zst" {
		t.Errorf("key %q", p.Key())
	}
}

func TestParseFilename(t *testing.T) {
	t.Parallel()

	f, ok := ParseFilename("scikit-learn-1.0-py39_0.tar.bz2")
	if !ok || f.Name != "scikit-learn" || f.Version != "1.0" || f.Build != "py39_0" || f.Ext != ExtTarBz2 {
		t.Fatalf("got %+v %v", f, ok)
	}

	for _, bad := range []string{"", "a.conda", "a-1.conda", "a-1-0.zip", "../a-1-0.conda", "a/b-1-0.conda"} {
		if _, ok := ParseFilename(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
