// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pkgdeps

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openartifact/openartifact/internal/ai/manifest"
)

const pypiFuzzMetadata = "Metadata-Version: 2.1\nName: demo\nVersion: 1.0\nRequires-Dist: requests (>=2.0)\nRequires-Dist: urllib3==1.26.0\n" +
	"Requires-Dist: pytest ; extra == 'test'\nRequires-Dist: six[extra1] (===1.16.0) ; python_version < '3'\n\nLong description\n"

var pypiFuzzName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

func pypiFuzzWheel(name, body string) []byte {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	w, _ := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	_, _ = w.Write([]byte(body))
	_ = zw.Close()

	return buf.Bytes()
}

func pypiFuzzSdist(name, body string) []byte {
	var buf bytes.Buffer

	gw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	tw := tar.NewWriter(gw)

	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gw.Close()

	return buf.Bytes()
}

// pypiFuzzCheckDeps asserts what every dependency list parsed out of a
// METADATA guarantees.
func pypiFuzzCheckDeps(t *testing.T, deps []Dep) {
	t.Helper()

	for _, d := range deps {
		if !pypiFuzzName.MatchString(d.Name) {
			t.Fatalf("dependency name %q is not a distribution name", d.Name)
		}

		if d.Version != "" && d.Range != "" {
			t.Fatalf("dependency %+v is both pinned and ranged", d)
		}

		if strings.ContainsAny(d.Version, " \t,;)*") {
			t.Fatalf("pinned version %q holds a separator", d.Version)
		}

		if d.License != "" {
			t.Fatalf("a METADATA dependency carries the license %q", d.License)
		}
	}
}

// FuzzParsePyPIDeps feeds arbitrary METADATA / PKG-INFO text to the
// Requires-Dist reader.
func FuzzParsePyPIDeps(f *testing.F) {
	f.Add(pypiFuzzMetadata)
	f.Add("Requires-Dist: a\n b (>= 1)\nrequires-dist: c; extra=='x'\nRequires-Dist: [x]\nRequires-Dist: -bad\n\nRequires-Dist: late\n")
	f.Add("Requires-Dist: pkg (== 1.0.*)\nRequires-Dist: pkg ( === 2 )\nRequires-Dist: pkg ; \n")
	f.Add("")

	f.Fuzz(func(t *testing.T, text string) {
		parts := []manifest.Part{{Label: "METADATA", Body: text}}
		res := parsePyPI(parts)

		pypiFuzzCheckDeps(t, res.Deps)

		values := headerValues(text, "Requires-Dist")
		if len(res.Deps) > len(values) {
			t.Fatalf("%d dependencies from %d Requires-Dist headers", len(res.Deps), len(values))
		}

		for _, v := range values {
			if strings.Contains(v, "\n") {
				t.Fatalf("headerValues returned the multi-line value %q", v)
			}
		}

		// The long description after the header block is never a header.
		injected := text + "\n\nRequires-Dist: injected\n"
		if !slices.Equal(headerValues(injected, "Requires-Dist"), values) {
			t.Fatalf("a description changed the Requires-Dist headers of %q", text)
		}

		if again := parsePyPI(parts); !slices.Equal(again.Deps, res.Deps) {
			t.Fatal("parsePyPI is not deterministic")
		}
	})
}

// FuzzReadPyPIDeps feeds an arbitrary file to the dependency reader under the
// file names of the PyPI distribution kinds, and checks the shared
// finishing step: unique, sorted, bounded, trimmed.
func FuzzReadPyPIDeps(f *testing.F) {
	f.Add(pypiFuzzWheel("demo-1.0.dist-info/METADATA", pypiFuzzMetadata), "demo-1.0-py3-none-any.whl")
	f.Add(pypiFuzzWheel("demo-1.0/PKG-INFO", pypiFuzzMetadata), "demo-1.0.zip")
	f.Add(pypiFuzzSdist("demo-1.0/PKG-INFO", pypiFuzzMetadata), "demo-1.0.tar.gz")
	f.Add(pypiFuzzSdist("demo-1.0/PKG-INFO", "Requires-Dist: a\nRequires-Dist: a==1\nRequires-Dist: "+strings.Repeat("b", 600)+"\n"), "demo-1.0.tgz")
	f.Add([]byte("BZh9"), "demo-1.0.tar.bz2")
	f.Add([]byte{0x1f, 0x8b}, "demo-1.0.tar.gz")
	f.Add([]byte{}, "")

	f.Fuzz(func(t *testing.T, data []byte, filename string) {
		res, err := Read(context.Background(), Source{
			Format:   "pypi",
			Filename: filename,
			Open:     func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
		})
		if err != nil {
			if supported := Supported("pypi", filename); supported == errors.Is(err, ErrUnsupported) {
				t.Fatalf("Read(%q) = %v, but Supported = %v", filename, err, supported)
			}

			return
		}

		pypiFuzzCheckFinished(t, res.Deps)
	})
}

func pypiFuzzCheckFinished(t *testing.T, deps []Dep) {
	t.Helper()

	if len(deps) > MaxDeps {
		t.Fatalf("%d dependencies exceed the cap %d", len(deps), MaxDeps)
	}

	seen := map[string]bool{}

	for i, d := range deps {
		if d.Name == "" || seen[d.Name] {
			t.Fatalf("dependency %d has an empty or duplicate name %q", i, d.Name)
		}

		seen[d.Name] = true

		if i > 0 && deps[i-1].Name > d.Name {
			t.Fatalf("dependencies are not sorted: %q before %q", deps[i-1].Name, d.Name)
		}

		for _, s := range []string{d.Name, d.Version, d.Range, d.License} {
			if len(s) > maxFieldLen || s != strings.TrimSpace(s) || !utf8.ValidString(s) {
				t.Fatalf("dependency field %q is unbounded, untrimmed or not UTF-8", s)
			}
		}
	}
}

// FuzzReadPyPIDepsMetadata wraps arbitrary METADATA text in a wheel and an
// sdist: both yield the dependencies of the text parser, finished.
func FuzzReadPyPIDepsMetadata(f *testing.F) {
	f.Add(pypiFuzzMetadata)
	f.Add("Requires-Dist: a\r\n\r\nRequires-Dist: late")
	f.Add("")

	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > memberLimit/2 || !utf8.ValidString(text) || strings.Contains(text, "\x00") {
			return
		}

		want := finish(parsePyPI([]manifest.Part{{Label: "METADATA", Body: text}}).Deps)

		for filename, data := range map[string][]byte{
			"demo-1.0-py3-none-any.whl": pypiFuzzWheel("demo-1.0.dist-info/METADATA", text),
			"demo-1.0.tar.gz":           pypiFuzzSdist("demo-1.0/PKG-INFO", text),
		} {
			res, err := Read(context.Background(), Source{
				Format:   "pypi",
				Filename: filename,
				Open:     func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
			})

			switch {
			case errors.Is(err, manifest.ErrNotFound):
				if len(want) != 0 {
					t.Fatalf("%s: no manifest for a header block that declares %+v", filename, want)
				}
			case err != nil:
				t.Fatalf("%s: %v", filename, err)
			case !slices.Equal(res.Deps, want):
				t.Fatalf("%s: read %+v, text parser read %+v", filename, res.Deps, want)
			}
		}
	})
}
