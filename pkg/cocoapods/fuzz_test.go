// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cocoapods

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// FuzzParsePodspec feeds ParsePodspec arbitrary upstream documents: none may
// panic, and a podspec it accepts rewrites to a JSON document of the same pod
// that parses again, with an archive the proxy can serve under a plain path.
func FuzzParsePodspec(f *testing.F) {
	for _, s := range []string{
		`{"name":"Pod","version":"1.0","source":{"git":"https://github.com/o/r.git","tag":"1.0"}}`,
		`{"name":"Pod","version":"1.0","source":{"git":"https://github.com/o/r.git","commit":"abcdef0"}}`,
		`{"name":"Pod","version":"1.0","source":{"http":"https://dl.example/a.zip","sha256":"` + strings.Repeat("a", 64) + `"}}`,
		`{"name":"Pod","version":"1.0","source":{"http":"https://dl.example/a.zip","headers":["X: y"]}}`,
		`{"name":"Pod","version":"1.0","source":{"svn":"https://svn.example/r"}}`,
		`{"name":"Pod","version":"1.0","source":"x","source":{"http":"https://a/b"}}`,
		`{"name":"Pod","version":"1.0"}`, `[]`, ``, `{"name":"Pod","version":"1.0","source":{"http":"https://a/\u0000"}}`,
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, doc []byte) {
		p, err := ParsePodspec(doc, "Pod", "1.0")
		if err != nil {
			return
		}

		a, ok := p.Archive()
		if !ok {
			if out, rewritten := p.Rewrite("https://oa/x"); rewritten || !bytes.Equal(out, doc) {
				t.Fatalf("a source left alone was changed: %s", out)
			}

			return
		}

		if !httpURL(a.URL) || !validSegment(a.File) || !ValidRef(a.Ref()) {
			t.Fatalf("archive %+v cannot be served", a)
		}

		out, rewritten := p.Rewrite("https://oa/x")
		if !rewritten || !json.Valid(out) {
			t.Fatalf("rewritten podspec is not JSON: %s", out)
		}

		again, err := ParsePodspec(out, "Pod", "1.0")
		if err != nil {
			t.Fatalf("rewritten podspec no longer parses: %v\n%s", err, out)
		}

		if b, ok := again.Archive(); !ok || b.URL != "https://oa/x" {
			t.Fatalf("rewritten source is %+v, %v", b, ok)
		}
	})
}

// FuzzParseShard feeds the shard parsers arbitrary documents: none may panic,
// and a document that verifies filters to itself, line for line, with a keep
// function that keeps everything.
func FuzzParseShard(f *testing.F) {
	for _, s := range []string{
		"AAManager/0.0.1/0.0.2\nCKToolbox/0.0.9\n", "A/1\r\n\nB/2\n", "", "\n", "A", "A//1", "/1", "A/1\x00", "<html>", "A/1/2/3/4/5/6/7/8/9",
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, doc []byte) {
		if VerifyShard(doc) != nil {
			if _, err := ParseShard(doc); err == nil {
				t.Fatal("ParseShard accepted what VerifyShard refused")
			}

			return
		}

		pods, err := ParseShard(doc)
		if err != nil {
			t.Fatal(err)
		}

		kept, err := FilterShard(doc, func(string, string) bool { return true })
		if err != nil || VerifyShard(kept) != nil {
			t.Fatalf("filtered shard is not a shard: %v\n%q", err, kept)
		}

		again, err := ParseShard(kept)
		if err != nil || len(again) != len(pods) {
			t.Fatalf("filtering changed the lines: %v %d != %d", err, len(again), len(pods))
		}

		for _, pv := range pods {
			if _, ok, err := VersionsOf(doc, pv.Name); err != nil || !ok {
				t.Fatalf("VersionsOf(%q) = %v, %v", pv.Name, ok, err)
			}
		}
	})
}
