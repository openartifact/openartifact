// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repodispatch

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/storage"
	pkgapk "github.com/openartifact/openartifact/pkg/apk"
	"github.com/openartifact/openartifact/pkg/apk/apktest"
	"github.com/openartifact/openartifact/pkg/logger"
)

// An apk proxy is served by the proxy service, not by the hosted apk router
// that answers hosted apk repositories; writes to it are refused.
func TestApkProxyIsServedByTheProxyNotTheHostedRouter(t *testing.T) {
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	pkgBytes := apktest.Build(t, apktest.Options{Name: "hello"})

	pkg, err := pkgapk.ParsePackage(bytes.NewReader(pkgBytes), int64(len(pkgBytes)))
	if err != nil {
		t.Fatal(err)
	}

	pkg.Size = int64(len(pkgBytes))

	index, err := pkgapk.BuildIndex([]pkgapk.Package{*pkg}, "test", nil)
	if err != nil {
		t.Fatal(err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/alpine/v3.20/main/x86_64/APKINDEX.tar.gz":
			_, _ = w.Write(index)
		case "/alpine/v3.20/main/x86_64/hello-1.0-r0.apk":
			_, _ = w.Write(pkgBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	mirror := &models.Repository{
		ID: "1", Name: "mirror", Format: "apk", RepoType: "proxy",
		UpstreamURL: upstream.URL + "/alpine", CacheTTLSeconds: 60, NegativeCacheTTLSeconds: 60,
	}
	db := &fakeDB{repos: map[string]*models.Repository{"mirror": mirror}, entries: map[string]*models.ProxyCacheEntry{}}

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}

	hosted := chi.NewRouter()
	hosted.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "hosted-router", http.StatusTeapot) })

	d := New(log, db, proxy.NewService(log, db, store), denyAuthorizer{}, map[string]http.Handler{"apk": hosted})

	r := chi.NewRouter()
	r.Route("/repos/{name}", func(r chi.Router) {
		r.Use(d.Middleware)
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "format-handler", http.StatusTeapot) })
	})

	if w := npmGet(r, "/repos/mirror/v3.20/main/x86_64/APKINDEX.tar.gz"); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), index) {
		t.Errorf("index = %d, %d bytes", w.Code, w.Body.Len())
	}

	if w := npmGet(
		r,
		"/repos/mirror/v3.20/main/x86_64/hello-1.0-r0.apk",
	); w.Code != http.StatusOK ||
		!bytes.Equal(w.Body.Bytes(), pkgBytes) {
		t.Errorf("package = %d, %d bytes", w.Code, w.Body.Len())
	}

	if w := npmGet(r, "/repos/mirror/v3.20/main/x86_64/ghost-1.0-r0.apk"); w.Code != http.StatusNotFound {
		t.Errorf("unlisted package = %d, want 404", w.Code)
	}

	if w := npmGet(r, "/repos/mirror/public.key"); w.Code != http.StatusNotFound {
		t.Errorf("public.key = %d, want 404", w.Code)
	}

	put := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/repos/mirror/v3.20/main/x86_64/x-1.0-r0.apk", nil)
	pw := httptest.NewRecorder()

	r.ServeHTTP(pw, put)

	if pw.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT to a proxy = %d, want 405", pw.Code)
	}
}
