// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pubtest builds pub package archives for tests.
package pubtest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"sort"
)

// TB is the part of testing.TB the builders use, which fuzz targets satisfy too.
type TB interface {
	Helper()
	Fatal(args ...any)
}

// Spec describes a package.
type Spec struct {
	Name, Version string
	// Pubspec replaces the generated pubspec.yaml when not empty.
	Pubspec string
	// Files are extra files (path to content) of the archive.
	Files map[string]string
}

// Entry is one tar entry.
type Entry struct {
	Name string
	Body []byte
	// Type is the tar type flag; 0 means a regular file.
	Type byte
}

// Pubspec returns the pubspec.yaml the archive of s carries.
func Pubspec(s Spec) string {
	if s.Pubspec != "" {
		return s.Pubspec
	}

	return fmt.Sprintf("name: %s\nversion: %s\ndescription: A demo package.\nenvironment:\n  sdk: '>=3.0.0 <4.0.0'\n"+
		"dependencies:\n  collection: ^1.17.0\n", s.Name, s.Version)
}

// Entries returns the entries of a valid archive of s.
func Entries(s Spec) []Entry {
	entries := []Entry{
		{Name: "pubspec.yaml", Body: []byte(Pubspec(s))},
		{Name: "lib/" + s.Name + ".dart", Body: []byte("int answer() => 42;\n")},
	}

	names := make([]string, 0, len(s.Files))
	for name := range s.Files {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		entries = append(entries, Entry{Name: name, Body: []byte(s.Files[name])})
	}

	return entries
}

// Tar returns the gzip-compressed tar of entries.
func Tar(tb TB, entries []Entry) []byte {
	tb.Helper()

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)

	for _, e := range entries {
		typ := e.Type
		if typ == 0 {
			typ = tar.TypeReg
		}

		hdr := &tar.Header{Name: e.Name, Typeflag: typ, Mode: 0o644, Size: int64(len(e.Body))}
		switch typ {
		case tar.TypeReg:
		case tar.TypeXGlobalHeader:
			hdr = &tar.Header{Name: e.Name, Typeflag: typ, PAXRecords: map[string]string{"comment": "made by a test"}}
		default:
			hdr.Size = 0
			hdr.Linkname = "target"
		}

		if err := tw.WriteHeader(hdr); err != nil {
			tb.Fatal(err)
		}

		if typ == tar.TypeReg {
			if _, err := tw.Write(e.Body); err != nil {
				tb.Fatal(err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		tb.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// Build returns a valid archive of s.
func Build(tb TB, s Spec) []byte {
	tb.Helper()

	return Tar(tb, Entries(s))
}
