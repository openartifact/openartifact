// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"

	"github.com/openartifact/openartifact/internal/xzutil"
)

type CompressedMetadata struct {
	GzipData []byte
	XZData   []byte
	GzSHA256 string
	XZSHA256 string
	GzSize   int64
	XZSize   int64
	OpenSize int64
}

// CompressMetadata compresses an in-memory XML document; see
// CompressMetadataStream for the streaming form.
func CompressMetadata(xmlData []byte) (*CompressedMetadata, error) {
	if len(xmlData) == 0 {
		return nil, errors.New("rpm: empty xml data") //nolint:err113 // static error
	}

	return CompressMetadataStream(func(w io.Writer) error {
		_, err := w.Write(xmlData)
		return err
	}, len(xmlData))
}

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

// CompressMetadataStream runs write against a sink that feeds the gzip and xz
// encoders at once, so the uncompressed document never has to exist in memory.
// OpenSize is the number of uncompressed bytes write produced. sizeHint is the
// expected uncompressed size; it only sizes the xz dictionary (a low guess costs
// a little compression ratio, never correctness).
func CompressMetadataStream(write func(io.Writer) error, sizeHint int) (*CompressedMetadata, error) {
	gzBuf, xzBuf := newHashedBuffer(), newHashedBuffer()

	gw, err := gzip.NewWriterLevel(gzBuf, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("rpm: gzip compression: %w", err)
	}

	xw, err := xzutil.NewWriter(xzBuf, sizeHint)
	if err != nil {
		return nil, fmt.Errorf("rpm: xz compression: %w", err)
	}

	src := &countingWriter{w: io.MultiWriter(gw, xw)}

	if err := write(src); err != nil {
		return nil, fmt.Errorf("rpm: writing xml: %w", err)
	}

	if src.n == 0 {
		return nil, errors.New("rpm: empty xml data") //nolint:err113 // static error
	}

	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("rpm: gzip compression: %w", err)
	}

	if err := xw.Close(); err != nil {
		return nil, fmt.Errorf("rpm: xz compression: %w", err)
	}

	return &CompressedMetadata{
		GzipData: gzBuf.buf.Bytes(),
		XZData:   xzBuf.buf.Bytes(),
		GzSHA256: hex.EncodeToString(gzBuf.h.Sum(nil)),
		XZSHA256: hex.EncodeToString(xzBuf.h.Sum(nil)),
		GzSize:   int64(gzBuf.buf.Len()),
		XZSize:   int64(xzBuf.buf.Len()),
		OpenSize: src.n,
	}, nil
}
