// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	gemFileBytes = "demo 1.0.0 gem"
	gemInfoBody  = "---\n1.0.0 |checksum:" // + sha256 of gemFileBytes
)

func gemSHA(s string) string { return sum(s) }

// fakeGemServer is an upstream gem server with one gem, "demo", in version 1.0.0.
type fakeGemServer struct {
	versions string
	info     string
	files    map[string]string
	gem404   atomic.Bool
}

func newFakeGemServer() *fakeGemServer {
	return &fakeGemServer{
		versions: "created_at: 2024-01-01T00:00:00Z\n---\ndemo 1.0.0 d41d8cd98f00b204e9800998ecf8427e\n",
		info:     gemInfoBody + gemSHA(gemFileBytes) + "\n",
		files:    map[string]string{"gems/demo-1.0.0.gem": gemFileBytes},
	}
}

func (g *fakeGemServer) handler(w http.ResponseWriter, r *http.Request) {
	switch p := strings.TrimPrefix(r.URL.Path, "/"); {
	case p == "versions":
		_, _ = w.Write([]byte(g.versions))
	case p == "info/demo" && g.info != "":
		_, _ = w.Write([]byte(g.info))
	case g.files[p] != "" && !g.gem404.Load():
		_, _ = w.Write([]byte(g.files[p]))
	default:
		http.NotFound(w, r)
	}
}

func gemFixture(t *testing.T, g *fakeGemServer) *fixture {
	t.Helper()

	f := newFixture(t, g.handler)
	f.repo.Format = "rubygems"
	f.repo.Name = "gems"
	f.repo.UpstreamURL = strings.TrimSuffix(f.repo.UpstreamURL, "/maven2/")

	return f
}

func gemDo(f *fixture, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	f.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, "/", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}

	w := httptest.NewRecorder()
	f.svc.Serve(w, req, f.repo, path)

	return w
}

func TestRubyGemsProxy_CompactFileHeadersAndRanges(t *testing.T) {
	g := newFakeGemServer()
	f := gemFixture(t, g)

	md5sum := md5.Sum([]byte(g.versions))
	etag := `"` + hex.EncodeToString(md5sum[:]) + `"`
	sha := sha256.Sum256([]byte(g.versions))
	digest := `sha-256="` + base64.StdEncoding.EncodeToString(sha[:]) + `"`
	size := len(g.versions)

	tests := []struct {
		name       string
		method     string
		hdr        map[string]string
		code       int
		body       string
		contentRng string
	}{
		{"whole", http.MethodGet, nil, 200, g.versions, ""},
		{"head", http.MethodHead, nil, 200, "", ""},
		{"tail range", http.MethodGet, map[string]string{"Range": "bytes=10-"}, 206, g.versions[10:],
			"bytes 10-" + strconv.Itoa(size-1) + "/" + strconv.Itoa(size)},
		{"bounded range", http.MethodGet, map[string]string{"Range": "bytes=0-3"}, 206, g.versions[:4], "bytes 0-3/" + strconv.Itoa(size)},
		{"suffix range", http.MethodGet, map[string]string{"Range": "bytes=-5"}, 206, g.versions[size-5:],
			"bytes " + strconv.Itoa(size-5) + "-" + strconv.Itoa(size-1) + "/" + strconv.Itoa(size)},
		{
			"past the end",
			http.MethodGet,
			map[string]string{"Range": "bytes=" + strconv.Itoa(size) + "-"},
			416,
			"",
			"bytes */" + strconv.Itoa(size),
		},
		{"if-range mismatch serves all", http.MethodGet, map[string]string{"Range": "bytes=10-", "If-Range": `"x"`}, 200, g.versions, ""},
		{"if-range match", http.MethodGet, map[string]string{"Range": "bytes=10-", "If-Range": etag}, 206, g.versions[10:],
			"bytes 10-" + strconv.Itoa(size-1) + "/" + strconv.Itoa(size)},
		{"not modified", http.MethodGet, map[string]string{"If-None-Match": etag}, 304, "", ""},
		{"malformed range serves all", http.MethodGet, map[string]string{"Range": "bytes=a-b"}, 200, g.versions, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := gemDo(f, tt.method, "versions", tt.hdr)
			if w.Code != tt.code || w.Body.String() != tt.body {
				t.Fatalf("= %d %q, want %d %q", w.Code, w.Body.String(), tt.code, tt.body)
			}

			if got := w.Header().Get("Content-Range"); got != tt.contentRng {
				t.Errorf("Content-Range %q, want %q", got, tt.contentRng)
			}

			if tt.code == 304 {
				return
			}

			if w.Header().Get("ETag") != etag && tt.code != 416 {
				t.Errorf("ETag %q, want %q", w.Header().Get("ETag"), etag)
			}

			if w.Header().Get("Repr-Digest") != digest || w.Header().Get("Digest") != digest {
				t.Errorf("digest headers %q / %q, want %q", w.Header().Get("Repr-Digest"), w.Header().Get("Digest"), digest)
			}
		})
	}

	if f.hits.Load() != 1 {
		t.Errorf("upstream requests = %d, want 1 (the file is cached)", f.hits.Load())
	}
}

func TestRubyGemsProxy_GemIsVerifiedAgainstInfoChecksum(t *testing.T) {
	g := newFakeGemServer()
	f := gemFixture(t, g)

	if w := f.do(http.MethodGet, "gems/demo-1.0.0.gem"); w.Code != http.StatusOK || w.Body.String() != gemFileBytes {
		t.Fatalf("gem = %d %q", w.Code, w.Body.String())
	}

	// Cached: served with the upstream down.
	g.gem404.Store(true)

	if w := f.do(http.MethodGet, "gems/demo-1.0.0.gem"); w.Code != http.StatusOK {
		t.Errorf("cached gem = %d", w.Code)
	}

	// A version the info does not list is not fetched.
	hits := f.hits.Load()

	if w := f.do(http.MethodGet, "gems/demo-2.0.0.gem"); w.Code != http.StatusNotFound {
		t.Errorf("unlisted version = %d, want 404", w.Code)
	}

	if f.hits.Load() != hits {
		t.Errorf("unlisted version reached the upstream file path")
	}
}

func TestRubyGemsProxy_ChecksumMismatchIsRefusedAndNotCached(t *testing.T) {
	g := newFakeGemServer()
	g.files["gems/demo-1.0.0.gem"] = "tampered"
	f := gemFixture(t, g)

	for range 2 {
		if w := f.do(http.MethodGet, "gems/demo-1.0.0.gem"); w.Code != http.StatusBadGateway {
			t.Fatalf("mismatching gem = %d %q, want 502", w.Code, w.Body.String())
		}
	}

	g.files["gems/demo-1.0.0.gem"] = gemFileBytes

	if w := f.do(http.MethodGet, "gems/demo-1.0.0.gem"); w.Code != http.StatusOK || w.Body.String() != gemFileBytes {
		t.Errorf("after the upstream is fixed = %d %q", w.Code, w.Body.String())
	}
}

func TestRubyGemsProxy_GemWithoutChecksumOrInfoIsServedUnverified(t *testing.T) {
	tests := []struct {
		name string
		info string
	}{
		{"info line without checksum", "---\n1.0.0 |ruby:>= 0\n"},
		{"no info at all", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newFakeGemServer()
			g.info = tt.info
			f := gemFixture(t, g)

			if w := f.do(http.MethodGet, "gems/demo-1.0.0.gem"); w.Code != http.StatusOK || w.Body.String() != gemFileBytes {
				t.Errorf("= %d %q", w.Code, w.Body.String())
			}
		})
	}
}

func TestRubyGemsProxy_AmbiguousFileNameTriesEveryReading(t *testing.T) {
	g := newFakeGemServer()
	g.files = map[string]string{"gems/a-1-2.gem": "ab"}

	f := gemFixture(t, g)

	// Upstream knows gem "a-1" in version 2 only.
	f.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/") {
		case "info/a-1":
			_, _ = w.Write([]byte("---\n2 |checksum:" + gemSHA("ab") + "\n"))
		case "gems/a-1-2.gem":
			_, _ = w.Write([]byte("ab"))
		default:
			http.NotFound(w, r)
		}
	})

	if w := f.do(http.MethodGet, "gems/a-1-2.gem"); w.Code != http.StatusOK || w.Body.String() != "ab" {
		t.Errorf("= %d %q", w.Code, w.Body.String())
	}
}

func TestRubyGemsProxy_RefusesMalformedPathsAndBodies(t *testing.T) {
	g := newFakeGemServer()
	f := gemFixture(t, g)

	for _, p := range []string{"api/v1/gems", "info/", "info/a/b", "info/../x", "gems/x.txt", "gems/-1.gem", "Marshal.4.8", "versions/x", "//versions"} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, w.Code)
		}
	}

	if f.hits.Load() != 0 {
		t.Errorf("upstream requests = %d, want 0", f.hits.Load())
	}

	g.versions = "<html>login</html>"

	if w := f.do(http.MethodGet, "versions"); w.Code != http.StatusBadGateway {
		t.Errorf("html versions = %d, want 502", w.Code)
	}
}

func TestRubyGemsProxy_GemspecAndLegacyIndexNeedMarshalStream(t *testing.T) {
	var z bytes.Buffer

	zw := zlib.NewWriter(&z)
	_, _ = zw.Write([]byte{4, 8, '[', 0})
	_ = zw.Close()

	var bad bytes.Buffer

	zw = zlib.NewWriter(&bad)
	_, _ = zw.Write([]byte("not marshal"))
	_ = zw.Close()

	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "bad-1.0.gemspec.rz") {
			_, _ = w.Write(bad.Bytes())
			return
		}

		_, _ = w.Write(z.Bytes())
	})
	f.repo.Format = "rubygems"
	f.repo.UpstreamURL = strings.TrimSuffix(f.repo.UpstreamURL, "/maven2/")

	if w := f.do(
		http.MethodGet,
		"quick/Marshal.4.8/demo-1.0.gemspec.rz",
	); w.Code != http.StatusOK ||
		!bytes.Equal(w.Body.Bytes(), z.Bytes()) {
		t.Errorf("gemspec = %d", w.Code)
	}

	if w := f.do(http.MethodGet, "quick/Marshal.4.8/bad-1.0.gemspec.rz"); w.Code != http.StatusBadGateway {
		t.Errorf("non-marshal gemspec = %d, want 502", w.Code)
	}
}

func TestRubyGemsProxy_BlobPolicy(t *testing.T) {
	for key, immutable := range map[string]bool{"gems/demo-1.0.0.gem": true, "versions": false, "info/demo": false, "quick/Marshal.4.8/x-1.gemspec.rz": false} {
		if got := Immutable("rubygems", key); got != immutable {
			t.Errorf("Immutable(%q) = %v, want %v", key, got, immutable)
		}
	}
}
