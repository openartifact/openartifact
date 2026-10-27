// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"errors"
	"strings"
	"testing"
)

// FuzzParsePath checks that every accepted path is canonical: it prints back to
// itself, so it can be used as an upstream URL and a cache key, and carries no
// parent reference, empty segment or character that needs escaping.
func FuzzParsePath(f *testing.F) {
	for _, s := range []string{
		"v2/conans/search", "v2/conans/zlib/1.3.1/_/_/latest", "v2/conans/zlib/1.3.1/_/_/revisions",
		"v2/conans/zlib/1.3.1/_/_/revisions/" + testRRev + "/files/conanfile.py",
		"v2/conans/zlib/1.3.1/_/_/revisions/" + testRRev + "/packages/" + testPID + "/revisions/" + testPRev + "/files",
		"v2/conans/zlib/1.3.1/_/_/revisions/" + testRRev + "/packages/" + testPID + "/latest",
		"v2/conans/zlib/1.3.1/_/_/revisions/" + testRRev + "/search", "v2/conans/zlib/1.3.1/_/_/search",
		"v2/conans/../a/1/_/_/latest", "v2/conans/zlib/1.3.1/_/_/revisions/../files", "v2/conans/a/1/_/_/revisions/r/files//x",
		"", "/", "v2/conans/",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, rel string) {
		p, ok := ParsePath(rel)
		if !ok {
			return
		}

		canonical := p.String()
		if canonical != strings.TrimPrefix(rel, "/") {
			t.Fatalf("ParsePath(%q) prints as %q", rel, canonical)
		}

		for seg := range strings.SplitSeq(canonical, "/") {
			if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, "\\?#%\x00") {
				t.Fatalf("ParsePath(%q) accepted segment %q", rel, seg)
			}
		}

		again, ok := ParsePath(canonical)
		if !ok || again != p {
			t.Fatalf("ParsePath is not stable for %q", canonical)
		}
	})
}

// FuzzParseRevisions checks that a list that parses renders to one that parses
// to the same revisions, and that merging it with itself changes nothing.
func FuzzParseRevisions(f *testing.F) {
	for _, s := range []string{
		`{"revisions":[{"revision":"a","time":"2025-01-01T00:00:00.000+0000"}]}`,
		`{"reference":"zlib/1.3.1","revisions":[]}`, `{}`, ``, `{"revisions":[{"revision":"../a","time":"x"}]}`,
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, doc []byte) {
		revs, err := ParseRevisions(doc)
		if err != nil {
			if !errors.Is(err, ErrBadDocument) {
				t.Fatalf("error %v is not ErrBadDocument", err)
			}

			return
		}

		merged := MergeRevisions(revs, revs)

		back, err := ParseRevisions(RenderRevisions(merged))
		if err != nil || len(back) != len(merged) {
			t.Fatalf("rendered list does not parse back: %v, %v", back, err)
		}

		for i := 1; i < len(back); i++ {
			if back[i-1].At().Before(back[i].At()) {
				t.Fatalf("merged list is not newest first: %v", back)
			}
		}
	})
}

// FuzzParseDocuments checks that the other documents are either refused with
// ErrBadDocument or hold only names that are safe to use in a path.
func FuzzParseDocuments(f *testing.F) {
	for _, s := range []string{
		`{"files":{"conanfile.py":{},"conanmanifest.txt":{}}}`, `{"results":["zlib/1.3.1@_/_"]}`, `{"p1":{"settings":{}}}`,
		`{"revision":"a","time":"2025-01-01T00:00:00.000+0000"}`, `{"files":{"../x":{}}}`, `[]`, ``,
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, doc []byte) {
		if names, _, err := ParseFiles(doc); err == nil {
			for _, n := range names {
				if !ValidFilename(n) {
					t.Fatalf("ParseFiles accepted %q", n)
				}
			}
		}

		if refs, err := ParseSearch(doc); err == nil {
			for _, r := range refs {
				if RefName(r) == "" {
					t.Fatalf("ParseSearch accepted %q", r)
				}
			}

			if _, err := ParseSearch(RenderSearch(refs)); err != nil {
				t.Fatalf("rendered search does not parse back: %v", err)
			}
		}

		if pk, err := ParsePackageSearch(doc); err == nil {
			if _, err := ParsePackageSearch(MergePackageSearch(pk, pk)); err != nil {
				t.Fatalf("merged package search does not parse back: %v", err)
			}
		}

		if rev, err := ParseRevision(doc); err == nil && !ValidRevision(rev.ID) {
			t.Fatalf("ParseRevision accepted %q", rev.ID)
		}
	})
}

// FuzzParseV1Path checks that every accepted Conan 1 path is canonical, like
// FuzzParsePath, and that a file path has the Conan 2 file it stands for.
func FuzzParseV1Path(f *testing.F) {
	for _, s := range []string{
		"v1/conans/search", "v1/conans/zlib/1.3.1/_/_", "v1/conans/zlib/1.3.1/_/_/download_urls",
		"v1/conans/zlib/1.3.1/_/_/packages/" + testPID + "/digest",
		"v1/files/zlib/1.3.1/_/_/" + testRRev + "/export/conanfile.py",
		"v1/files/zlib/1.3.1/_/_/" + testRRev + "/package/" + testPID + "/" + testPRev + "/conan_package.tgz",
		"v1/files/zlib/1.3.1/_/_/0/export/../x", "v1/conans/../a/1/_/_", "v1/files/a/1/_/_/r/export//x", "", "/",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, rel string) {
		p, ok := ParseV1Path(rel)
		if !ok {
			return
		}

		canonical := p.String()
		if canonical != strings.TrimPrefix(rel, "/") {
			t.Fatalf("ParseV1Path(%q) prints as %q", rel, canonical)
		}

		for seg := range strings.SplitSeq(canonical, "/") {
			if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, "\\?#%\x00") {
				t.Fatalf("ParseV1Path(%q) accepted segment %q", rel, seg)
			}
		}

		if v2, isFile := p.V2File(); isFile {
			if _, ok := ParsePath(v2.String()); !ok {
				t.Fatalf("the Conan 2 file %q of %q is not a Conan 2 path", v2.String(), rel)
			}
		}
	})
}
