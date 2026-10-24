// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pkgdeps

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openartifact/openartifact/internal/ai/manifest"
)

// fuzzNuGetPart is a manifest part as manifest.ExtractParts hands it over: valid
// UTF-8 without NULs.
func fuzzNuGetPart(label, body string) manifest.Part {
	body = strings.ReplaceAll(strings.ToValidUTF8(body, "\uFFFD"), "\x00", "")

	return manifest.Part{Label: label, Body: body}
}

// fuzzNuGetCheckFinished checks what finish promises for any dependency list:
// bounded, named, bounded fields, sorted by unique name.
func fuzzNuGetCheckFinished(t *testing.T, deps []Dep) {
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

// fuzzNuGetCheckPinned checks the version/range split of a dependency as
// parseNuGet reads it: a pinned version is one exact version and leaves no range.
func fuzzNuGetCheckPinned(t *testing.T, deps []Dep) {
	t.Helper()

	for _, d := range deps {
		if d.Version == "" {
			if d.Range != strings.TrimSpace(d.Range) {
				t.Fatalf("range %q of %q is not trimmed", d.Range, d.Name)
			}

			continue
		}

		if d.Range != "" || strings.Contains(d.Version, ",") || d.Version != strings.TrimSpace(d.Version) {
			t.Fatalf("dependency %+v pins a version that is a range or untrimmed", d)
		}
	}
}

// FuzzNuGetDeps reads the dependencies of any .nuspec text.
func FuzzNuGetDeps(f *testing.F) {
	f.Add(`<package><metadata><dependencies><dependency id="a" version="[1.0.0]"/><dependency id="b" version="1.0"/></dependencies></metadata></package>`)
	f.Add(`<package><metadata><dependencies><group targetFramework="net8.0"><dependency id="a" version="[1.0.0, 2.0.0)"/><dependency id=" b " version=" [ 2.0 ] "/></group></dependencies></metadata></package>`)
	f.Add(`<package><metadata><dependencies><dependency id="a"/><dependency id="a" version="[1]"/><dependency id="a" version="2"/></dependencies></metadata></package>`)
	f.Add(`<package><metadata><dependencies><dependency id="" version="1"/><dependency id="  "/></dependencies></metadata></package>`)
	f.Add(`<package><metadata><dependencies><dependency id="a" version="[1,]"/><dependency id="b" version="[[1]]"/></dependencies></metadata></package>`)
	f.Add(`<package><metadata/></package>`)
	f.Add(`<package`)
	f.Add(``)

	f.Fuzz(func(t *testing.T, nuspec string) {
		part := fuzzNuGetPart(".nuspec", nuspec)

		res, err := parseNuGet([]manifest.Part{part})
		if err != nil {
			if len(res.Deps) != 0 || res.Namespace != "" {
				t.Fatalf("error %v with a result %+v", err, res)
			}

			return
		}

		fuzzNuGetCheckPinned(t, res.Deps)
		fuzzNuGetCheckFinished(t, finish(res.Deps))
	})
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

// FuzzNuGetRead reads a .nupkg: a nuspec at the root plus one more member under
// any name. The dependencies are those of the nuspec, or the archive is refused
// when the extra member makes the manifest ambiguous.
func FuzzNuGetRead(f *testing.F) {
	const spec = `<package><metadata><dependencies><dependency id="a" version="[1.0.0]"/></dependencies></metadata></package>`

	f.Add(spec, "lib/net8.0/a.dll")
	f.Add(spec, "b.nuspec")
	f.Add(spec, "./b.nuspec")
	f.Add(spec, "dir/../b.nuspec")
	f.Add(spec, `x\y.nuspec`)
	f.Add(spec, ".nuspec")
	f.Add(spec, "P.NuSpec")
	f.Add(spec, "")
	f.Add("", "b.nuspec")

	f.Fuzz(func(t *testing.T, nuspec, extra string) {
		members := [][2]string{{"p.nuspec", nuspec}}
		if extra != "" {
			members = append(members, [2]string{extra, "x"})
		}

		data := fuzzNuGetZip(members)

		res, err := Read(t.Context(), Source{
			Format:   "nuget",
			Filename: "p.1.0.0.nupkg",
			Open:     func() (io.ReadCloser, error) { return fuzzNuGetBlob{bytes.NewReader(data)}, nil },
		})
		if err != nil {
			if len(res.Deps) != 0 {
				t.Fatalf("error %v with dependencies %+v", err, res.Deps)
			}

			if errors.Is(err, ErrUnsupported) {
				t.Fatalf("a .nupkg is supported: %v", err)
			}

			return
		}

		fuzzNuGetCheckFinished(t, res.Deps)

		direct, derr := parseNuGet([]manifest.Part{fuzzNuGetPart(".nuspec", nuspec)})
		if derr != nil {
			t.Fatalf("Read accepted a nuspec the parser refuses: %v", derr)
		}

		if want := finish(direct.Deps); !slices.Equal(res.Deps, want) {
			t.Fatalf("Read = %+v, the nuspec alone gives %+v", res.Deps, want)
		}
	})
}
