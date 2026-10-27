// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pkgapk "github.com/openartifact/openartifact/pkg/apk"
	"github.com/openartifact/openartifact/pkg/apk/apktest"
)

const (
	apkDir     = "v3.20/main/x86_64/"
	apkIndexK  = apkDir + "APKINDEX.tar.gz"
	apkPkgFile = "hello-1.0-r0.apk"
	apkPkgKey  = apkDir + apkPkgFile
)

type apkMirror struct {
	index []byte
	pkg   []byte
	files map[string][]byte
}

// newApkMirror builds a signed index listing hello-1.0-r0 and the package.
func newApkMirror(t *testing.T) *apkMirror {
	t.Helper()

	pkgBytes := apktest.Build(t, apktest.Options{Name: "hello"})

	pkg, err := pkgapk.ParsePackage(bytes.NewReader(pkgBytes), int64(len(pkgBytes)))
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	pkg.Size = int64(len(pkgBytes))

	key, err := pkgapk.GenerateKey(2048)
	if err != nil {
		t.Fatalf("key: %v", err)
	}

	signer, err := pkgapk.NewSigner("alpine-test.rsa.pub", key)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}

	index, err := pkgapk.BuildIndex([]pkgapk.Package{*pkg}, "test", signer)
	if err != nil {
		t.Fatalf("index: %v", err)
	}

	return &apkMirror{index: index, pkg: pkgBytes, files: map[string][]byte{apkIndexK: index, apkPkgKey: pkgBytes}}
}

func newApkFixture(t *testing.T, m *apkMirror) *fixture {
	t.Helper()

	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		body, ok := m.files[strings.TrimPrefix(r.URL.Path, "/alpine/")]
		if !ok {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	})
	f.repo.Format = "apk"
	f.repo.UpstreamURL = f.upstream.URL + "/alpine"

	return f
}

func TestApkProxy_IndexIsPassedThroughUnmodifiedAndCachedForTTL(t *testing.T) {
	m := newApkMirror(t)
	f := newApkFixture(t, m)

	w := f.do(http.MethodGet, apkIndexK)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), m.index) {
		t.Fatalf("status %d, index served differs from the upstream's (%d bytes)", w.Code, w.Body.Len())
	}

	e := f.db.entry("repo-1", apkIndexK)
	if e == nil || time.Until(e.ExpiresAt) > time.Duration(f.repo.CacheTTLSeconds)*time.Second {
		t.Fatalf("index entry = %+v, want the repository TTL", e)
	}

	hits := f.hits.Load()

	if w := f.do(http.MethodGet, apkIndexK); w.Code != http.StatusOK || f.hits.Load() != hits {
		t.Errorf("second read: status %d, upstream hits %d -> %d, want a cache hit", w.Code, hits, f.hits.Load())
	}
}

func TestApkProxy_PackageIsVerifiedCachedAndPinned(t *testing.T) {
	m := newApkMirror(t)
	f := newApkFixture(t, m)

	if w := f.do(http.MethodGet, apkPkgKey); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), m.pkg) {
		t.Fatalf("status %d, body differs from the package", w.Code)
	}

	assertPinned(t, f, apkPkgKey)

	hits := f.hits.Load()

	if w := f.do(http.MethodGet, apkPkgKey); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), m.pkg) || f.hits.Load() != hits {
		t.Errorf("cached read: status %d, upstream hits %d -> %d", w.Code, hits, f.hits.Load())
	}
}

func TestApkProxy_HeadAndConditionalRequests(t *testing.T) {
	m := newApkMirror(t)
	f := newApkFixture(t, m)

	w := f.do(http.MethodHead, apkPkgKey)
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Content-Length") == "" {
		t.Fatalf("HEAD: status %d, body %d bytes, headers %v", w.Code, w.Body.Len(), w.Header())
	}

	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)

	rec := httptest.NewRecorder()
	f.svc.Serve(rec, req, f.repo, apkPkgKey)

	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("conditional GET: status %d, %d body bytes, want 304", rec.Code, rec.Body.Len())
	}
}

func TestApkProxy_Refusals(t *testing.T) {
	other := apktest.Build(t, apktest.Options{Name: "hello", Files: map[string]string{"usr/bin/hello": "different"}})

	tests := []struct {
		name   string
		key    string
		mutate func(m *apkMirror)
		want   int
	}{
		{"checksum mismatch", apkPkgKey, func(m *apkMirror) { m.files[apkPkgKey] = other }, http.StatusBadGateway},
		{"not a package", apkPkgKey, func(m *apkMirror) { m.files[apkPkgKey] = []byte("<html>portal</html>") }, http.StatusBadGateway},
		{"unlisted in the index", apkDir + "ghost-2.0-r0.apk", func(m *apkMirror) {
			m.files[apkDir+"ghost-2.0-r0.apk"] = other
		}, http.StatusNotFound},
		{"index missing upstream", apkPkgKey, func(m *apkMirror) { delete(m.files, apkIndexK) }, http.StatusNotFound},
		{"package missing upstream", apkPkgKey, func(m *apkMirror) { delete(m.files, apkPkgKey) }, http.StatusNotFound},
		{"invalid index", apkPkgKey, func(m *apkMirror) { m.files[apkIndexK] = []byte("junk") }, http.StatusBadGateway},
		{
			"other path",
			"v3.20/main/x86_64/other.txt",
			func(m *apkMirror) { m.files["v3.20/main/x86_64/other.txt"] = []byte("x") },
			http.StatusNotFound,
		},
		{"public key", "public.key", func(m *apkMirror) { m.files["public.key"] = []byte("x") }, http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newApkMirror(t)
			tt.mutate(m)

			f := newApkFixture(t, m)

			if w := f.do(http.MethodGet, tt.key); w.Code != tt.want {
				t.Fatalf("status %d, want %d", w.Code, tt.want)
			}

			if e := f.db.entry("repo-1", tt.key); e != nil && e.StatusCode == http.StatusOK {
				t.Errorf("a refused file was cached: %+v", e)
			}
		})
	}
}

func TestApkProxy_Keys(t *testing.T) {
	tests := []struct {
		key             string
		index, pkg, imm bool
	}{
		{apkIndexK, true, false, false},
		{apkPkgKey, false, true, true},
		{"APKINDEX.tar.gz", false, false, false},
		{"v3.20/main/APKINDEX.tar.gz", false, false, false},
		{"a/b/c/d/APKINDEX.tar.gz", false, false, false},
		{"v3.20/main/x86_64/hello.apk", false, false, false},
		{"v3.20/main/x86_64/hello-1.0-r0.apk.sig", false, false, false},
		{"hello-1.0-r0.apk", false, false, false},
	}

	for _, tt := range tests {
		if got := apkIndexKey(tt.key); got != tt.index || isMetadataKey("apk", tt.key) != tt.index {
			t.Errorf("index(%q) = %v, want %v", tt.key, got, tt.index)
		}

		if got := apkPackageKey(tt.key); got != tt.pkg || Immutable("apk", tt.key) != tt.imm {
			t.Errorf("package(%q) = %v, want %v", tt.key, got, tt.pkg)
		}
	}
}
