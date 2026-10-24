// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"mime/multipart"

	"github.com/openartifact/openartifact/internal/iocopy"
)

// Part is an uploaded multipart file part read in place from the parser's own
// storage (memory below the ParseMultipartForm threshold, a temp file the
// server removes after the request otherwise), hashed in one pass. Handlers
// used to copy it into a second buffer before parsing and storing it.
type Part struct {
	file   multipart.File
	size   int64
	sha256 string
}

// HashPart hashes file, whose size is the multipart header's, with SHA-256 and
// every extra hash. A file shorter or longer than size is an error. The caller
// closes the Part, which closes file; on error file is closed here.
func HashPart(file multipart.File, size int64, extra ...hash.Hash) (*Part, error) {
	p := &Part{file: file, size: size}
	sum := sha256.New()

	writers := make([]io.Writer, 0, 1+len(extra))
	writers = append(writers, sum)

	for _, h := range extra {
		writers = append(writers, h)
	}

	if n, err := iocopy.Copy(io.MultiWriter(writers...), p.Reader()); err != nil || n != size {
		_ = file.Close()

		if err == nil {
			err = io.ErrUnexpectedEOF
		}

		return nil, fmt.Errorf("read multipart file (%d of %d bytes): %w", n, size, err)
	}

	p.sha256 = hex.EncodeToString(sum.Sum(nil))

	return p, nil
}

// Size is the number of bytes in the part.
func (p *Part) Size() int64 { return p.size }

// SHA256Hex is the lower-case hex SHA-256 of the part.
func (p *Part) SHA256Hex() string { return p.sha256 }

// Reader returns an independent reader over the whole part.
func (p *Part) Reader() *io.SectionReader { return io.NewSectionReader(p.file, 0, p.size) }

// Close releases the part.
func (p *Part) Close() { _ = p.file.Close() }

// ReaderAtLimited returns r as an io.ReaderAt and its size without copying
// when r already is one (*io.SectionReader, *bytes.Reader: it must be unread,
// as their size ignores the read position); any other reader is read to its end
// into memory, failing with ErrContentTooLarge past limit bytes. Zip parsers need
// random access, so they use this instead of always buffering.
func ReaderAtLimited(r io.Reader, limit int64) (io.ReaderAt, int64, error) {
	if sized, ok := r.(interface {
		io.ReaderAt
		Size() int64
	}); ok {
		if sized.Size() > limit {
			return nil, 0, fmt.Errorf("%w: limit is %d bytes", ErrContentTooLarge, limit)
		}

		return sized, sized.Size(), nil
	}

	data, err := ReadAllLimited(r, limit)
	if err != nil {
		return nil, 0, err
	}

	return bytes.NewReader(data), int64(len(data)), nil
}
