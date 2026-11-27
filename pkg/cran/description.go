// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cran

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// maxScanBytes bounds the decompressed bytes read while looking for DESCRIPTION
// in a package tarball (a zip bomb guard; a large source package is a few
// hundred MiB unpacked).
const maxScanBytes = 2 << 30

// maxZipEntries bounds the central directory entries examined in a zip.
const maxZipEntries = 1 << 20

// ErrUnsupportedExt is returned for a package file extension other than .tar.gz, .tgz or .zip.
var ErrUnsupportedExt = errors.New("cran: unsupported package extension")

// ErrNoDescription marks a package archive without a DESCRIPTION file in its
// top-level package directory.
var ErrNoDescription = errors.New("cran: package archive has no DESCRIPTION")

// ReadDescription reads <dir>/DESCRIPTION out of a package archive of size
// bytes: a ".tar.gz" or ".tgz" tarball, or a ".zip" (ext selects). dir is the
// archive's top-level directory, which for a well-formed package is its name.
func ReadDescription(ra io.ReaderAt, size int64, ext string) (string, Record, error) {
	switch ext {
	case ".zip":
		return readZipDescription(ra, size)
	case ".tar.gz", ".tgz":
		return readTarDescription(io.NewSectionReader(ra, 0, size))
	}

	return "", nil, fmt.Errorf("%w %q", ErrUnsupportedExt, ext)
}

// descriptionDir returns the top-level directory of a "<dir>/DESCRIPTION"
// member name.
func descriptionDir(name string) (string, bool) {
	name = strings.TrimPrefix(path.Clean(name), "./")

	dir, file, ok := strings.Cut(name, "/")

	return dir, ok && file == "DESCRIPTION" && dir != "" && dir != ".."
}

func readTarDescription(r io.Reader) (string, Record, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return "", nil, fmt.Errorf("cran: package is not a gzip tarball: %w", err)
	}

	defer func() { _ = zr.Close() }()

	tr := tar.NewReader(io.LimitReader(zr, maxScanBytes))

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", nil, ErrNoDescription
		}

		if err != nil {
			return "", nil, fmt.Errorf("cran: read package tarball: %w", err)
		}

		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		dir, ok := descriptionDir(hdr.Name)
		if !ok {
			continue
		}

		rec, err := ParseDescription(tr)
		if err != nil {
			return "", nil, err
		}

		return dir, rec, nil
	}
}

func readZipDescription(ra io.ReaderAt, size int64) (string, Record, error) {
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return "", nil, fmt.Errorf("cran: package is not a zip archive: %w", err)
	}

	if len(zr.File) > maxZipEntries {
		return "", nil, ErrTooLarge
	}

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}

		dir, ok := descriptionDir(f.Name)
		if !ok {
			continue
		}

		if f.UncompressedSize64 > MaxDescriptionBytes {
			return "", nil, ErrTooLarge
		}

		rc, err := f.Open()
		if err != nil {
			return "", nil, fmt.Errorf("cran: open DESCRIPTION: %w", err)
		}

		rec, err := ParseDescription(rc)
		_ = rc.Close()

		if err != nil {
			return "", nil, err
		}

		return dir, rec, nil
	}

	return "", nil, ErrNoDescription
}
