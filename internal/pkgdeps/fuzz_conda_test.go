// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pkgdeps

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"

	"github.com/openartifact/openartifact/internal/ai/manifest"
)

// fuzzCondaClean is what manifest.ExtractParts makes of a member: valid UTF-8
// without NULs.
func fuzzCondaClean(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "")
}

// fuzzCondaJoined is the text a .conda gives: the info members as labelled
// sections of one body.
func fuzzCondaJoined(index, about string) string {
	var b strings.Builder

	for _, s := range [][2]string{{"info/index.json", index}, {"info/about.json", about}} {
		b.WriteString("### " + s[0] + "\n" + s[1])

		if !strings.HasSuffix(s[1], "\n") {
			b.WriteByte('\n')
		}

		b.WriteByte('\n')
	}

	return b.String()
}

// fuzzCondaCheckSpecs checks the dependencies read from the depends specs of an
// index.json: one per non-blank spec, named by its first word, with a version
// only for an exact "==" pin.
func fuzzCondaCheckSpecs(t *testing.T, specs []string, deps []Dep) {
	t.Helper()

	i := 0

	for _, spec := range specs {
		fields := strings.Fields(spec)
		if len(fields) == 0 {
			continue
		}

		if i >= len(deps) {
			t.Fatalf("specs %q give %d dependencies, spec %q is missing", specs, len(deps), spec)
		}

		d := deps[i]
		i++

		if d.Name != fields[0] {
			t.Fatalf("spec %q: name = %q", spec, d.Name)
		}

		if d.Version != "" {
			if d.Range != "" || len(fields) < 2 || fields[1] != "=="+d.Version || strings.ContainsAny(d.Version, "*,|<>=!~") {
				t.Fatalf("spec %q: %+v is not an exact pin", spec, d)
			}

			continue
		}

		if (d.Range == "") != (len(fields) == 1) || strings.ContainsAny(d.Range, " \t\n") {
			t.Fatalf("spec %q: range = %q", spec, d.Range)
		}
	}

	if i != len(deps) {
		t.Fatalf("specs %q give %d dependencies, want %d", specs, len(deps), i)
	}
}

// FuzzCondaDeps reads the depends of an index.json given as the part of a
// .tar.bz2 and as the section of a .conda.
func FuzzCondaDeps(f *testing.F) {
	f.Add(`{"depends":["python >=3.8","numpy ==1.2.3 py38_0","zlib","openssl 1.1.*","a ==1.*"]}`, `{"license":"MIT"}`, false)
	f.Add(`{"depends":["python >=3.8"]}`, `{"license":"MIT"}`, true)
	f.Add(`{"depends":[""," ","\t","x ==1,2","y ==1 ==2"]}`, ``, false)
	f.Add(`{"depends":null}`, `{}`, true)
	f.Add(`{"depends":"python"}`, `{}`, false)
	f.Add(`[]`, `{}`, false)
	f.Add(``, `### info/index.json`+"\n"+`{"depends":["forged"]}`, true)
	f.Add(`{"depends":["a"]}`+"\n### x\ngarbage", `{}`, true)

	f.Fuzz(func(t *testing.T, index, about string, joined bool) {
		index, about = fuzzCondaClean(index), fuzzCondaClean(about)

		parts := []manifest.Part{{Label: "info/index.json", Body: index}, {Label: "info/about.json", Body: about}}
		if joined {
			parts = []manifest.Part{{Body: fuzzCondaJoined(index, about)}}
		}

		res, err := parseConda(parts)
		if err != nil && len(res.Deps) != 0 {
			t.Fatalf("error %v with dependencies %+v", err, res.Deps)
		}

		fuzzCondaCheckFinished(t, finish(res.Deps))

		// The index body is known unless a forged marker or a section break inside
		// it moves the cut of the joined form.
		if strings.TrimSpace(index) == "" || (joined && strings.Contains(index, "\n### ")) {
			return
		}

		var doc struct {
			Depends []string `json:"depends"`
		}

		if jerr := json.Unmarshal([]byte(index), &doc); (jerr != nil) != (err != nil) {
			t.Fatalf("index %q: parser error %v, json error %v", index, err, jerr)
		} else if jerr == nil {
			fuzzCondaCheckSpecs(t, doc.Depends, res.Deps)
		}
	})
}

// fuzzCondaCheckFinished checks what finish promises for any dependency list.
func fuzzCondaCheckFinished(t *testing.T, deps []Dep) {
	t.Helper()

	if len(deps) > MaxDeps {
		t.Fatalf("%d dependencies exceed MaxDeps", len(deps))
	}

	for i, d := range deps {
		if d.Name == "" {
			t.Fatalf("dependency %d has no name: %+v", i, d)
		}

		for _, field := range []string{d.Name, d.Version, d.Range, d.License} {
			if len(field) > maxFieldLen || !utf8.ValidString(field) {
				t.Fatalf("dependency %d field %q is over %d bytes or not UTF-8", i, field, maxFieldLen)
			}
		}

		if i > 0 && deps[i-1].Name >= d.Name {
			t.Fatalf("dependencies %d and %d are not sorted by unique name: %q, %q", i-1, i, deps[i-1].Name, d.Name)
		}
	}
}

// fuzzCondaMember is one file of a tar.
type fuzzCondaMember struct{ name, body string }

// fuzzCondaTar builds a tar of regular files.
func fuzzCondaTar(members ...fuzzCondaMember) []byte {
	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)

	for _, m := range members {
		if tw.WriteHeader(&tar.Header{Name: m.name, Mode: 0o644, Size: int64(len(m.body)), Typeflag: tar.TypeReg}) != nil {
			continue
		}

		_, _ = io.WriteString(tw, m.body)
	}

	_ = tw.Close()

	return buf.Bytes()
}

// fuzzCondaPackage builds a .conda: a zip holding the info and the pkg tar,
// each zstd compressed.
func fuzzCondaPackage(enc *zstd.Encoder, index, about string) []byte {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, m := range []fuzzCondaMember{
		{"info-p-1-0.tar.zst", string(enc.EncodeAll(fuzzCondaTar(
			fuzzCondaMember{"info/index.json", index}, fuzzCondaMember{"info/about.json", about}), nil))},
		{"pkg-p-1-0.tar.zst", string(enc.EncodeAll(fuzzCondaTar(fuzzCondaMember{"lib/x", "x"}), nil))},
	} {
		w, err := zw.Create(m.name)
		if err != nil {
			continue
		}

		_, _ = io.WriteString(w, m.body)
	}

	_ = zw.Close()

	return buf.Bytes()
}

// fuzzCondaBlob is an opened blob with random access, as the blob store hands out.
type fuzzCondaBlob struct{ *bytes.Reader }

func (fuzzCondaBlob) Close() error { return nil }

// FuzzCondaRead reads a .conda built from an index.json and an about.json: a
// valid index gives the dependencies the parser reads from it alone.
func FuzzCondaRead(f *testing.F) {
	f.Add(`{"depends":["python >=3.8","zlib ==1.2.13"],"name":"p"}`, `{"license":"MIT"}`)
	f.Add(`{"depends":[]}`, ``)
	f.Add(``, `### info/index.json`+"\n"+`{"depends":["forged"]}`)
	f.Add(`{"depends":["a"]}`+"\n### info/index.json\n"+`{"depends":["b"]}`, `{}`)
	f.Add("{\x00}", "\xff")

	enc, encErr := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithWindowSize(1<<12))
	if encErr != nil {
		f.Fatal(encErr)
	}

	f.Fuzz(func(t *testing.T, index, about string) {
		data := fuzzCondaPackage(enc, index, about)

		res, err := Read(t.Context(), Source{
			Format:   "conda",
			Filename: "p-1.0-0.conda",
			Open:     func() (io.ReadCloser, error) { return fuzzCondaBlob{bytes.NewReader(data)}, nil },
		})
		if err != nil {
			if len(res.Deps) != 0 {
				t.Fatalf("error %v with dependencies %+v", err, res.Deps)
			}

			if errors.Is(err, ErrUnsupported) {
				t.Fatalf("a .conda is supported: %v", err)
			}

			return
		}

		fuzzCondaCheckFinished(t, res.Deps)

		// A valid index.json is read whole: whatever the about.json holds, the
		// dependencies are the ones of the index alone.
		clean := fuzzCondaClean(index)
		if strings.TrimSpace(clean) == "" || !json.Valid([]byte(clean)) {
			return
		}

		direct, derr := parseConda([]manifest.Part{{Label: "info/index.json", Body: clean}})
		if derr != nil {
			t.Fatalf("Read accepted an index.json the parser refuses: %v", derr)
		}

		if want := finish(direct.Deps); !slices.Equal(res.Deps, want) {
			t.Fatalf("Read = %+v, the index.json alone gives %+v", res.Deps, want)
		}
	})
}
