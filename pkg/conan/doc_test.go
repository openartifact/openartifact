// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"errors"
	"slices"
	"testing"
)

func TestParseRevision(t *testing.T) {
	t.Parallel()

	rev, err := ParseRevision([]byte(`{"revision":"abc","time":"2025-12-09T12:51:39.337+0000"}`))
	if err != nil || rev.ID != "abc" || rev.At().Year() != 2025 {
		t.Fatalf("ParseRevision = %+v, %v", rev, err)
	}

	for _, doc := range []string{
		``, `{}`, `[]`, `null`, `{"revision":"","time":"2025-12-09T12:51:39.337+0000"}`,
		`{"revision":"a/b","time":"2025-12-09T12:51:39.337+0000"}`, `{"revision":"abc","time":"yesterday"}`,
		`{"revision":"abc","time":"2025-12-09T12:51:39.337+0000"} {}`,
	} {
		if _, err := ParseRevision([]byte(doc)); !errors.Is(err, ErrBadDocument) {
			t.Errorf("ParseRevision(%q) error = %v, want ErrBadDocument", doc, err)
		}
	}
}

func TestMergeRevisionsNewestFirst(t *testing.T) {
	t.Parallel()

	a, err := ParseRevisions([]byte(`{"reference":"zlib/1.3.1","revisions":[
		{"revision":"r1","time":"2025-01-01T00:00:00.000+0000"},{"revision":"r3","time":"2025-03-01T00:00:00.000+0000"}]}`))
	if err != nil {
		t.Fatal(err)
	}

	b, err := ParseRevisions([]byte(`{"revisions":[
		{"revision":"r2","time":"2025-02-01T00:00:00.000+0000"},{"revision":"r1","time":"2026-01-01T00:00:00.000+0000"}]}`))
	if err != nil {
		t.Fatal(err)
	}

	merged := MergeRevisions(a, b)

	var ids []string
	for _, r := range merged {
		ids = append(ids, r.ID)
	}

	if !slices.Equal(ids, []string{"r3", "r2", "r1"}) {
		t.Fatalf("merged order = %v", ids)
	}

	if merged[2].Time != "2025-01-01T00:00:00.000+0000" {
		t.Errorf("a revision listed twice keeps the first list's entry, got %q", merged[2].Time)
	}

	back, err := ParseRevisions(RenderRevisions(merged))
	if err != nil || len(back) != 3 {
		t.Fatalf("rendered list does not parse back: %v, %v", back, err)
	}

	if got := string(RenderRevisions(nil)); got != `{"revisions":[]}` {
		t.Errorf("empty list rendered as %s", got)
	}
}

func TestParseFiles(t *testing.T) {
	t.Parallel()

	names, published, err := ParseFiles([]byte(`{"files":{"conanfile.py":{},"conanmanifest.txt":{}}}`))
	if err != nil || !published || !slices.Equal(names, []string{"conanfile.py", "conanmanifest.txt"}) {
		t.Fatalf("ParseFiles = %v, %v, %v", names, published, err)
	}

	if _, published, err = ParseFiles([]byte(`{"files":{"conanfile.py":{}}}`)); err != nil || published {
		t.Errorf("a list without the manifest is not published: %v, %v", published, err)
	}

	for _, doc := range []string{`{}`, `{"files":null}`, `{"files":{"../x":{}}}`, `[]`, ``} {
		if _, _, err := ParseFiles([]byte(doc)); !errors.Is(err, ErrBadDocument) {
			t.Errorf("ParseFiles(%q) error = %v, want ErrBadDocument", doc, err)
		}
	}
}

func TestSearchDocuments(t *testing.T) {
	t.Parallel()

	got, err := ParseSearch([]byte(`{"results":["zlib/1.2.11@_/_","zlib/1.3.1","mylib/2.0@acme/stable"]}`))
	if err != nil || len(got) != 3 {
		t.Fatalf("ParseSearch = %v, %v", got, err)
	}

	for ref, want := range map[string]string{"zlib/1.3.1@_/_": "zlib", "zlib/1.3.1": "zlib", "zlib": "", "/1": "", "a b/1": "", "": ""} {
		if RefName(ref) != want {
			t.Errorf("RefName(%q) = %q, want %q", ref, RefName(ref), want)
		}
	}

	for _, doc := range []string{`{"results":["nonsense"]}`, `{"results":[1]}`, `[]`, ``} {
		if _, err := ParseSearch([]byte(doc)); err == nil {
			t.Errorf("ParseSearch(%q) succeeded", doc)
		}
	}

	if string(RenderSearch(nil)) != `{"results":[]}` {
		t.Errorf("empty search rendered as %s", RenderSearch(nil))
	}
}

func TestMergePackageSearch(t *testing.T) {
	t.Parallel()

	a, err := ParsePackageSearch([]byte(`{"p1":{"settings":{"os":"Linux"}},"p2":{}}`))
	if err != nil {
		t.Fatal(err)
	}

	b, err := ParsePackageSearch([]byte(`{"p1":{"settings":{"os":"Windows"}},"p3":{}}`))
	if err != nil {
		t.Fatal(err)
	}

	merged, err := ParsePackageSearch(MergePackageSearch(a, b))
	if err != nil || len(merged) != 3 || string(merged["p1"]) != `{"settings":{"os":"Linux"}}` {
		t.Fatalf("merged = %v, %v", merged, err)
	}

	for _, doc := range []string{`{"p1":1}`, `{"p1":null}`, `{"a/b":{}}`, `[]`} {
		if _, err := ParsePackageSearch([]byte(doc)); err == nil {
			t.Errorf("ParsePackageSearch(%q) succeeded", doc)
		}
	}
}
