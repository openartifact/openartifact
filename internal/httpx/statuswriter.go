// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"io"
	"net/http"

	"github.com/openartifact/openartifact/internal/iocopy"
)

// StatusWriter is the response-writer wrapper the middleware chain shares: it
// records the status code the handler chose and is otherwise transparent.
//
// Every wrapper in the chain that hides a method of the writer underneath
// costs something real. ReadFrom is the one that matters: net/http's response
// implements io.ReaderFrom and serves an *os.File with sendfile(2), but only
// when the writer handed to iocopy.Copy still exposes it. StatusWriter
// forwards ReadFrom, Flush and (through Unwrap, for http.ResponseController)
// Hijack, SetWriteDeadline and EnableFullDuplex.
type StatusWriter struct {
	http.ResponseWriter

	status int
}

// WrapStatus returns w as a StatusWriter. A writer that already is one is
// returned as is, so the layers that all want the status code (metrics,
// tracing, access log, audit) share a single wrapper per request.
func WrapStatus(w http.ResponseWriter) *StatusWriter {
	if sw, ok := w.(*StatusWriter); ok {
		return sw
	}

	return &StatusWriter{ResponseWriter: w, status: http.StatusOK}
}

// Status is the status code written so far, 200 while none was.
func (w *StatusWriter) Status() int { return w.status }

// SetStatus overrides the recorded status without touching the response, for a
// layer that knows what a later layer will answer (a panic becomes a 500).
func (w *StatusWriter) SetStatus(code int) { w.status = code }

// WriteHeader records code and forwards it.
func (w *StatusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *StatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush forwards to the underlying writer when it can flush.
func (w *StatusWriter) Flush() { Flush(w.ResponseWriter) }

// Flush flushes w when it, or a writer it wraps, supports it.
func Flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()

		return
	}

	_ = http.NewResponseController(w).Flush()
}

// ReadFrom forwards to the underlying writer's ReadFrom, which is what lets a
// file be sent with sendfile(2). A writer underneath without ReadFrom gets a
// pooled copy through Write.
func (w *StatusWriter) ReadFrom(r io.Reader) (int64, error) {
	return ReadFrom(w.ResponseWriter, r)
}

// ReadFrom copies r to w, through w's own ReadFrom when it has one (the
// net/http response does, and uses sendfile for a file) and through a pooled
// buffer otherwise. Response-writer wrappers call it from their ReadFrom after
// they have done their header bookkeeping.
func ReadFrom(w http.ResponseWriter, r io.Reader) (int64, error) {
	if rf, ok := w.(io.ReaderFrom); ok {
		return rf.ReadFrom(r) //nolint:wrapcheck // transparent writer wrapper
	}

	return iocopy.Copy(struct{ io.Writer }{w}, r) //nolint:wrapcheck // transparent writer wrapper
}
