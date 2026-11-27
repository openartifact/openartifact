// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pub

import (
	"bytes"
	"testing"

	"github.com/openartifact/openartifact/pkg/pub/pubtest"
)

// FuzzParsePubspec feeds ParsePubspec arbitrary YAML: it must never panic, and
// a pubspec it accepts must carry a valid identity and bounded JSON.
func FuzzParsePubspec(f *testing.F) {
	f.Add([]byte(pubtest.Pubspec(pubtest.Spec{Name: "demo", Version: "1.0.0"})))
	f.Add([]byte("name: a\nversion: 1.2.3-rc.1+4\nx: &a [1, 2]\ny: *a\n"))
	f.Add([]byte("name: a\nversion: 1.0.0\n? [k]\n: v\n"))
	f.Add([]byte("name: a\nversion: 1.0.0\nx: !!binary aGk=\nt: 2024-01-02T03:04:05Z\nf: .inf\n"))
	f.Add([]byte("---\nname: a\n---\nversion: 1.0.0\n"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := ParsePubspec(data)
		if err != nil {
			return
		}

		if !ValidName(p.Name) || IsReserved(p.Name) || !ValidVersion(p.Version) || len(p.Pubspec) > MaxPubspecJSONBytes {
			t.Fatalf("accepted an invalid pubspec: %q %q (%d bytes)", p.Name, p.Version, len(p.Pubspec))
		}
	})
}

// FuzzReadArchive feeds ReadArchive arbitrary bytes: it must never panic.
func FuzzReadArchive(f *testing.F) {
	f.Add(pubtest.Build(f, pubtest.Spec{Name: "demo", Version: "1.0.0", Files: map[string]string{"lib/x.dart": "x"}}))
	f.Add([]byte{})
	f.Add([]byte("\x1f\x8b\x08\x00"))

	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := ReadArchive(bytes.NewReader(data))
		if err != nil {
			return
		}

		if !ValidName(p.Name) || !ValidVersion(p.Version) {
			t.Fatalf("accepted an invalid identity: %q %q", p.Name, p.Version)
		}
	})
}

// FuzzParseDoc feeds ParseDoc arbitrary bytes: it must never panic, and a
// document it accepts must render again.
func FuzzParseDoc(f *testing.F) {
	f.Add([]byte(upstreamDoc))
	f.Add([]byte(`{"name":"a","versions":[]}`))
	f.Add([]byte(`{"name":"a","versions":[{"version":"1.0.0","archive_url":"https://x.example/a.tar.gz"}]}`))
	f.Add([]byte(`[]`))

	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := ParseDoc(data)
		if err != nil {
			return
		}

		if _, err := d.Marshal(link, nil); err != nil {
			t.Fatalf("an accepted document does not render: %v", err)
		}
	})
}

// FuzzParsePath checks that a parsed path rebuilds to the same text.
func FuzzParsePath(f *testing.F) {
	f.Add("api/packages/http")
	f.Add("api/packages/http/versions/1.0.0")
	f.Add("api/archives/http-1.0.0.tar.gz")
	f.Add("api/packages/versions/new")

	f.Fuzz(func(t *testing.T, rel string) {
		p, ok := ParsePath(rel)
		if !ok {
			return
		}

		var back string

		switch p.Kind {
		case KindPackage:
			back = PackagePath(p.Name)
		case KindVersion:
			back = PackagePath(p.Name) + "/versions/" + p.Version
		case KindArchive:
			back = ArchivePath(p.Name, p.Version)
		case KindNew:
			back = NewPath
		case KindUpload:
			back = UploadPath
		case KindFinish:
			back = FinishPath
		}

		if back != rel {
			t.Fatalf("%q parsed to %+v, which rebuilds to %q", rel, p, back)
		}
	})
}
