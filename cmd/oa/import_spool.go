// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"io"
	"os"
)

// spooled is a source body copied to a temporary file, for formats that must
// read an archive's own metadata (package.json, Cargo.toml, PKG-INFO, ...) and
// then upload the same bytes. Memory use stays flat; the disk holds one copy of
// the file being transferred per concurrent transfer.
type spooled struct {
	f    *os.File
	Size int64
}

// spool reads body to the end into a temporary file. The caller must Close it.
func spool(body io.Reader) (*spooled, error) {
	f, err := os.CreateTemp("", "oa-import-*")
	if err != nil {
		return nil, fmt.Errorf("spooling source file: %w", err)
	}

	n, err := io.Copy(f, body)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())

		return nil, fmt.Errorf("spooling source file: %w", err)
	}

	return &spooled{f: f, Size: n}, nil
}

// Reader returns a fresh reader over the whole spooled content.
func (s *spooled) Reader() *io.SectionReader { return io.NewSectionReader(s.f, 0, s.Size) }

// Close closes and removes the temporary file.
func (s *spooled) Close() error {
	err := s.f.Close()
	if rmErr := os.Remove(s.f.Name()); err == nil {
		err = rmErr
	}

	return err
}
