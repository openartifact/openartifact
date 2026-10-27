// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repodispatch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// A hosted apk repository has free-form paths below /repos/{name}, so the
// dispatcher hands every method and path to the apk router itself instead of
// falling through to the shared /repos/{name} routes.
func TestHostedApkIsServedByItsOwnRouter(t *testing.T) {
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}

	db := &fakeDB{repos: map[string]*models.Repository{
		"alp": {ID: "1", Name: "alp", Format: "apk", RepoType: "hosted"},
		"raw": {ID: "2", Name: "raw", Format: "raw", RepoType: "hosted"},
	}}

	apkRouter := chi.NewRouter()
	apkRouter.HandleFunc("/*", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Method+" "+chi.URLParam(r, "name")+" "+chi.URLParam(r, "*"))
	})

	d := New(log, db, proxy.NewService(log, db, store), denyAuthorizer{}, map[string]http.Handler{"apk": apkRouter})
	nextHit := false

	r := chi.NewRouter()
	r.Route("/repos/{name}", func(r chi.Router) {
		r.Use(d.Middleware)
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) {
			nextHit = true
			_, _ = io.WriteString(w, "format-handler")
		})
	})

	do := func(method, path string) string {
		req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
		req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{IsAnonymous: true}))

		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		return w.Body.String()
	}

	for _, tc := range []struct{ method, path, want string }{
		{http.MethodGet, "/repos/alp/v3.20/main/x86_64/APKINDEX.tar.gz", "GET alp v3.20/main/x86_64/APKINDEX.tar.gz"},
		{http.MethodPut, "/repos/alp/edge/testing/aarch64/x-1.0-r0.apk", "PUT alp edge/testing/aarch64/x-1.0-r0.apk"},
		{http.MethodGet, "/repos/alp/public.key", "GET alp public.key"},
	} {
		if got := do(tc.method, tc.path); got != tc.want {
			t.Errorf("%s %s = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}

	if nextHit {
		t.Error("an apk repository must not reach the shared /repos/{name} routes")
	}

	if got := do(http.MethodGet, "/repos/raw/some/file"); got != "format-handler" || !nextHit {
		t.Errorf("other hosted formats must still fall through: %q", got)
	}
}
