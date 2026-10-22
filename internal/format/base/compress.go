// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"

	"github.com/klauspost/compress/gzip"
)

// gzipPool recycles encoders: a gzip.Writer carries hundreds of KiB of state.
// BestSpeed keeps the CPU cost of compressing a per-request body low while
// still shrinking text metadata 5-20x.
//
//nolint:gochecknoglobals // process-wide encoder pool
var gzipPool = sync.Pool{New: func() any {
	zw, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
	return zw
}}

// Compress wraps a metadata handler so its text response (JSON, YAML, XML,
// HTML, plain text) is gzip-encoded when the client sends Accept-Encoding:
// gzip. Only 200 responses of a textual Content-Type that are not already
// encoded are compressed; blobs, redirects, 304s and errors pass through
// untouched, as does any request carrying Range or using a method other than
// GET. Apply it to metadata routes only, never to artifact downloads.
func Compress(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !acceptsGzip(r) {
			next(w, r)
			return
		}

		cw := &compressWriter{ResponseWriter: w}
		defer cw.finish()

		next(cw, r)
	}
}

type compressWriter struct {
	http.ResponseWriter

	zw          *gzip.Writer
	wroteHeader bool
}

func (c *compressWriter) WriteHeader(code int) {
	if c.wroteHeader {
		return
	}

	c.wroteHeader = true

	h := c.Header()
	if code == http.StatusOK && h.Get("Content-Encoding") == "" && compressibleType(h.Get("Content-Type")) && smallEnough(h) {
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		weakenETag(h)

		c.zw, _ = gzipPool.Get().(*gzip.Writer)
		c.zw.Reset(c.ResponseWriter)
	}

	c.ResponseWriter.WriteHeader(code)
}

func (c *compressWriter) Write(p []byte) (int, error) {
	if !c.wroteHeader {
		// net/http sniffs the type of an untyped body; do the same before
		// deciding whether it is compressible.
		if c.Header().Get("Content-Type") == "" {
			c.Header().Set("Content-Type", http.DetectContentType(p))
		}

		c.WriteHeader(http.StatusOK)
	}

	if c.zw != nil {
		return c.zw.Write(p) //nolint:wrapcheck // transparent writer wrapper
	}

	return c.ResponseWriter.Write(p) //nolint:wrapcheck // transparent writer wrapper
}

// Flush flushes the encoder and the underlying writer.
func (c *compressWriter) Flush() {
	if c.zw != nil {
		_ = c.zw.Flush()
	}

	_ = http.NewResponseController(c.ResponseWriter).Flush()
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (c *compressWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *compressWriter) finish() {
	if c.zw == nil {
		return
	}

	_ = c.zw.Close()

	c.zw.Reset(io.Discard)
	gzipPool.Put(c.zw)
	c.zw = nil
}

// smallEnough reports whether a body is worth compressing: a handler that
// declared a tiny Content-Length is left alone.
func smallEnough(h http.Header) bool {
	cl := h.Get("Content-Length")
	if cl == "" {
		return true
	}

	n := 0

	for _, ch := range cl {
		if ch < '0' || ch > '9' {
			return true
		}

		n = n*10 + int(ch-'0')
		if n >= minCompressBytes {
			return true
		}
	}

	return n >= minCompressBytes
}

// compressibleType reports whether a Content-Type names a text format that
// gzip shrinks well.
func compressibleType(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}

	switch {
	case strings.HasPrefix(mt, "text/"):
		return true
	case strings.HasSuffix(mt, "+json"), strings.HasSuffix(mt, "+xml"), strings.HasSuffix(mt, "+yaml"):
		return true
	}

	switch mt {
	case "application/json", "application/xml", "application/yaml", "application/x-yaml", "application/javascript":
		return true
	}

	return false
}
