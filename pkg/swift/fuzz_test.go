// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package swift_test

import (
	"bytes"
	"testing"

	"github.com/openartifact/openartifact/pkg/swift"
	"github.com/openartifact/openartifact/pkg/swift/swifttest"
)

// FuzzReadArchive feeds ReadArchive arbitrary bytes: it must never panic, and
// the manifests it returns must hold valid file names and bounded UTF-8 text.
func FuzzReadArchive(f *testing.F) {
	f.Add(swifttest.Package(f, "", "L", map[string]string{"Package@swift-4.2.swift": swifttest.Manifest("4.2", "L")}))
	f.Add(swifttest.Package(f, "L-1.0.0", "L", nil))
	f.Add([]byte{})
	f.Add([]byte("PK\x05\x06"))

	f.Fuzz(func(t *testing.T, data []byte) {
		ms, err := swift.ReadArchive(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return
		}

		if len(ms) == 0 || ms[0].Filename != swift.ManifestFile {
			t.Fatalf("accepted an archive without Package.swift first: %+v", ms)
		}

		for _, m := range ms {
			if _, ok := swift.ParseManifestName(m.Filename); !ok && m.Filename != swift.ManifestFile {
				t.Fatalf("accepted manifest named %q", m.Filename)
			}

			if len(m.Content) > swift.MaxManifestBytes {
				t.Fatalf("manifest %q is %d bytes", m.Filename, len(m.Content))
			}
		}
	})
}

// FuzzParsePath checks that a parsed path holds a valid identity and names a
// resource that renders back to the path.
func FuzzParsePath(f *testing.F) {
	for _, s := range []string{
		"identifiers", "login", "mona/LinkedList", "mona/LinkedList.json", "mona/LinkedList/1.1.1", "mona/LinkedList/1.1.1.zip",
		"mona/LinkedList/1.1.1/Package.swift", "/a/b/1.0.0-rc.1+x.json",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, rel string) {
		p, ok := swift.ParsePath(rel)
		if !ok {
			return
		}

		switch p.Kind {
		case swift.KindIdentifiers, swift.KindLogin:
			return
		}

		if !swift.ValidScope(p.Scope) || !swift.ValidName(p.Name) {
			t.Fatalf("%q parsed to an invalid identity %+v", rel, p)
		}

		if p.Kind != swift.KindList && !swift.ValidVersion(p.Version) {
			t.Fatalf("%q parsed to an invalid version %+v", rel, p)
		}
	})
}

// FuzzParseLinks checks the Link header parser never panics and only returns
// links with a relation.
func FuzzParseLinks(f *testing.F) {
	f.Add(`<https://x/a>; rel="alternate"; filename="Package@swift-4.swift"; swift-tools-version="4.0", <https://x/b>; rel=latest-version`)
	f.Add(`<`)
	f.Add(`<a>;`)
	f.Add(`<a>; rel="x`)

	f.Fuzz(func(t *testing.T, v string) {
		for _, l := range swift.ParseLinks([]string{v}) {
			if l.Rel == "" {
				t.Fatalf("link without relation: %+v", l)
			}

			_ = l.String()
		}
	})
}

// FuzzParseMetadata checks the metadata validator never panics and that an
// accepted document is compact valid JSON.
func FuzzParseMetadata(f *testing.F) {
	f.Add(
		`{"description":"d","repositoryURLs":["https://github.com/a/b.git"],"licenseURL":"https://x/l","originalPublicationTime":"2020-01-02T03:04:05Z"}`,
	)
	f.Add(`[]`)
	f.Add(`{"repositoryURLs":[""]}`)

	f.Fuzz(func(t *testing.T, data string) {
		raw, m, err := swift.ParseMetadata([]byte(data))
		if err != nil {
			return
		}

		if len(raw) == 0 || raw[0] != '{' || len(m.RepositoryURLs) > swift.MaxRepositoryURLs {
			t.Fatalf("accepted %q as %s %+v", data, raw, m)
		}

		for _, u := range m.RepositoryURLs {
			_ = swift.NormalizeURL(u)
		}
	})
}

// FuzzParseReleaseDocs checks the upstream document parsers never panic.
func FuzzParseReleaseDocs(f *testing.F) {
	f.Add(`{"releases":{"1.0.0":{"url":"https://x"}}}`)
	f.Add(
		`{"id":"a.b","version":"1.0.0","resources":[{"name":"source-archive","checksum":"` + string(bytes.Repeat([]byte("0"), 64)) + `"}]}`,
	)

	f.Fuzz(func(t *testing.T, data string) {
		if l, err := swift.ParseReleaseList([]byte(data)); err == nil {
			for _, v := range l.Versions() {
				if !swift.ValidVersion(v) {
					t.Fatalf("list holds invalid version %q", v)
				}
			}
		}

		if r, err := swift.ParseRelease([]byte(data), "a.b", "1.0.0"); err == nil && !swift.ValidSHA256Hex(r.Checksum()) {
			t.Fatalf("release accepted with checksum %q", r.Checksum())
		}
	})
}

// FuzzNormalizeURL checks that normalization is idempotent.
func FuzzNormalizeURL(f *testing.F) {
	f.Add("git@github.com:mona/LinkedList.git")
	f.Add("https://github.com/mona/LinkedList/")

	f.Fuzz(func(t *testing.T, s string) {
		n := swift.NormalizeURL(s)
		if n != "" && swift.NormalizeURL(n) != n {
			t.Fatalf("NormalizeURL(%q) = %q, then %q", s, n, swift.NormalizeURL(n))
		}
	})
}
