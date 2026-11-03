// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package httpcompress encodes compressible HTTP responses (JSON, XML, YAML,
// plain text) with zstd or gzip, as the client's Accept-Encoding allows.
//
// It is for metadata and API documents, never for artifact bytes: the
// middleware is mounted on routes that serve documents, and it additionally
// leaves alone every response that is a download (Content-Disposition), is
// already encoded or ranged, is not of a text type, or is smaller than
// MinSize.
//
// The wrapped writer is transparent: it keeps io.ReaderFrom (so a response it
// leaves alone is still sent with sendfile(2)), http.Flusher and, through
// Unwrap, everything http.ResponseController reaches (Hijack, deadlines).
package httpcompress

import (
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/openartifact/openartifact/internal/httpx"
	"github.com/openartifact/openartifact/internal/iocopy"
)

// MinSize is the smallest body worth compressing: below it the framing and
// the CPU cost outweigh the savings, and a small response is faster sent as is.
const MinSize = 1024

// Middleware compresses the compressible responses of next for clients that
// accept an encoding.
//
// Semantics, per RFC 9110 and 9111:
//   - Only 200 responses of a text type (text/*, +json, +xml, +yaml,
//     application/json, ...) that carry no Content-Encoding, Content-Range,
//     Content-Disposition or "Cache-Control: no-transform" are encoded.
//   - Requests with Range or Upgrade, and HEAD requests, pass through
//     untouched: a range addresses the identity representation, and a HEAD
//     answer cannot know the encoded length.
//   - A body below MinSize (by Content-Length, or once the handler is done) is
//     sent as is. Responses the handler flushes are streamed encoded.
//   - An encoded response drops Content-Length and Accept-Ranges, gets
//     "Vary: Accept-Encoding" and, when it has an entity tag, a weak one: the
//     encoded and the plain bytes differ, so the strong tag no longer names
//     them. If-None-Match uses weak comparison, so a client revalidating with
//     the weak tag still gets its 304 from the handler.
//   - "Vary: Accept-Encoding" is added to every response whose representation
//     depends on the header, including the plain ones, so no cache can hand an
//     encoded body to a client that did not ask for it or the reverse.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !negotiable(r) {
			next.ServeHTTP(w, r)

			return
		}

		c := negotiate(r.Header.Get("Accept-Encoding"))
		if c == codingNone {
			// Nothing to encode, but the answer still depends on the header.
			addVary(w.Header())
			next.ServeHTTP(w, r)

			return
		}

		cw := &responseWriter{ResponseWriter: w, coding: c}
		defer cw.release()

		next.ServeHTTP(cw, r)
		cw.finish()
	})
}

// Func is Middleware for a handler function that serves documents on GET only;
// every other method goes straight to next.
func Func(next http.HandlerFunc) http.HandlerFunc {
	h := Middleware(next)

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next(w, r)

			return
		}

		h.ServeHTTP(w, r)
	}
}

// negotiable reports whether the request can receive an encoded response.
func negotiable(r *http.Request) bool {
	switch r.Method {
	case http.MethodHead, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return false
	}

	return r.Header.Get("Range") == "" && r.Header.Get("Upgrade") == ""
}

type state uint8

const (
	// undecided: nothing is committed yet; a prefix of the body may be held in
	// buf while its size is unknown.
	undecided state = iota
	plain
	encoded
)

// bufPool holds the small buffers that collect the first bytes of a body of
// unknown size until MinSize is known to be reached.
//
//nolint:gochecknoglobals // process-wide buffer pool
var bufPool = sync.Pool{New: func() any {
	b := make([]byte, 0, MinSize)
	return &b
}}

type responseWriter struct {
	http.ResponseWriter

	coding coding
	state  state
	status int     // status the handler chose; 0 = none yet
	buf    *[]byte // body prefix collected while undecided
	enc    encoder

	// vary: the response is of a compressible type, so its representation
	// depends on Accept-Encoding whether or not it ends up encoded.
	vary bool
}

// WriteHeader records the status. Anything but 200 cannot be encoded and is
// committed at once; a 200 waits for its body, whose size decides.
func (w *responseWriter) WriteHeader(code int) {
	if w.state != undecided {
		return
	}

	// Informational responses (100, 103) precede the final one.
	if code >= http.StatusContinue && code < http.StatusOK && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code)

		return
	}

	if w.status != 0 {
		return
	}

	w.status = code

	if code != http.StatusOK {
		// A 304 stands in for the 200 it validates: it carries that
		// response's Vary.
		if code == http.StatusNotModified && w.Header().Get("ETag") != "" {
			addVary(w.Header())
		}

		w.commitPlain()
	}
}

// Write sends p, encoded or not. The first call decides; a body of unknown size
// is held back until it reaches MinSize or the handler returns.
func (w *responseWriter) Write(p []byte) (int, error) {
	switch w.state {
	case plain:
		return w.ResponseWriter.Write(p) //nolint:wrapcheck // transparent writer wrapper
	case encoded:
		return w.enc.Write(p) //nolint:wrapcheck // transparent writer wrapper
	case undecided:
	}

	if w.status == 0 {
		w.status = http.StatusOK
	}

	h := w.Header()

	ct := h.Get("Content-Type")
	if ct == "" {
		// net/http sniffs the type of an untyped body from its first bytes;
		// decide on the same evidence.
		ct = http.DetectContentType(p)
	}

	if !w.eligible(h, ct) {
		w.commitPlain()

		return w.ResponseWriter.Write(p) //nolint:wrapcheck // transparent writer wrapper
	}

	switch n := declaredLength(h); {
	case n >= MinSize:
		w.commitEncoded(ct)
	case n >= 0:
		w.commitPlain()

		return w.ResponseWriter.Write(p) //nolint:wrapcheck // transparent writer wrapper
	default:
		if w.buf == nil {
			w.buf, _ = bufPool.Get().(*[]byte)
		}

		if len(*w.buf)+len(p) < MinSize {
			*w.buf = append(*w.buf, p...)

			return len(p), nil
		}

		w.commitEncoded(ct)
	}

	return w.enc.Write(p) //nolint:wrapcheck // transparent writer wrapper
}

// ReadFrom copies r into the response. A response left plain keeps sendfile(2)
// reachable; an encoded one is compressed as it streams.
func (w *responseWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.state == undecided {
		w.decideStreaming()
	}

	if w.state == plain {
		return httpx.ReadFrom(w.ResponseWriter, r) //nolint:wrapcheck // transparent writer wrapper
	}

	return iocopy.Copy(struct{ io.Writer }{w.enc}, r) //nolint:wrapcheck // transparent writer wrapper
}

// Flush pushes buffered output to the client; a response that is flushed is a
// stream, so it is encoded (when eligible) without waiting for a size.
func (w *responseWriter) Flush() {
	if w.state == undecided {
		w.decideStreaming()
	}

	if w.state == encoded {
		_ = w.enc.Flush()
	}

	httpx.Flush(w.ResponseWriter)
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// decideStreaming commits the response when its body arrives as a stream of
// unknown size (ReadFrom, Flush).
func (w *responseWriter) decideStreaming() {
	if w.status == 0 {
		w.status = http.StatusOK
	}

	h := w.Header()

	// Without bytes to sniff, an untyped body is left alone.
	ct := h.Get("Content-Type")

	eligible := ct != "" && w.eligible(h, ct)

	if n := declaredLength(h); eligible && (n < 0 || n >= MinSize) {
		w.commitEncoded(ct)

		return
	}

	w.commitPlain()
}

// eligible reports whether a 200 response of the given type may be encoded. It
// also records whether the type makes the representation vary.
func (w *responseWriter) eligible(h http.Header, contentType string) bool {
	if w.status != http.StatusOK || !compressibleType(contentType) {
		return false
	}

	w.vary = true

	if h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" || h.Get("Content-Disposition") != "" {
		return false
	}

	return !hasToken(h.Get("Cache-Control"), "no-transform")
}

// commitEncoded starts the encoded response: headers first, then the held
// prefix of the body through the encoder.
func (w *responseWriter) commitEncoded(contentType string) {
	h := w.Header()

	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", contentType)
	}

	h.Del("Content-Length")
	h.Del("Accept-Ranges")
	h.Set("Content-Encoding", w.coding.token())
	addVary(h)
	weakenETag(h)

	w.ResponseWriter.WriteHeader(w.status)

	w.enc = getEncoder(w.coding, w.ResponseWriter)
	w.state = encoded

	if w.buf != nil {
		_, _ = w.enc.Write(*w.buf)

		w.releaseBuf()
	}
}

// commitPlain sends the response as it is, with the held prefix of its body.
func (w *responseWriter) commitPlain() {
	if w.vary {
		addVary(w.Header())
	}

	w.ResponseWriter.WriteHeader(w.status)

	w.state = plain

	if w.buf != nil {
		_, _ = w.ResponseWriter.Write(*w.buf)

		w.releaseBuf()
	}
}

// finish completes the response when the handler returns.
func (w *responseWriter) finish() {
	switch w.state {
	case undecided:
		// A body that stayed under MinSize, or a bare status.
		if w.status != 0 || w.buf != nil {
			if w.status == 0 {
				w.status = http.StatusOK
			}

			w.commitPlain()
		}
	case encoded:
		_ = w.enc.Close()

		putEncoder(w.coding, w.enc)
		w.enc = nil
	case plain:
	}
}

// release returns what the writer still holds to its pools. It runs even when
// the handler panicked; the response is then not completed here, so the
// recovery middleware can still answer.
func (w *responseWriter) release() {
	w.releaseBuf()

	if w.enc != nil {
		putEncoder(w.coding, w.enc)
		w.enc = nil
	}
}

func (w *responseWriter) releaseBuf() {
	if w.buf == nil {
		return
	}

	*w.buf = (*w.buf)[:0]
	bufPool.Put(w.buf)
	w.buf = nil
}

// declaredLength is the Content-Length the handler announced, -1 when none or
// not a number.
func declaredLength(h http.Header) int64 {
	v := h.Get("Content-Length")
	if v == "" {
		return -1
	}

	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return -1
	}

	return n
}

// addVary adds Accept-Encoding to Vary unless it is already named.
func addVary(h http.Header) {
	for _, v := range h.Values("Vary") {
		if hasToken(v, "Accept-Encoding") || hasToken(v, "*") {
			return
		}
	}

	h.Add("Vary", "Accept-Encoding")
}

// weakenETag turns a strong entity tag into a weak one: the encoded body is
// not byte-identical to the plain one the tag was computed for.
func weakenETag(h http.Header) {
	if et := h.Get("ETag"); et != "" && !strings.HasPrefix(et, "W/") {
		h.Set("ETag", "W/"+et)
	}
}

// hasToken reports whether the comma-separated header value lists token
// (case-insensitively, ignoring parameters).
func hasToken(value, token string) bool {
	for value != "" {
		var part string

		part, value, _ = strings.Cut(value, ",")
		part, _, _ = strings.Cut(part, "=")

		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}

	return false
}

// compressibleType reports whether a Content-Type names a text format that
// compresses well.
func compressibleType(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}

	switch {
	case mt == "text/event-stream":
		// Server-sent events are flushed event by event and consumed
		// incrementally; encoding buys nothing and delays them.
		return false
	case strings.HasPrefix(mt, "text/"):
		return true
	case strings.HasSuffix(mt, "+json"), strings.HasSuffix(mt, "+xml"), strings.HasSuffix(mt, "+yaml"):
		return true
	}

	switch mt {
	case "application/json", "application/xml", "application/yaml", "application/x-yaml",
		"application/javascript", "application/x-ndjson":
		return true
	}

	return false
}
