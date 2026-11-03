// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package httpcompress

import (
	"io"
	"sync"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
)

// encoder is the part of a compressor the response writer drives.
type encoder interface {
	io.Writer
	Flush() error
	Close() error
	Reset(w io.Writer)
}

// Encoders carry hundreds of KiB of state (hash tables, windows): they are
// pooled, never built per request. Both run at their fastest level: these are
// small text documents compressed on the request path, where CPU time per byte
// matters more than the last few percent of ratio.
//
//nolint:gochecknoglobals // process-wide encoder pools
var (
	gzipPool = sync.Pool{New: func() any {
		zw, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return zw
	}}

	zstdPool = sync.Pool{New: func() any {
		// One goroutine per encoder and a small window: the encoder must not
		// fan out per request, and 256 KiB covers any metadata document.
		zw, _ := zstd.NewWriter(io.Discard,
			zstd.WithEncoderLevel(zstd.SpeedFastest),
			zstd.WithEncoderConcurrency(1),
			zstd.WithWindowSize(256<<10),
			zstd.WithLowerEncoderMem(true),
		)

		return zw
	}}
)

// getEncoder returns a pooled encoder writing to w.
func getEncoder(c coding, w io.Writer) encoder {
	var enc encoder

	switch c {
	case codingZstd:
		enc, _ = zstdPool.Get().(encoder)
	case codingGzip:
		enc, _ = gzipPool.Get().(encoder)
	case codingNone:
		return nil
	}

	enc.Reset(w)

	return enc
}

// putEncoder returns enc to its pool, detached from the response it wrote.
func putEncoder(c coding, enc encoder) {
	enc.Reset(io.Discard)

	switch c {
	case codingZstd:
		zstdPool.Put(enc)
	case codingGzip:
		gzipPool.Put(enc)
	case codingNone:
	}
}
