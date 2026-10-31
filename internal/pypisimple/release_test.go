// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypisimple

import (
	"testing"
	"time"
)

func TestFileVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		project, filename, want string
	}{
		{"requests", "requests-2.32.0-py3-none-any.whl", "2.32"},
		{"requests", "requests-2.32.0.tar.gz", "2.32"},
		{"requests", "requests-2.32.tar.gz", "2.32"},
		{"requests", "requests-2.32.0-py3-none-any.whl.metadata", "2.32"},
		{"zope-interface", "zope.interface-6.0-cp311-cp311-linux_x86_64.whl", "6"},
		{"zope-interface", "zope.interface-6.0.tar.gz", "6"},
		{"foo-bar", "foo_bar-1.0rc1.zip", "1.0rc1"},
		{"foo-bar", "foo-bar-1.0.post1.tar.bz2", "1.0.post1"},
		{"foo", "Foo-V1_2_0.tar.gz", "1.2"},
		{"foo", "foo-1.0.0.egg", "1"},
		{"foo", "other-name-3.1.tar.gz", "3.1"},
		{"foo", "foo.tar.gz", ""},
		{"foo", "foo.whl", ""},
	}

	for _, tt := range tests {
		if got := FileVersion(tt.project, tt.filename); got != tt.want {
			t.Errorf("FileVersion(%q, %q) = %q, want %q", tt.project, tt.filename, got, tt.want)
		}
	}
}

func TestSpelledVersion(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ project, filename, want string }{
		{"requests", "requests-2.32.0-py3-none-any.whl", "2.32.0"},
		{"requests", "requests-2.32.0.tar.gz", "2.32.0"},
		{"requests", "requests-2.32.0-py3-none-any.whl.metadata", "2.32.0"},
		{"foo", "Foo-V1_2_0.tar.gz", "V1_2_0"},
		{"foo", "foo.tar.gz", ""},
	} {
		if got := SpelledVersion(tt.project, tt.filename); got != tt.want {
			t.Errorf("SpelledVersion(%q, %q) = %q, want %q", tt.project, tt.filename, got, tt.want)
		}
	}
}

func TestUploaded(t *testing.T) {
	t.Parallel()

	want := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

	for _, in := range []string{"2024-01-02T03:04:05Z", "2024-01-02T03:04:05.000000Z", "2024-01-02T04:04:05+01:00"} {
		f := File{UploadTime: in}
		if got := f.Uploaded(); !got.Equal(want) {
			t.Errorf("Uploaded(%q) = %v, want %v", in, got, want)
		}
	}

	for _, in := range []string{"", "yesterday", "2024-01-02"} {
		f := File{UploadTime: in}
		if got := f.Uploaded(); !got.IsZero() {
			t.Errorf("Uploaded(%q) = %v, want zero", in, got)
		}
	}
}

func TestReleases_EarliestFileDatesTheRelease(t *testing.T) {
	t.Parallel()

	files := []File{
		{Filename: "foo-1.0.tar.gz", UploadTime: "2024-01-01T00:00:00Z"},
		// A wheel added to the old release a month later.
		{Filename: "foo-1.0-py3-none-any.whl", UploadTime: "2024-02-01T00:00:00Z"},
		{Filename: "foo-2.0.tar.gz", UploadTime: "2024-03-01T00:00:00Z"},
		// No time at all, and an unparsable one: the release is undated.
		{Filename: "foo-3.0.tar.gz"},
		{Filename: "foo-3.0-py3-none-any.whl", UploadTime: "soon"},
		// A file that carries no version is a release of its own.
		{Filename: "foo.zip", UploadTime: "2024-04-01T00:00:00Z"},
		{Filename: "foo.exe"},
	}

	rel := NewReleases("foo", files)

	for filename, want := range map[string]time.Time{
		"foo-1.0.tar.gz":           time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		"foo-1.0-py3-none-any.whl": time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		"foo-2.0.tar.gz":           time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
		"foo-3.0.tar.gz":           {},
		"foo-3.0-py3-none-any.whl": {},
		"foo.zip":                  time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC),
		"foo.exe":                  {},
		"foo-9.9.tar.gz":           {},
	} {
		if got := rel.Published("foo", filename); !got.Equal(want) {
			t.Errorf("Published(%q) = %v, want %v", filename, got, want)
		}
	}

	cutoff := time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC)

	kept := rel.Filter("foo", files, func(_ string, published time.Time) bool { return published.IsZero() || published.Before(cutoff) })

	var names []string
	for _, f := range kept {
		names = append(names, f.Filename)
	}

	want := []string{"foo-1.0.tar.gz", "foo-1.0-py3-none-any.whl", "foo-3.0.tar.gz", "foo-3.0-py3-none-any.whl", "foo.exe"}
	if len(names) != len(want) {
		t.Fatalf("kept %v, want %v", names, want)
	}

	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("kept %v, want %v", names, want)
		}
	}

	if len(files) != 7 {
		t.Errorf("Filter modified its input")
	}
}
