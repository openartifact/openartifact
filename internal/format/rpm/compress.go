// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"

	"github.com/klauspost/compress/gzip"
)

// CompressedMetadata is one repodata document compressed for repomd.xml.
type CompressedMetadata struct {
	GzipData []byte
	GzSHA256 string
	GzSize   int64
	OpenSize int64
}

// gzipLevel trades a little size for a lot of CPU: on real primary, filelists
// and other documents level 6 compresses at 3-10x the speed of level 9 and
// produces output about 1-5 % larger. dnf reads only the gzip variant that
// repomd.xml lists, so no other encoding is produced.
const gzipLevel = 6

// hashedBuffer collects compressed output and hashes it on the way.
type hashedBuffer struct {
	buf bytes.Buffer
	h   hash.Hash
}

func newHashedBuffer() *hashedBuffer { return &hashedBuffer{h: sha256.New()} }

func (b *hashedBuffer) Write(p []byte) (int, error) {
	b.h.Write(p)

	return b.buf.Write(p)
}

// countingWriter counts the bytes that pass through it.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)

	return n, err
}

// CompressMetadataStream runs write against a sink that feeds the gzip encoder,
// so the uncompressed document never has to exist in memory. OpenSize is the
// number of uncompressed bytes write produced.
func CompressMetadataStream(write func(io.Writer) error) (*CompressedMetadata, error) {
	gzBuf := newHashedBuffer()

	gw, err := gzip.NewWriterLevel(gzBuf, gzipLevel)
	if err != nil {
		return nil, fmt.Errorf("rpm: gzip compression: %w", err)
	}

	src := &countingWriter{w: gw}

	if err := write(src); err != nil {
		return nil, fmt.Errorf("rpm: writing xml: %w", err)
	}

	if src.n == 0 {
		return nil, errors.New("rpm: empty xml data") //nolint:err113 // static error
	}

	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("rpm: gzip compression: %w", err)
	}

	return &CompressedMetadata{
		GzipData: gzBuf.buf.Bytes(),
		GzSHA256: hex.EncodeToString(gzBuf.h.Sum(nil)),
		GzSize:   int64(gzBuf.buf.Len()),
		OpenSize: src.n,
	}, nil
}
