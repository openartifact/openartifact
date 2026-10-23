// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package archivetest builds the small package archives (wheels, sdists, npm
// tarballs, crates, nupkgs) that license-reading tests feed to upload handlers.
package archivetest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"testing"
)

// Member is one file of an archive.
type Member struct{ Name, Body string }

// File returns a Member.
func File(name, body string) Member { return Member{Name: name, Body: body} }

// Zip builds a zip archive (wheel, nupkg, jar, zip sdist).
func Zip(tb testing.TB, members ...Member) []byte {
	tb.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, m := range members {
		w, err := zw.Create(m.Name)
		if err != nil {
			tb.Fatal(err)
		}

		if _, err := w.Write([]byte(m.Body)); err != nil {
			tb.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// TarGz builds a gzip-compressed tar archive (sdist, npm tarball, crate).
func TarGz(tb testing.TB, members ...Member) []byte {
	tb.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, m := range members {
		hdr := &tar.Header{Name: m.Name, Mode: 0o644, Size: int64(len(m.Body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			tb.Fatal(err)
		}

		if _, err := tw.Write([]byte(m.Body)); err != nil {
			tb.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		tb.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// Gem builds a .gem file whose metadata.gz holds the given YAML gemspec.
func Gem(tb testing.TB, gemspecYAML string) []byte {
	tb.Helper()

	var meta bytes.Buffer

	gz := gzip.NewWriter(&meta)
	if _, err := gz.Write([]byte(gemspecYAML)); err != nil {
		tb.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		tb.Fatal(err)
	}

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "metadata.gz", Mode: 0o644, Size: int64(meta.Len()), Typeflag: tar.TypeReg}); err != nil {
		tb.Fatal(err)
	}

	if _, err := tw.Write(meta.Bytes()); err != nil {
		tb.Fatal(err)
	}

	if err := tw.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}
