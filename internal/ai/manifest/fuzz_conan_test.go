// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package manifest

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

const fuzzConanLimit = 4096

var errFuzzConanSibling = errors.New("sibling unavailable")

// fuzzConanClean is what ExtractParts makes of a file: valid UTF-8 without NULs.
func fuzzConanClean(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "")
}

// fuzzConanSource describes a conan recipe whose siblings are the given texts; a
// nil entry is a sibling that does not exist.
func fuzzConanSource(siblings map[string]*string, blob string, broken string, strict bool) Source {
	return Source{
		Format:   "conan",
		Filename: "conanmanifest.txt",
		Open:     func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(blob)), nil },
		Sibling: func(name string) (io.ReadCloser, error) {
			if name == broken {
				return nil, fmt.Errorf("open %s: %w", name, errFuzzConanSibling)
			}

			body := siblings[name]
			if body == nil {
				return nil, nil
			}

			return io.NopCloser(strings.NewReader(*body)), nil
		},
		MemberLimit: fuzzConanLimit,
		Strict:      strict,
	}
}

// fuzzConanWant is the text a sibling or the blob is read as: cut at the member
// limit, or refused when strict, then cleaned.
func fuzzConanWant(s string, strict bool) (string, bool) {
	if len(s) > fuzzConanLimit && strict {
		return "", false
	}

	return fuzzConanClean(s[:min(len(s), fuzzConanLimit)]), true
}

// fuzzConanCheckOK checks a successful read against the siblings that exist.
func fuzzConanCheckOK(t *testing.T, parts []Part, text string, siblings map[string]*string, blob string) {
	t.Helper()

	if len(text) > MaxBytes || !utf8.ValidString(text) || strings.Contains(text, "\x00") || strings.TrimSpace(text) == "" {
		t.Fatalf("Extract returned %d bytes of unclean or blank text", len(text))
	}

	next := 0

	for _, p := range parts {
		if !utf8.ValidString(p.Body) || strings.Contains(p.Body, "\x00") || strings.TrimSpace(p.Body) == "" {
			t.Fatalf("part %q is not clean non-blank text: %q", p.Label, p.Body)
		}

		if !strings.Contains(text, p.Body) {
			t.Fatalf("Extract = %q does not hold the part %q", text, p.Body)
		}

		want := blob

		if len(siblings) > 0 || p.Label != "" {
			sib := siblings[p.Label]
			if sib == nil {
				t.Fatalf("part %q of a sibling that does not exist", p.Label)
			}

			want = *sib

			i := slices.Index([]string{"conanfile.py", "conandata.yml"}, p.Label)
			if i < next {
				t.Fatalf("part %q is unknown or out of order", p.Label)
			}

			next = i + 1
		}

		if got, _ := fuzzConanWant(want, false); p.Body != got {
			t.Fatalf("part %q = %q, want %q", p.Label, p.Body, got)
		}
	}
}

// FuzzExtractConan reads a conan recipe: the recipe files that exist next to the
// manifest, in order and cut at the member limit, or else the blob itself; a
// sibling that cannot be opened fails the read, and so does a long one when strict.
func FuzzExtractConan(f *testing.F) {
	const recipe = "class P(ConanFile):\n    requires = \"zlib/1.2.13\"\n"

	f.Add(recipe, "sources:\n  1.0:\n    url: x\n", "1\n", true, true, "", true)
	f.Add(recipe, "", "1\n", true, false, "", false)
	f.Add("", "data", "1\n", false, true, "", true)
	f.Add("", "", "1\n", false, false, "", true)
	f.Add("", "", "", false, false, "", false)
	f.Add(recipe, "data", "1\n", true, true, "conandata.yml", true)
	f.Add(recipe, "data", "1\n", true, true, "conanfile.py", false)
	f.Add("\x00\xff", "\x00", "\x00", true, true, "", true)
	f.Add(strings.Repeat("x", fuzzConanLimit+1), "", "", true, false, "", true)
	f.Add(strings.Repeat("x", fuzzConanLimit+1), "", "", true, false, "", false)

	f.Fuzz(func(t *testing.T, conanfile, conandata, blob string, hasFile, hasData bool, broken string, strict bool) {
		siblings := map[string]*string{}
		if hasFile {
			siblings["conanfile.py"] = &conanfile
		}

		if hasData {
			siblings["conandata.yml"] = &conandata
		}

		src := fuzzConanSource(siblings, blob, broken, strict)

		parts, perr := ExtractParts(t.Context(), src)
		text, xerr := Extract(t.Context(), src)

		for _, sentinel := range []error{ErrUnsupported, ErrNotFound, ErrTooLarge, ErrAmbiguous, errFuzzConanSibling} {
			if errors.Is(perr, sentinel) != errors.Is(xerr, sentinel) {
				t.Fatalf("ExtractParts = %v, Extract = %v: they disagree on %v", perr, xerr, sentinel)
			}
		}

		if (perr == nil) != (xerr == nil) {
			t.Fatalf("ExtractParts = %v, Extract = %v: only one of them succeeds", perr, xerr)
		}

		if errors.Is(perr, ErrUnsupported) || errors.Is(perr, ErrAmbiguous) {
			t.Fatalf("a conan recipe fails as %v", perr)
		}

		if perr != nil {
			fuzzConanCheckError(t, perr, siblings, blob, broken, strict)

			return
		}

		fuzzConanCheckOK(t, parts, text, siblings, blob)
	})
}

// fuzzConanCheckError checks that a failed read has a reason: the broken
// sibling, a member over the limit in a strict read, or nothing to read.
func fuzzConanCheckError(t *testing.T, err error, siblings map[string]*string, blob, broken string, strict bool) {
	t.Helper()

	switch {
	case errors.Is(err, errFuzzConanSibling):
		if !slices.Contains([]string{"conanfile.py", "conandata.yml"}, broken) {
			t.Fatalf("sibling %q failed but is never opened", broken)
		}
	case errors.Is(err, ErrTooLarge):
		if !strict {
			t.Fatal("a lenient read must cut a long file, not refuse it")
		}
	case errors.Is(err, ErrNotFound):
		texts := []string{blob}
		if len(siblings) > 0 {
			texts = texts[:0]

			for _, body := range siblings {
				texts = append(texts, *body)
			}
		}

		for _, s := range texts {
			if clean, ok := fuzzConanWant(s, strict); ok && strings.TrimSpace(clean) != "" {
				t.Fatalf("no manifest found although a file holds %q", clean)
			}
		}
	default:
		t.Fatalf("unexpected error %v", err)
	}
}
