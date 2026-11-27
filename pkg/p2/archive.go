// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package p2

import (
	"archive/zip"
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/M0Rf30/pkgformat/xzlimit"
	"github.com/ulikunitz/xz"

	"github.com/openartifact/openartifact/internal/limitio"
)

const (
	// maxJarEntries bounds the entries of a metadata jar. A p2 jar holds the one
	// XML file; the bound is checked against the end-of-central-directory
	// record before the zip reader allocates a table for it.
	maxJarEntries = 16

	// eocdLen is the size of the end-of-central-directory record without its
	// comment, and eocdSearch how far from the end of the file a reader looks for it.
	eocdLen    = 22
	eocdSearch = 1<<16 + eocdLen
)

var (
	// ErrBadArchive is returned for a jar that is not the plain archive of one
	// XML document.
	ErrBadArchive = errors.New("p2: unusable metadata archive")
	// ErrNoEntry is returned when a jar lacks the XML entry its name promises.
	ErrNoEntry = errors.New("p2: archive has no document entry")
)

// Rewrite decodes the document d stored in src (size bytes), rewrites it with
// RewriteXML and writes it to dst in the same encoding. A jar is rewritten into
// a jar of one deflated entry; an xz document into one xz stream.
//
// Hardening: the decoded XML is bounded by MaxXMLBytes whatever the encoding
// declares (a bomb is refused, not half-read), a jar is refused when its
// directory announces more than maxJarEntries entries (before anything is
// allocated for them) or has no entry of the document's name, and an xz stream
// that asks for a dictionary over the pkgformat limit, or holds more than one
// stream, is refused.
func (d Doc) Rewrite(dst io.Writer, src io.ReaderAt, size int64, opts Options) error {
	switch d.Codec {
	case CodecXML:
		return RewriteXML(dst, limitio.Capped(io.NewSectionReader(src, 0, size), MaxXMLBytes, ErrTooLarge), opts)
	case CodecXZ:
		return d.rewriteXZ(dst, io.NewSectionReader(src, 0, size), opts)
	case CodecJar:
		return d.rewriteJar(dst, src, size, opts)
	}

	return fmt.Errorf("%w: unknown encoding", ErrBadArchive)
}

func (d Doc) rewriteXZ(dst io.Writer, src io.Reader, opts Options) error {
	r, err := xzlimit.NewReader(bufio.NewReaderSize(src, 64<<10))
	if err != nil {
		return fmt.Errorf("p2: open xz document: %w", err)
	}

	w, err := xz.NewWriter(dst)
	if err != nil {
		return fmt.Errorf("p2: xz writer: %w", err)
	}

	if err := RewriteXML(w, limitio.Capped(r, MaxXMLBytes, ErrTooLarge), opts); err != nil {
		return err
	}

	return w.Close() //nolint:wrapcheck // the destination's own error
}

func (d Doc) rewriteJar(dst io.Writer, src io.ReaderAt, size int64, opts Options) error {
	if err := checkDirectory(src, size); err != nil {
		return err
	}

	zr, err := zip.NewReader(src, size)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadArchive, err)
	}

	var entry *zip.File

	for _, f := range zr.File {
		if f.Name == d.Entry() {
			entry = f
			break
		}
	}

	if entry == nil {
		return fmt.Errorf("%w: %s", ErrNoEntry, d.Entry())
	}

	if entry.UncompressedSize64 > uint64(MaxXMLBytes) {
		return ErrTooLarge
	}

	rc, err := entry.Open()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadArchive, err)
	}
	defer func() { _ = rc.Close() }()

	zw := zip.NewWriter(dst)

	w, err := zw.CreateHeader(&zip.FileHeader{Name: d.Entry(), Method: zip.Deflate, Modified: entry.Modified})
	if err != nil {
		return fmt.Errorf("p2: write jar: %w", err)
	}

	if err := RewriteXML(w, limitio.Capped(rc, MaxXMLBytes, ErrTooLarge), opts); err != nil {
		return err
	}

	return zw.Close() //nolint:wrapcheck // the destination's own error
}

// checkDirectory reads the end-of-central-directory record of the zip archive in
// src and refuses it when it announces more than maxJarEntries entries (the zip
// reader allocates a table for as many as the record claims, so a small hostile
// file could otherwise demand gigabytes) or uses zip64, which a p2 jar never
// needs. It finds the record the way archive/zip does: the last signature in
// the tail of the file.
func checkDirectory(src io.ReaderAt, size int64) error {
	n := min(size, eocdSearch)
	if n < eocdLen {
		return fmt.Errorf("%w: too short", ErrBadArchive)
	}

	tail := make([]byte, n)
	if _, err := src.ReadAt(tail, size-n); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: %w", ErrBadArchive, err)
	}

	for i := len(tail) - eocdLen; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) != 0x06054b50 {
			continue
		}

		comment := int(binary.LittleEndian.Uint16(tail[i+20:]))
		if comment+eocdLen+i > len(tail) {
			return fmt.Errorf("%w: truncated comment", ErrBadArchive)
		}

		if entries := binary.LittleEndian.Uint16(tail[i+10:]); entries > maxJarEntries {
			return fmt.Errorf("%w: %d entries", ErrBadArchive, entries)
		}

		return nil
	}

	return fmt.Errorf("%w: no directory", ErrBadArchive)
}
