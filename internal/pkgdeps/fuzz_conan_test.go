// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pkgdeps

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openartifact/openartifact/internal/ai/manifest"
)

// fuzzConanClean is what manifest.ExtractParts makes of a file: valid UTF-8
// without NULs.
func fuzzConanClean(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "")
}

// fuzzConanNameChar reports whether c may follow the first character of a conan
// package name.
func fuzzConanNameChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '+' || c == '-'
}

func fuzzConanValidName(name string) bool {
	if name == "" || name[0] == '.' || name[0] == '+' || name[0] == '-' {
		return false
	}

	for i := range len(name) {
		if !fuzzConanNameChar(name[i]) {
			return false
		}
	}

	return true
}

// fuzzConanRequiresLine reports whether the recipe has a line the parser reads
// that quotes ref.
func fuzzConanRequiresLine(recipe, ref string) bool {
	for line := range strings.SplitSeq(recipe, "\n") {
		if !strings.Contains(line, "requires") || strings.Contains(line, "tool_requires") ||
			strings.Contains(line, "build_requires") || strings.Contains(line, "test_requires") {
			continue
		}

		if strings.Contains(line, `"`+ref) || strings.Contains(line, "'"+ref) {
			return true
		}
	}

	return false
}

// fuzzConanCheck checks a dependency parseConan read: a package name with
// either an exact version or a bracketed range, quoted on a requires line.
func fuzzConanCheck(t *testing.T, recipe string, d Dep) {
	t.Helper()

	if !fuzzConanValidName(d.Name) {
		t.Fatalf("name %q is not a conan package name", d.Name)
	}

	switch {
	case d.Version != "" && d.Range == "":
		if strings.ContainsAny(d.Version, "\"'@#/[ \t\r\n\f") {
			t.Fatalf("version %q of %q holds a separator", d.Version, d.Name)
		}
	case d.Version == "" && strings.HasPrefix(d.Range, "[") && strings.HasSuffix(d.Range, "]"):
		if strings.ContainsAny(d.Range, "\"'") {
			t.Fatalf("range %q of %q holds a quote", d.Range, d.Name)
		}
	default:
		t.Fatalf("dependency %+v has neither an exact version nor a range", d)
	}

	if !fuzzConanRequiresLine(recipe, d.Name+"/"+d.Version+d.Range) {
		t.Fatalf("%+v is not quoted on a requires line of the recipe", d)
	}
}

// fuzzConanCheckFinished checks what finish promises for any dependency list.
func fuzzConanCheckFinished(t *testing.T, deps []Dep) {
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

// FuzzConanDeps reads the requires of any recipe text; only conanfile.py is a
// recipe, any other part is ignored.
func FuzzConanDeps(f *testing.F) {
	f.Add("class P(ConanFile):\n    requires = \"zlib/1.2.13\", \"openssl/[>=1.1 <4]@user/stable\"\n", true)
	f.Add("    def requirements(self):\n        self.requires('fmt/10.1.1#abc123')\n        self.tool_requires('cmake/3.27')\n", true)
	f.Add("requires = 'a/1.0'\ntest_requires = 'b/1.0'\nbuild_requires = 'c/1.0'\n", true)
	f.Add("requires = 'a/1.0'", false)
	f.Add("requires = \"a/\", \"/1\", \"a/b/c\", \"a/[]\", \"a/[1\", \"_x/1.0\", \".x/1.0\"", true)
	f.Add("", true)

	f.Fuzz(func(t *testing.T, recipe string, isRecipe bool) {
		recipe = fuzzConanClean(recipe)

		label := "conandata.yml"
		if isRecipe {
			label = "conanfile.py"
		}

		res := parseConan([]manifest.Part{{Label: label, Body: recipe}})

		if !isRecipe && len(res.Deps) != 0 {
			t.Fatalf("%d dependencies read from a conandata.yml", len(res.Deps))
		}

		for _, d := range res.Deps {
			fuzzConanCheck(t, recipe, d)
		}

		fuzzConanCheckFinished(t, finish(res.Deps))
	})
}

// fuzzConanBlob is an opened blob as the blob store hands out.
type fuzzConanBlob struct{ io.Reader }

func (fuzzConanBlob) Close() error { return nil }

// FuzzConanRead reads a recipe through its sibling files, as the listing points
// at the manifest and the recipe lies next to it: the conanfile.py gives the
// requires, the conandata.yml and the blob itself none.
func FuzzConanRead(f *testing.F) {
	const recipe = "class P(ConanFile):\n    requires = \"zlib/1.2.13\"\n"

	f.Add(recipe, "sources:\n  1.0:\n    url: x\n", "1\n")
	f.Add(recipe, "", "")
	f.Add("", "requires = 'a/1.0'", "requires = 'b/1.0'")
	f.Add("\x00\xff", "", "")

	f.Fuzz(func(t *testing.T, conanfile, conandata, blob string) {
		siblings := map[string]string{"conanfile.py": conanfile, "conandata.yml": conandata}

		res, err := Read(t.Context(), Source{
			Format:   "conan",
			Filename: "conanmanifest.txt",
			Open:     func() (io.ReadCloser, error) { return fuzzConanBlob{strings.NewReader(blob)}, nil },
			Sibling: func(name string) (io.ReadCloser, error) {
				body, ok := siblings[name]
				if !ok || body == "" {
					return nil, nil
				}

				return fuzzConanBlob{strings.NewReader(body)}, nil
			},
		})
		if err != nil {
			if len(res.Deps) != 0 {
				t.Fatalf("error %v with dependencies %+v", err, res.Deps)
			}

			if errors.Is(err, ErrUnsupported) {
				t.Fatalf("a conan recipe is supported: %v", err)
			}

			return
		}

		fuzzConanCheckFinished(t, res.Deps)

		clean := fuzzConanClean(conanfile)
		for _, d := range res.Deps {
			fuzzConanCheck(t, clean, d)
		}

		if want := finish(parseConan([]manifest.Part{{Label: "conanfile.py", Body: clean}}).Deps); !slices.Equal(res.Deps, want) {
			t.Fatalf("Read = %+v, the conanfile.py alone gives %+v", res.Deps, want)
		}
	})
}
