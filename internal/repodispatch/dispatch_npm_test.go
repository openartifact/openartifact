// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repodispatch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const npmBase = "http://oa.test"

func npmDoc(name, version, tarball string) string {
	b, _ := json.Marshal(map[string]any{
		"name":      name,
		"dist-tags": map[string]string{"latest": version},
		"versions": map[string]any{
			version: map[string]any{"name": name, "version": version, "dist": map[string]string{"tarball": tarball, "shasum": "x"}},
		},
	})

	return string(b)
}

// npmEnv wires an npm proxy ("mirror") and a hosted npm repository ("local")
// into a group ("all") behind the real chi route shape of /repos/{name}.
func npmEnv(t *testing.T) http.Handler {
	t.Helper()
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	var upstream *httptest.Server //nolint:wsl // referenced by its own handler

	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/@s/remote" {
			_, _ = io.WriteString(w, npmDoc("@s/remote", "1.0.0", upstream.URL+"/@s/remote/-/remote-1.0.0.tgz"))
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)

	repo := func(id, name, typ string) *models.Repository {
		return &models.Repository{ID: id, Name: name, Format: "npm", RepoType: typ, CacheTTLSeconds: 60, NegativeCacheTTLSeconds: 60}
	}

	mirror := repo("1", "mirror", "proxy")
	mirror.UpstreamURL = upstream.URL

	db := &fakeDB{
		repos:   map[string]*models.Repository{"local": repo("2", "local", "hosted"), "mirror": mirror, "all": repo("3", "all", "group")},
		links:   map[string][]*models.RepositoryGroupMember{"3": {{MemberRepoID: "2", Priority: 0}, {MemberRepoID: "1", Priority: 1}}},
		entries: map[string]*models.ProxyCacheEntry{},
	}

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}

	// The hosted router has the route shapes of the real npm handler. It
	// reports the package name it was asked for after unescaping.
	hosted := chi.NewRouter()
	hosted.Get("/{scope}/{pkg}/-/{tarball}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hosted-tarball:"+chi.URLParam(r, "scope")+"/"+chi.URLParam(r, "pkg")+"/"+chi.URLParam(r, "tarball"))
	})
	hosted.Get("/{pkg}", func(w http.ResponseWriter, r *http.Request) {
		name := strings.ReplaceAll(strings.ReplaceAll(chi.URLParam(r, "pkg"), "%2f", "/"), "%2F", "/")
		if name != "@s/local" {
			http.NotFound(w, r)
			return
		}

		_, _ = io.WriteString(w, npmDoc(name, "2.0.0", npmBase+"/repos/local/@s/local/-/local-2.0.0.tgz"))
	})

	d := New(log, db, proxy.NewService(log, db, store), denyAuthorizer{}, map[string]http.Handler{"npm": hosted}).WithBaseURL(npmBase)

	r := chi.NewRouter()
	r.Route("/repos/{name}", func(r chi.Router) {
		r.Use(d.Middleware)
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "format-handler", http.StatusTeapot) })
	})

	return r
}

func npmGet(h http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{IsAnonymous: true}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	return w
}

func TestNPMProxyAndGroupThroughDispatch(t *testing.T) {
	h := npmEnv(t)

	tarballOf := func(w *httptest.ResponseRecorder, version string) string {
		var d struct {
			Versions map[string]struct {
				Dist struct{ Tarball string } `json:"dist"`
			} `json:"versions"`
		}

		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatalf("not JSON: %v: %s", err, w.Body.String())
		}

		return d.Versions[version].Dist.Tarball
	}

	t.Run("proxy rewrites tarball URLs to itself", func(t *testing.T) {
		w := npmGet(h, "/repos/mirror/@s%2fremote")
		if w.Code != 200 || tarballOf(w, "1.0.0") != npmBase+"/repos/mirror/@s/remote/-/remote-1.0.0.tgz" {
			t.Errorf("= %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("group merges a hosted and a proxy member, escaped or not", func(t *testing.T) {
		for _, p := range []string{"/repos/all/@s%2flocal", "/repos/all/@s/local"} {
			w := npmGet(h, p)
			if w.Code != 200 || tarballOf(w, "2.0.0") != npmBase+"/repos/all/@s/local/-/local-2.0.0.tgz" {
				t.Errorf("GET %s = %d %s", p, w.Code, w.Body.String())
			}
		}

		w := npmGet(h, "/repos/all/@s%2fremote")
		if w.Code != 200 || tarballOf(w, "1.0.0") != npmBase+"/repos/all/@s/remote/-/remote-1.0.0.tgz" {
			t.Errorf("proxy member via group = %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("group tarball is answered by the first member that has it", func(t *testing.T) {
		w := npmGet(h, "/repos/all/@s/local/-/local-2.0.0.tgz")
		if w.Code != 200 || w.Body.String() != "hosted-tarball:@s/local/local-2.0.0.tgz" {
			t.Errorf("= %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("unknown package", func(t *testing.T) {
		if w := npmGet(h, "/repos/all/nope"); w.Code != http.StatusNotFound {
			t.Errorf("= %d, want 404", w.Code)
		}
	})
}
