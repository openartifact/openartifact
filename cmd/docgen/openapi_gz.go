// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// apiSpecGzFile is the gzip form of apiSpecFile that internal/api embeds (the
// binary carries 84 KB instead of 834 KB). It is generated: the source stays
// the readable openapi.json, which the web client generator and the fuzzers read.
const apiSpecGzFile = "internal/api/openapi.json.gz"

// syncSpecGzip makes apiSpecGzFile the gzip form of apiSpecFile. With write it
// rewrites a stale file; either way it reports whether the file was stale. It
// compares the inflated content, not the bytes, so a different Go release that
// compresses differently does not touch a current file.
func syncSpecGzip(root string, write bool) (stale bool, err error) {
	spec, err := os.ReadFile(filepath.Join(root, apiSpecFile))
	if err != nil {
		return false, err
	}

	path := filepath.Join(root, apiSpecGzFile)

	have, err := inflateFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		// An unreadable gzip is stale like a missing one.
		have = nil
	}

	if bytes.Equal(have, spec) {
		return false, nil
	}

	if !write {
		return true, nil
	}

	var buf bytes.Buffer

	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return true, err
	}

	if _, err := zw.Write(spec); err != nil {
		return true, err
	}

	if err := zw.Close(); err != nil {
		return true, err
	}

	return true, os.WriteFile(path, buf.Bytes(), 0o644) //nolint:gosec // committed source file
}

func inflateFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer zr.Close()

	return io.ReadAll(zr)
}
