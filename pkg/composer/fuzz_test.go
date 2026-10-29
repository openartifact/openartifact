// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// fataler is the part of testing.TB that both *testing.F and the OSS-Fuzz
// shim's F (which lacks the rest of testing.TB) provide.
type fataler interface {
	Helper()
	Fatal(args ...any)
}

// seedZip is a zip with the given file name and body.
func seedZip(tb fataler, name, body string) []byte {
	tb.Helper()

	var buf bytes.Buffer

	w := zip.NewWriter(&buf)

	fw, err := w.Create(name)
	if err != nil {
		tb.Fatal(err)
	}

	if _, err := fw.Write([]byte(body)); err != nil {
		tb.Fatal(err)
	}

	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// FuzzReadArchive feeds ReadArchive arbitrary bytes: it must never panic, and
// an archive it accepts must carry a valid package name.
func FuzzReadArchive(f *testing.F) {
	f.Add(seedZip(f, "composer.json", `{"name":"acme/tools","version":"1.0.0","license":"MIT"}`))
	f.Add(seedZip(f, "x-y-1/composer.json", `{"name":"acme/tools"}`))
	f.Add([]byte("PK\x03\x04"))

	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := ReadArchive(bytes.NewReader(data), int64(len(data)))
		if err == nil && !ValidName(m.Name) {
			t.Fatalf("accepted the invalid name %q", m.Name)
		}
	})
}

// FuzzNormalizePublished checks that an accepted version is a bounded,
// URL-safe string and that its normalized form is never empty.
func FuzzNormalizePublished(f *testing.F) {
	for _, v := range []string{"1.0.0", "v1.2.3", "1.0.0-RC1", "1.x-dev", "dev-main", "2.0.0+build", "", "dev-", "1.2.3.4.5"} {
		f.Add(v)
	}

	f.Fuzz(func(t *testing.T, v string) {
		n, err := NormalizePublished(v)
		if err != nil {
			return
		}

		if n == "" || len(n) > MaxVersionLen || len(v) > MaxVersionLen {
			t.Fatalf("NormalizePublished(%q) = %q", v, n)
		}
	})
}

// FuzzRenderSource feeds RenderSource arbitrary upstream bodies: none may
// panic, and what it renders is a minified document of the one package whose
// every dist points at the repository.
func FuzzRenderSource(f *testing.F) {
	f.Add([]byte(`{"minified":"composer/2.0","packages":{"a/b":[` +
		`{"name":"a/b","version":"2.0.0","version_normalized":"2.0.0.0","dist":{"type":"zip","url":"https://h/z/2","reference":"r2","shasum":""}},` +
		`{"version":"1.0.0","version_normalized":"1.0.0.0","dist":{"url":"https://h/z/1","reference":"r1"}},` +
		`{"version":"0.9.0","dist":"__unset"}]}}`))
	f.Add([]byte(`{"packages":{"a/b":[{"version":"1.0.0","dist":{"type":"tar","url":"https://h/t"}},{"version":"1.1.0","dist":null}]}}`))
	f.Add([]byte(`{"packages":{"a/b":[]}}`))
	f.Add([]byte(`{`))

	const root = "https://oa.example/repos/php"

	f.Fuzz(func(t *testing.T, data []byte) {
		out, err := RenderSource(data, "a/b", root)
		if err != nil {
			return
		}

		versions, err := ParseDocument(out, "a/b")
		if err != nil {
			t.Fatalf("rendered document does not read back: %v", err)
		}

		for _, v := range versions {
			block, ok := v["dist"].(map[string]any)
			if !ok {
				continue
			}

			u, _ := block["url"].(string)
			if _, ok := DistRef(u); !ok || len(u) <= len(root) || u[:len(root)] != root {
				t.Fatalf("dist url %q is not a dist of the repository", u)
			}
		}
	})
}

// FuzzParsePath checks that a parsed path names a valid package and that a
// dist path built from it parses back to the same coordinates.
func FuzzParsePath(f *testing.F) {
	for _, p := range []string{
		"packages.json", "p2/acme/tools.json", "p2/acme/tools~dev.json", "packages/upload/acme/tools/dev-x%2Fy",
		"dists/acme/tools/1.0.0/abc.zip", "dists/acme/tools/dev-x/y/abc.zip", "dists//", "p2/a/b.json/c",
	} {
		f.Add(p)
	}

	f.Fuzz(func(t *testing.T, rel string) {
		p, ok := ParsePath(rel)
		if !ok {
			return
		}

		switch p.Kind {
		case KindMetadata, KindUpload, KindDist:
			if !ValidName(p.Name) {
				t.Fatalf("ParsePath(%q) accepted the invalid name %q", rel, p.Name)
			}
		}

		if p.Kind != KindDist || p.Ref == "" || strings.ContainsAny(p.Ref, "/%") {
			return
		}

		back, ok := ParsePath(DistPath(p.Name, p.Version, p.Ref))
		if !ok || back != p {
			t.Fatalf("DistPath of %+v parses back to %+v, %v", p, back, ok)
		}
	})
}
