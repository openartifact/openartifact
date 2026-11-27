// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package p2

import (
	"bytes"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/p2/p2test"
)

// FuzzRewriteXML feeds the rewriter arbitrary documents: it must never panic,
// and a document it accepts must come out as a document it accepts again with
// nothing left to strip.
func FuzzRewriteXML(f *testing.F) {
	f.Add(p2test.Artifacts)
	f.Add(p2test.Composite)
	f.Add("<repository><properties size='1'><property name='p2.statsURI' value='x'/></properties></repository>")
	f.Add("<repository><children><child location='../x'/><child/></children></repository>")
	f.Add("<!DOCTYPE a [<!ENTITY b 'c'>]><repository/>")
	f.Add("")

	r, err := NewRelocator("https://u.example/rel", "https://u.example/rel/compositeContent.xml", "compositeContent.xml")
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, doc string) {
		var once bytes.Buffer
		if err := RewriteXML(&once, strings.NewReader(doc), Options{Relocate: r.Relocate}); err != nil {
			return
		}

		var twice bytes.Buffer
		if err := RewriteXML(&twice, bytes.NewReader(once.Bytes()), Options{Relocate: r.Relocate}); err != nil {
			t.Fatalf("the rewrite of an accepted document is refused: %v\n%q", err, once.String())
		}

		if !bytes.Equal(once.Bytes(), twice.Bytes()) {
			t.Fatalf("the rewrite is not stable:\n%q\n%q", once.String(), twice.String())
		}
	})
}

// FuzzRewriteArchive feeds the three encodings arbitrary bytes: never a panic.
func FuzzRewriteArchive(f *testing.F) {
	f.Add(p2test.Jar(f, "artifacts.xml", p2test.Artifacts))
	f.Add(p2test.XZ(f, p2test.Artifacts))
	f.Add([]byte("PK\x05\x06\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"))
	f.Add([]byte("\xfd7zXZ\x00"))
	f.Add([]byte(p2test.Artifacts))

	docs := make([]Doc, 0, 3)

	for _, key := range []string{"artifacts.jar", "artifacts.xml.xz", "artifacts.xml"} {
		d, _ := ParseDoc(key)
		docs = append(docs, d)
	}

	f.Fuzz(func(_ *testing.T, data []byte) {
		for _, d := range docs {
			_ = d.Rewrite(&bytes.Buffer{}, bytes.NewReader(data), int64(len(data)), Options{})
		}
	})
}

// FuzzRelocate checks that a relocated location never leaves the proxy: it is
// relative, or it is the location as it came.
func FuzzRelocate(f *testing.F) {
	f.Add("../../x/", "a/b/compositeContent.xml")
	f.Add("https://u.example/rel/x", "compositeContent.xml")
	f.Add("//u.example/y", "compositeContent.xml")
	f.Add("%2e%2e/%2e%2e/z", "compositeContent.xml")

	f.Fuzz(func(t *testing.T, location, key string) {
		r, err := NewRelocator("https://u.example/rel", "https://u.example/rel/"+key, key)
		if err != nil {
			return
		}

		got, changed := r.Relocate(location)
		if !changed {
			if got != location {
				t.Fatalf("unchanged location came back as %q (was %q)", got, location)
			}

			return
		}

		if strings.Contains(got, "://") || strings.HasPrefix(got, "/") {
			t.Fatalf("relocated %q to the non-relative %q", location, got)
		}
	})
}
