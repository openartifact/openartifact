// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testSPA() (http.Handler, fstest.MapFS) {
	text := strings.Repeat("const answer = 42; // compressible\n", 80)
	html := "<!doctype html><html><body>" + strings.Repeat("<div>OpenArtifact</div>", 80) + "</body></html>"
	// Already-compressed formats: bytes that do not shrink under gzip.
	noise := make([]byte, 2048)
	for i := range noise {
		noise[i] = byte(i*131 + i*i*7)
	}

	fsys := fstest.MapFS{
		"index.html":                  {Data: []byte(html)},
		"favicon.svg":                 {Data: []byte("<svg xmlns='http://www.w3.org/2000/svg'>" + strings.Repeat("<path d='M0 0'/>", 80) + "</svg>")},
		"assets/index-AbCd1234.js":    {Data: []byte(text)},
		"assets/index-AbCd1234.css":   {Data: []byte(strings.Repeat("a{color:red}", 100))},
		"assets/logo-ZyXw9876.png":    {Data: append([]byte("\x89PNG\r\n\x1a\n"), noise...)},
		"assets/inter-Qwer5678.woff2": {Data: append([]byte("wOF2"), noise...)},
		"assets/plain.js":             {Data: []byte(text)},
	}

	return newSPAHandler(fsys), fsys
}

func do(h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)

	return rw
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()

	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}

	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}

	return out
}

func TestSPA_AssetHeaders(t *testing.T) {
	h, fsys := testSPA()

	tests := []struct {
		name         string
		path         string
		acceptEnc    string
		wantCache    string
		wantEncoding string
		wantVary     bool
	}{
		{"hashed js gzip", "/assets/index-AbCd1234.js", "gzip, deflate, br", "public, max-age=31536000, immutable", "gzip", true},
		{"hashed js identity", "/assets/index-AbCd1234.js", "", "public, max-age=31536000, immutable", "", true},
		{"hashed js q=0", "/assets/index-AbCd1234.js", "gzip;q=0, identity", "public, max-age=31536000, immutable", "", true},
		{"hashed css gzip", "/assets/index-AbCd1234.css", "gzip", "public, max-age=31536000, immutable", "gzip", true},
		{"unhashed asset is not immutable", "/assets/plain.js", "gzip", "no-cache", "gzip", true},
		{"index.html", "/", "gzip", "no-cache", "gzip", true},
		{"index.html explicit", "/index.html", "gzip", "no-cache", "gzip", true},
		{"spa fallback route", "/repos/demo/packages", "gzip", "no-cache", "gzip", true},
		{"svg root file", "/favicon.svg", "gzip", "no-cache", "gzip", true},
		{"png never compressed", "/assets/logo-ZyXw9876.png", "gzip", "public, max-age=31536000, immutable", "", false},
		{"woff2 never compressed", "/assets/inter-Qwer5678.woff2", "gzip", "public, max-age=31536000, immutable", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hdr := map[string]string{}
			if tc.acceptEnc != "" {
				hdr["Accept-Encoding"] = tc.acceptEnc
			}

			rw := do(h, http.MethodGet, tc.path, hdr)
			if rw.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rw.Code)
			}

			got := rw.Header()
			if got.Get("Cache-Control") != tc.wantCache {
				t.Errorf("Cache-Control = %q, want %q", got.Get("Cache-Control"), tc.wantCache)
			}

			if got.Get("Content-Encoding") != tc.wantEncoding {
				t.Errorf("Content-Encoding = %q, want %q", got.Get("Content-Encoding"), tc.wantEncoding)
			}

			if hasVary := strings.Contains(got.Get("Vary"), "Accept-Encoding"); hasVary != tc.wantVary {
				t.Errorf("Vary = %q, want Accept-Encoding present=%v", got.Get("Vary"), tc.wantVary)
			}

			if got.Get("ETag") == "" || got.Get("Content-Security-Policy") == "" {
				t.Errorf("missing ETag or security headers: %v", got)
			}

			name := strings.TrimPrefix(tc.path, "/")
			if name == "" || !strings.Contains(name, ".") {
				name = "index.html"
			}

			want := fsys[name].Data
			body := rw.Body.Bytes()

			if tc.wantEncoding == "gzip" {
				if len(body) >= len(want) {
					t.Errorf("gzip body %d bytes is not smaller than the original %d", len(body), len(want))
				}

				body = gunzip(t, body)
			}

			if !bytes.Equal(body, want) {
				t.Errorf("decoded body differs from the embedded file")
			}
		})
	}
}

func TestSPA_ConditionalRequests(t *testing.T) {
	h, _ := testSPA()

	for _, path := range []string{"/", "/repos/demo/edit", "/assets/index-AbCd1234.js"} {
		for _, enc := range []string{"gzip", ""} {
			t.Run(path+" enc="+enc, func(t *testing.T) {
				hdr := map[string]string{"Accept-Encoding": enc}
				first := do(h, http.MethodGet, path, hdr)
				etag := first.Header().Get("ETag")

				hdr["If-None-Match"] = etag
				rw := do(h, http.MethodGet, path, hdr)

				if rw.Code != http.StatusNotModified {
					t.Fatalf("matching If-None-Match: status = %d, want 304", rw.Code)
				}

				if rw.Body.Len() != 0 || rw.Header().Get("Content-Encoding") != "" {
					t.Errorf("304 must be bodiless: %d bytes, encoding %q", rw.Body.Len(), rw.Header().Get("Content-Encoding"))
				}

				if rw.Header().Get("ETag") != etag || rw.Header().Get("Cache-Control") != first.Header().Get("Cache-Control") {
					t.Errorf("304 headers = %v, want ETag and Cache-Control of the 200", rw.Header())
				}

				hdr["If-None-Match"] = `"something-else"`
				if rw := do(h, http.MethodGet, path, hdr); rw.Code != http.StatusOK {
					t.Errorf("stale If-None-Match: status = %d, want 200", rw.Code)
				}
			})
		}
	}

	// The gzip and identity representations are different entities.
	gz := do(h, http.MethodGet, "/", map[string]string{"Accept-Encoding": "gzip"}).Header().Get("ETag")
	id := do(h, http.MethodGet, "/", nil).Header().Get("ETag")

	if gz == id {
		t.Errorf("gzip and identity share ETag %q", gz)
	}

	if rw := do(h, http.MethodGet, "/", map[string]string{"If-None-Match": gz}); rw.Code != http.StatusOK {
		t.Errorf("a gzip validator must not revalidate the identity body: status = %d", rw.Code)
	}
}

func TestSPA_HeadAndErrors(t *testing.T) {
	h, _ := testSPA()

	head := do(h, http.MethodHead, "/assets/index-AbCd1234.js", map[string]string{"Accept-Encoding": "gzip"})
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") == "0" || head.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD: status %d, body %d, Content-Length %q", head.Code, head.Body.Len(), head.Header().Get("Content-Length"))
	}

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/assets/missing-Zzzzzzzz.js", http.StatusNotFound},
		{http.MethodGet, "/missing.png", http.StatusNotFound},
		{http.MethodGet, "/api/v1/nope", http.StatusNotFound},
		{http.MethodPost, "/", http.StatusMethodNotAllowed},
	} {
		if rw := do(h, tc.method, tc.path, nil); rw.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rw.Code, tc.want)
		}
	}
}

// TestSPA_RepoPagesWithDottedNames is the regression test for direct loads of
// package pages whose name has a file extension (a raw "docs/hello.txt",
// "lodash.debounce"): the extension heuristic that 404s missing static files
// must not apply under /repos/.
func TestSPA_RepoPagesWithDottedNames(t *testing.T) {
	h, fsys := testSPA()
	index := string(fsys["index.html"].Data)

	for _, p := range []string{
		"/repos/raw-files/packages/raw/docs%2Fhello.txt",
		"/repos/raw-files/packages/raw/hello.txt",
		"/repos/npm-local/packages/npm/lodash.debounce",
		"/repos/maven-releases/packages/maven/com.example%3Aapp",
	} {
		rw := do(h, http.MethodGet, p, nil)
		if rw.Code != http.StatusOK || rw.Body.String() != index {
			t.Errorf("GET %s = %d (%d bytes), want 200 with index.html", p, rw.Code, rw.Body.Len())
		}
	}

	// The heuristic still protects everything outside /repos/.
	if rw := do(h, http.MethodGet, "/docs/hello.txt", nil); rw.Code != http.StatusNotFound {
		t.Errorf("GET /docs/hello.txt = %d, want 404", rw.Code)
	}
}

func TestAcceptsGzip(t *testing.T) {
	tests := []struct {
		header string
		want   bool
	}{
		{"", false},
		{"gzip", true},
		{"GZIP", true},
		{"deflate, gzip;q=0.5", true},
		{"br, gzip, deflate", true},
		{"*", true},
		{"gzip;q=0", false},
		{"gzip; q=0.0, *;q=1", false}, // an explicit entry beats the wildcard
		{"identity, *;q=0.5", true},
		{"identity, *;q=0", false},
		{"identity", false},
		{"br", false},
		{"gzip;q=bogus", false},
	}

	for _, tc := range tests {
		if got := acceptsGzip(tc.header); got != tc.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestCompressible(t *testing.T) {
	tests := []struct {
		ctyp string
		want bool
	}{
		{"text/html; charset=utf-8", true},
		{"text/javascript; charset=utf-8", true},
		{"text/css; charset=utf-8", true},
		{"application/json", true},
		{"application/manifest+json", true},
		{"image/svg+xml", true},
		{"application/wasm", true},
		{"image/png", false},
		{"image/jpeg", false},
		{"image/webp", false},
		{"font/woff2", false},
		{"font/woff", false},
		{"application/zip", false},
		{"application/gzip", false},
		{"application/octet-stream", false},
		{"", false},
	}

	for _, tc := range tests {
		if got := compressible(tc.ctyp); got != tc.want {
			t.Errorf("compressible(%q) = %v, want %v", tc.ctyp, got, tc.want)
		}
	}
}

// TestSPA_UnbuiltUI covers a fresh clone: the embedded dist/ holds only the
// .gitkeep placeholder, so every page is 404 and the placeholder is not served.
func TestSPA_UnbuiltUI(t *testing.T) {
	h := newSPAHandler(fstest.MapFS{".gitkeep": {}})

	for _, p := range []string{"/", "/repos/x", "/.gitkeep"} {
		if rw := do(h, http.MethodGet, p, nil); rw.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rw.Code)
		}
	}
}

// TestEmbeddedDistCompiles guards the //go:embed pattern: it must resolve on a
// checkout where the UI has never been built.
func TestEmbeddedDistCompiles(t *testing.T) {
	if _, err := fs.Stat(distFS, "dist/.gitkeep"); err != nil {
		if _, err2 := fs.Stat(distFS, "dist/index.html"); err2 != nil {
			t.Fatalf("embedded dist has neither .gitkeep nor index.html: %v", err)
		}
	}
}
