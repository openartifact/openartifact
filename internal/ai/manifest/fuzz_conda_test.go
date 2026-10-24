// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package manifest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
)

const fuzzCondaLimit = 4096

// fuzzCondaMember is one regular file of a tar.
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

// fuzzCondaBlob is an opened blob with random access, as the blob store hands out.
type fuzzCondaBlob struct{ *bytes.Reader }

func (fuzzCondaBlob) Close() error { return nil }

// fuzzCondaPackage builds a .conda: a zip holding the info and the pkg tar,
// each zstd compressed.
func fuzzCondaPackage(enc *zstd.Encoder, info []byte) []byte {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, m := range []fuzzCondaMember{
		{"info-p-1-0.tar.zst", string(enc.EncodeAll(info, nil))},
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

// fuzzCondaCheck reads a .conda both ways: the info members come as one
// unlabelled part of labelled sections, clean and bounded, the two reads agree,
// and the read is never ambiguous.
func fuzzCondaCheck(t *testing.T, src Source) {
	t.Helper()

	parts, perr := ExtractParts(t.Context(), src)
	text, xerr := Extract(t.Context(), src)

	for _, sentinel := range []error{ErrUnsupported, ErrNotFound, ErrTooLarge, ErrAmbiguous} {
		if errors.Is(perr, sentinel) != errors.Is(xerr, sentinel) {
			t.Fatalf("ExtractParts = %v, Extract = %v: they disagree on %v", perr, xerr, sentinel)
		}
	}

	if (perr == nil) != (xerr == nil) {
		t.Fatalf("ExtractParts = %v, Extract = %v: only one of them succeeds", perr, xerr)
	}

	if errors.Is(perr, ErrUnsupported) || errors.Is(perr, ErrAmbiguous) {
		t.Fatalf("read of %q failed as %v", src.Filename, perr)
	}

	if perr != nil {
		return
	}

	if len(parts) != 1 || parts[0].Label != "" || !strings.HasPrefix(parts[0].Body, "### info/") {
		t.Fatalf("parts = %q, want one unlabelled part of info sections", parts)
	}

	if body := parts[0].Body; !utf8.ValidString(body) || strings.Contains(body, "\x00") {
		t.Fatalf("part is not clean text: %q", body)
	}

	if len(text) > MaxBytes || !utf8.ValidString(text) || strings.Contains(text, "\x00") || text != parts[0].Body {
		t.Fatalf("Extract = %q, want the part %q", text, parts[0].Body)
	}
}

// FuzzExtractConda reads a .conda built from an index.json, an about.json and
// one more info member under any name.
func FuzzExtractConda(f *testing.F) {
	f.Add(`{"name":"p","depends":["python"]}`, `{"license":"MIT"}`, "", true)
	f.Add(`{"name":"p"}`, ``, "info/index.json", true)
	f.Add(`{"name":"p"}`, `{}`, "./info/about.json", false)
	f.Add(`{"name":"p"}`, `{}`, "info/../info/index.json", true)
	f.Add(`{"name":"p"}`, `{}`, `info\index.json`, false)
	f.Add("\x00", "\xff", "", true)
	f.Add(strings.Repeat("x", fuzzCondaLimit+1), `{}`, "", true)
	f.Add(strings.Repeat("x", fuzzCondaLimit+1), `{}`, "", false)
	f.Add("", "", "", true)

	enc, encErr := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithWindowSize(1<<12))
	if encErr != nil {
		f.Fatal(encErr)
	}

	f.Fuzz(func(t *testing.T, index, about, extra string, strict bool) {
		members := []fuzzCondaMember{{"info/index.json", index}, {"info/about.json", about}}
		if extra != "" {
			members = append(members, fuzzCondaMember{extra, "x"})
		}

		data := fuzzCondaPackage(enc, fuzzCondaTar(members...))

		fuzzCondaCheck(t, Source{
			Format:      "conda",
			Filename:    "p-1.0-0.conda",
			Open:        func() (io.ReadCloser, error) { return fuzzCondaBlob{bytes.NewReader(data)}, nil },
			MemberLimit: fuzzCondaLimit,
			Strict:      strict,
		})
	})
}

// fuzzCondaFirst reads a tar the way the info slots mean to: the first regular
// file of each name, whole (up to n+1 bytes), until the stream breaks.
func fuzzCondaFirst(data []byte, n int64) map[string]string {
	first := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(data))

	for {
		hdr, err := tr.Next()
		if err != nil || hdr.Typeflag != tar.TypeReg {
			if err != nil {
				return first
			}

			continue
		}

		name := cleanName(hdr.Name)
		if name != "info/index.json" && name != "info/about.json" {
			continue
		}

		if _, seen := first[name]; seen {
			continue
		}

		body, err := io.ReadAll(io.LimitReader(tr, n+1))
		if err != nil {
			return first
		}

		first[name] = string(body)
	}
}

// FuzzCondaInfoTar walks any bytes taken for the info tar of a package with the
// member limit and strictness of the caller: the sections found are the first
// info/index.json and info/about.json of the stream, whole up to the limit, and
// a strict read refuses a longer one rather than cutting it.
func FuzzCondaInfoTar(f *testing.F) {
	both := fuzzCondaTar(fuzzCondaMember{"info/index.json", `{"name":"p"}`}, fuzzCondaMember{"info/about.json", `{"license":"MIT"}`})
	twice := fuzzCondaTar(fuzzCondaMember{"info/index.json", "first"}, fuzzCondaMember{"./info/index.json", "second"}, fuzzCondaMember{"info/about.json", "about"})

	f.Add(both, uint16(fuzzCondaLimit), false)
	f.Add(both, uint16(4), true)
	f.Add(both, uint16(4), false)
	f.Add(twice, uint16(fuzzCondaLimit), true)
	f.Add(fuzzCondaTar(fuzzCondaMember{"/abs/info/index.json", "x"}, fuzzCondaMember{"../info/about.json", "x"}), uint16(100), false)
	f.Add(both[:len(both)-700], uint16(fuzzCondaLimit), false)
	f.Add([]byte{}, uint16(0), true)

	f.Fuzz(func(t *testing.T, data []byte, limit uint16, strict bool) {
		if len(data) > 16<<10 {
			return
		}

		n := int64(limit) + 1
		secs, err := collectTar(t.Context(), bytes.NewReader(data), condaInfoSlots(), limits{n: n, strict: strict})

		switch {
		case errors.Is(err, ErrAmbiguous):
			t.Fatal("no info slot is unique")
		case errors.Is(err, ErrTooLarge):
			if !strict {
				t.Fatal("a lenient read must cut a long member, not refuse it")
			}
		case err != nil:
			return
		}

		first := fuzzCondaFirst(data, n)

		if err == nil {
			fuzzCondaCheckSections(t, secs, first, n, strict)
		}
	})
}

func fuzzCondaCheckSections(t *testing.T, secs []section, first map[string]string, n int64, strict bool) {
	t.Helper()

	if len(secs) > 2 {
		t.Fatalf("%d sections from two slots", len(secs))
	}

	rank := map[string]int{"info/index.json": 0, "info/about.json": 1}
	last := -1

	for _, sec := range secs {
		r, known := rank[sec.label]
		if !known || r <= last {
			t.Fatalf("sections out of slot order or repeated: %q after rank %d", sec.label, last)
		}

		last = r

		whole, ok := first[sec.label]
		if !ok {
			t.Fatalf("section %q is in no member of the stream", sec.label)
		}

		if int64(len(sec.body)) > n {
			t.Fatalf("section %q is %d bytes, over the limit %d", sec.label, len(sec.body), n)
		}

		if int64(len(whole)) > n {
			if strict {
				t.Fatalf("strict read kept %q although its member is over the limit", sec.label)
			}

			whole = whole[:n]
		}

		if sec.body != whole {
			t.Fatalf("section %q = %q, the first member holds %q", sec.label, sec.body, whole)
		}
	}
}
