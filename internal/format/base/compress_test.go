// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func serveCompressed(t *testing.T, h http.HandlerFunc, method string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, "/", http.NoBody)
	for k, v := range header {
		req.Header.Set(k, v)
	}

	w := httptest.NewRecorder()
	Compress(h)(w, req)

	return w
}

func writes(contentType string, status int, body []byte, extra map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}

		for k, v := range extra {
			w.Header().Set(k, v)
		}

		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}

func TestCompressEncodesTextMetadata(t *testing.T) {
	body := []byte(strings.Repeat("name: chart\nversion: 1.0.0\n", 400))

	w := serveCompressed(t, writes("application/x-yaml", http.StatusOK, body, map[string]string{"ETag": `"v1"`}), http.MethodGet, map[string]string{"Accept-Encoding": "gzip"})

	if w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("ETag") != `W/"v1"` || !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("headers: %v", w.Header())
	}

	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}

	if got, _ := io.ReadAll(zr); !bytes.Equal(got, body) {
		t.Error("body does not decode to the original")
	}

	if w.Body.Len() >= len(body)/4 {
		t.Errorf("compressed %d -> %d bytes, expected at least 4x for repetitive YAML", len(body), w.Body.Len())
	}
}

// Everything that is not a 200 text document is passed through untouched.
func TestCompressLeavesOtherResponsesAlone(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 4096)
	gzipHdr := map[string]string{"Accept-Encoding": "gzip"}

	cases := []struct {
		name   string
		h      http.HandlerFunc
		method string
		header map[string]string
	}{
		{"blob", writes("application/octet-stream", http.StatusOK, big, nil), http.MethodGet, gzipHdr},
		{"already encoded", writes("application/json", http.StatusOK, big, map[string]string{"Content-Encoding": "gzip"}), http.MethodGet, gzipHdr},
		{"error status", writes("application/json", http.StatusInternalServerError, big, nil), http.MethodGet, gzipHdr},
		{"not modified", writes("application/json", http.StatusNotModified, nil, nil), http.MethodGet, gzipHdr},
		{"tiny declared length", writes("application/json", http.StatusOK, big[:10], map[string]string{"Content-Length": "10"}), http.MethodGet, gzipHdr},
		{"client refuses gzip", writes("application/json", http.StatusOK, big, nil), http.MethodGet, map[string]string{"Accept-Encoding": "gzip;q=0"}},
		{"no Accept-Encoding", writes("application/json", http.StatusOK, big, nil), http.MethodGet, nil},
		{"range request", writes("application/json", http.StatusOK, big, nil), http.MethodGet, map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=0-9"}},
		{"HEAD", writes("application/json", http.StatusOK, big, nil), http.MethodHead, gzipHdr},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := serveCompressed(t, tc.h, tc.method, tc.header)

			enc := w.Header().Get("Content-Encoding")
			if tc.name == "already encoded" {
				if enc != "gzip" || !bytes.Equal(w.Body.Bytes(), big) {
					t.Errorf("re-encoded an encoded response: %q, %d bytes", enc, w.Body.Len())
				}

				return
			}

			if enc != "" {
				t.Errorf("Content-Encoding = %q, want none", enc)
			}

			if tc.method != http.MethodHead && w.Header().Get("Content-Length") != "" && w.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) {
				t.Errorf("Content-Length %q != body %d", w.Header().Get("Content-Length"), w.Body.Len())
			}
		})
	}
}

func TestCompressSniffsUntypedBodies(t *testing.T) {
	// An untyped binary body must not be compressed just because no
	// Content-Type was declared.
	w := serveCompressed(t, writes("", http.StatusOK, append([]byte{0x1f, 0x00, 0x01, 0x02}, make([]byte, 4096)...), nil), http.MethodGet, map[string]string{"Accept-Encoding": "gzip"})
	if w.Header().Get("Content-Encoding") != "" {
		t.Error("compressed an untyped binary body")
	}
}
