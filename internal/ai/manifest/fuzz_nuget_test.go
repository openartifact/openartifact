// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package manifest

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

const fuzzNuGetLimit = 4096

// fuzzNuGetClean is what ExtractParts makes of a member: valid UTF-8 without NULs.
func fuzzNuGetClean(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "")
}

// fuzzNuGetZip builds a zip of (name, body) members.
func fuzzNuGetZip(members [][2]string) []byte {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, m := range members {
		w, err := zw.Create(m[0])
		if err != nil {
			continue
		}

		_, _ = io.WriteString(w, m[1])
	}

	_ = zw.Close()

	return buf.Bytes()
}

// fuzzNuGetBlob is an opened blob with random access, as the blob store hands out.
type fuzzNuGetBlob struct{ *bytes.Reader }

func (fuzzNuGetBlob) Close() error { return nil }

func fuzzNuGetSource(data []byte, strict bool) Source {
	return Source{
		Format:      "nuget",
		Filename:    "p.1.0.0.nupkg",
		Open:        func() (io.ReadCloser, error) { return fuzzNuGetBlob{bytes.NewReader(data)}, nil },
		MemberLimit: fuzzNuGetLimit,
		Strict:      strict,
	}
}

// fuzzNuGetContract reads src both ways and checks what every caller relies on:
// the two reads agree on success and on the kind of failure, parts are clean
// non-blank text under an expected label, and the text holds them.
func fuzzNuGetContract(t *testing.T, src Source, labels ...string) ([]Part, error) {
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

	if errors.Is(perr, ErrUnsupported) || (errors.Is(perr, ErrAmbiguous) && !src.Strict) {
		t.Fatalf("read of %q (strict %v) failed as %v", src.Filename, src.Strict, perr)
	}

	if perr != nil {
		return nil, perr
	}

	if len(text) > MaxBytes || !utf8.ValidString(text) || strings.Contains(text, "\x00") || strings.TrimSpace(text) == "" {
		t.Fatalf("Extract returned %d bytes of unclean or blank text", len(text))
	}

	if len(parts) == 0 {
		t.Fatal("ExtractParts succeeded without a part")
	}

	for _, p := range parts {
		if !slices.Contains(labels, p.Label) {
			t.Fatalf("unexpected part label %q", p.Label)
		}

		if !utf8.ValidString(p.Body) || strings.Contains(p.Body, "\x00") || strings.TrimSpace(p.Body) == "" {
			t.Fatalf("part %q is not clean non-blank text: %q", p.Label, p.Body)
		}

		// The member limit, with every invalid byte possibly widened to U+FFFD.
		if len(p.Body) > 3*fuzzNuGetLimit {
			t.Fatalf("part %q is %d bytes, over the member limit", p.Label, len(p.Body))
		}

		if !strings.Contains(text, p.Body) {
			t.Fatalf("Extract = %q does not hold the part %q", text, p.Body)
		}
	}

	return parts, nil
}

// FuzzExtractNuGet reads the .nuspec of any bytes taken for a .nupkg.
func FuzzExtractNuGet(f *testing.F) {
	const spec = "<package><metadata><id>a</id><version>1.0.0</version></metadata></package>"

	f.Add(fuzzNuGetZip([][2]string{{"a.nuspec", spec}}), false)
	f.Add(fuzzNuGetZip([][2]string{{"a.nuspec", spec}, {"lib/net8.0/a.dll", "x"}}), true)
	f.Add(fuzzNuGetZip([][2]string{{"a.nuspec", spec}, {"./b.nuspec", spec}}), true)
	f.Add(fuzzNuGetZip([][2]string{{"a.nuspec", spec}, {"b.NUSPEC", spec}}), false)
	f.Add(fuzzNuGetZip([][2]string{{"lib/a.nuspec", spec}}), false)
	f.Add(fuzzNuGetZip([][2]string{{"a.nuspec", strings.Repeat("x", fuzzNuGetLimit+1)}}), true)
	f.Add([]byte("PK\x03\x04"), false)
	f.Add([]byte{}, true)

	f.Fuzz(func(t *testing.T, data []byte, strict bool) {
		if len(data) > 16<<10 {
			return
		}

		parts, err := fuzzNuGetContract(t, fuzzNuGetSource(data, strict), ".nuspec")
		if err != nil {
			return
		}

		if len(parts) != 1 {
			t.Fatalf("%d .nuspec parts, a package has one", len(parts))
		}
	})
}

// FuzzExtractNuGetMembers builds a .nupkg of a nuspec and one more member under
// any name: unless that name could be a second nuspec, the nuspec is read as it
// is, up to the member limit, and the archive is never ambiguous.
func FuzzExtractNuGetMembers(f *testing.F) {
	const spec = "<package><metadata><id>a</id></metadata></package>"

	f.Add(spec, "lib/a.dll", true)
	f.Add(spec, "b.nuspec", true)
	f.Add(spec, "b.nuspec", false)
	f.Add(spec, "./b.nuspec", true)
	f.Add(spec, "dir/../b.nuspec", true)
	f.Add(spec, `x\y.nuspec`, true)
	f.Add(spec, ".nuspec", true)
	f.Add(spec, "p.nuspec", true)
	f.Add(spec, "", true)
	f.Add("<a>\x00\xff</a>", "", false)
	f.Add(strings.Repeat("x", fuzzNuGetLimit+1), "", true)
	f.Add(strings.Repeat("x", fuzzNuGetLimit+1), "", false)
	f.Add("", "", true)

	f.Fuzz(func(t *testing.T, nuspec, extra string, strict bool) {
		members := [][2]string{{"p.nuspec", nuspec}}
		if extra != "" {
			members = append(members, [2]string{extra, "x"})
		}

		parts, err := fuzzNuGetContract(t, fuzzNuGetSource(fuzzNuGetZip(members), strict), ".nuspec")

		if strings.Contains(strings.ToLower(extra), "nuspec") {
			return
		}

		want := fuzzNuGetClean(nuspec[:min(len(nuspec), fuzzNuGetLimit)])

		switch {
		case len(nuspec) > fuzzNuGetLimit && strict:
			if !errors.Is(err, ErrTooLarge) {
				t.Fatalf("a nuspec over the limit, strict: err = %v, want ErrTooLarge", err)
			}
		case strings.TrimSpace(want) == "":
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("a blank nuspec: err = %v, want ErrNotFound", err)
			}
		case err != nil:
			t.Fatalf("the only nuspec of the package is refused: %v", err)
		case len(parts) != 1 || parts[0].Body != want:
			t.Fatalf("parts = %q, want the nuspec %q", parts, want)
		}
	})
}
