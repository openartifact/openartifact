// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pacman

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestReadDBRoundTrip(t *testing.T) {
	t.Parallel()

	a, b := sampleEntry(), sampleEntry()
	b.Name, b.Version, b.Filename, b.Arch = "alpha", "1-1", "alpha-1-1-any.pkg.tar.xz", "any"
	b.Files, b.Depends, b.Groups = nil, nil, nil

	var buf bytes.Buffer
	if err := WriteDB(&buf, []DBEntry{a, b}, true); err != nil {
		t.Fatal(err)
	}

	got, err := ReadDB(&buf)
	if err != nil {
		t.Fatal(err)
	}

	want := []DBEntry{b, a} // WriteDB orders by name
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, want)
	}
}

func TestReadDBCompressions(t *testing.T) {
	t.Parallel()

	var plain bytes.Buffer
	if err := WriteDB(&plain, []DBEntry{sampleEntry()}, false); err != nil {
		t.Fatal(err)
	}

	tarball := buildTar(t, []member{
		{name: "hello-1:2.12-3/", dir: true},
		{name: "hello-1:2.12-3/desc", body: string(func() []byte { e := sampleEntry(); return e.Desc() }())},
	})

	for _, kind := range []string{CompressionGzip, CompressionXZ, CompressionZstd} {
		got, err := ReadDB(bytes.NewReader(compress(t, kind, tarball)))
		if err != nil || len(got) != 1 || got[0].Name != "hello" {
			t.Errorf("%s: %v %v", kind, got, err)
		}
	}
}

func TestReadDBDropsUnusableEntries(t *testing.T) {
	t.Parallel()

	good := sampleEntry()
	bad := func(name, desc string) member { return member{name: name, body: desc} }
	tarball := buildTar(t, []member{
		{name: "hello-1:2.12-3/desc", body: string(good.Desc())},
		bad("a-1-1/desc", "%FILENAME%\n../evil-1-1-any.pkg.tar.zst\n\n%NAME%\na\n\n%VERSION%\n1-1\n\n%ARCH%\nany\n"),
		bad("b-1-1/desc", "%FILENAME%\nb-1-1-any.pkg.tar.zst\n\n%NAME%\nb\n\n%VERSION%\n1-1\n\n%ARCH%\nany\n\n%CSIZE%\nnope\n"),
		bad("c-1-1/desc", "%FILENAME%\nc-1-1-any.db\n\n%NAME%\nc\n\n%VERSION%\n1-1\n\n%ARCH%\nany\n"),
		bad("d-1-1/desc", "%NAME%\nd\n"),
		bad("nested/d-1-1/desc", "%NAME%\nd\n"),
		bad("hello-1:2.12-3/other", "x"),
	})

	got, err := ReadDB(bytes.NewReader(compress(t, CompressionGzip, tarball)))
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].Name != "hello" {
		t.Fatalf("kept %+v", got)
	}
}

func TestReadDBLegacyDepends(t *testing.T) {
	t.Parallel()

	tarball := buildTar(t, []member{
		{name: "a-1-1/desc", body: "%FILENAME%\na-1-1-any.pkg.tar.zst\n\n%NAME%\na\n\n%VERSION%\n1-1\n\n%ARCH%\nany\n"},
		{name: "a-1-1/depends", body: "%DEPENDS%\nglibc\nbash\n\n%PROVIDES%\nx\n"},
		{name: "a-1-1/files", body: "%FILES%\nusr/\nusr/bin/a\n\n%BACKUP%\netc/a\tabc\n"},
	})

	got, err := ReadDB(bytes.NewReader(compress(t, CompressionGzip, tarball)))
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}

	if !reflect.DeepEqual(got[0].Depends, []string{"glibc", "bash"}) || !reflect.DeepEqual(got[0].Provides, []string{"x"}) {
		t.Errorf("depends not merged: %+v", got[0])
	}

	if !reflect.DeepEqual(got[0].Files, []string{"usr/", "usr/bin/a"}) {
		t.Errorf("files = %v", got[0].Files)
	}
}

func TestReadDBRejects(t *testing.T) {
	t.Parallel()

	for _, bad := range [][]byte{nil, []byte("hello"), compress(t, CompressionGzip, []byte("not a tar archive at all, not even close"))} {
		if _, err := ReadDB(bytes.NewReader(bad)); !errors.Is(err, ErrNotDatabase) {
			t.Errorf("%q: err = %v", bad[:min(len(bad), 12)], err)
		}
	}
}

func TestReadFileLists(t *testing.T) {
	t.Parallel()

	a, b := sampleEntry(), sampleEntry()
	b.Name, b.Files = "other", []string{"opt/other"}

	var buf bytes.Buffer
	if err := WriteDB(&buf, []DBEntry{a, b}, true); err != nil {
		t.Fatal(err)
	}

	got, err := ReadFileLists(&buf, func(dir string) bool { return strings.HasPrefix(dir, "other-") })
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || !reflect.DeepEqual(got["other-1:2.12-3"], []string{"opt/other"}) {
		t.Fatalf("got %v", got)
	}
}

func TestValidFileName(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]bool{
		"a-1-1-any.pkg.tar.zst":     true,
		"a-1:2-1-x86_64.pkg.tar.xz": true,
		"a-1-1-any.db":              false,
		"../a-1-1-any.pkg.tar.zst":  false,
		"a/b-1-1-any.pkg.tar.zst":   false,
		".a-1-1-any.pkg.tar.zst":    false,
		"a-1-1-any.pkg.tar.zst.sig": false,
		"":                          false,
		"a b-1-1-any.pkg.tar.zst":   false,
	} {
		if got := ValidFileName(name); got != want {
			t.Errorf("ValidFileName(%q) = %v", name, got)
		}
	}
}

func TestClassifyFile(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]FileKind{
		"a-1-1-x86_64.pkg.tar.zst":      FilePackage,
		"a-1:1-1-any.pkg.tar.xz":        FilePackage,
		"a-1-1-x86_64.pkg.tar.gz.sig":   FilePackageSig,
		"core.db":                       FileDatabase,
		"core.db.tar.gz":                FileDatabase,
		"core.files":                    FileDatabase,
		"core.files.tar.gz.sig":         FileDatabaseSig,
		"core.db.sig":                   FileDatabaseSig,
		".db":                           FileInvalid,
		"a-1-1-x86_64.pkg.tar.bz2":      FileInvalid,
		"../a-1-1-x86_64.pkg.tar.zst":   FileInvalid,
		"a b-1-1-x86_64.pkg.tar.zst":    FileInvalid,
		".hidden-1-1-any.pkg.tar.zst":   FileInvalid,
		"a-1-1-x86_64.pkg.tar.zst.sig2": FileInvalid,
	} {
		if got, _ := ClassifyFile(name); got != want {
			t.Errorf("ClassifyFile(%q) = %v, want %v", name, got, want)
		}
	}
}
