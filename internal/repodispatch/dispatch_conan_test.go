// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repodispatch

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	conanTestRev    = `{"revision":"cac0f6daea041b0ccf42934163defb20","time":"2025-12-09T12:51:39.337+0000"}`
	conanRemoteBase = "http://oa.test"
)

// conanEnv wires a conan proxy ("mirror", upstream: zlib) and a hosted
// repository ("local": mylib) into a group ("all") behind the real chi route
// shape of /repos/{name}.
func conanEnv(t *testing.T) http.Handler {
	t.Helper()
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/conans/zlib/1.0/_/_/latest":
			_, _ = io.WriteString(w, conanTestRev)
		case "/v2/conans/search":
			_, _ = io.WriteString(w, `{"results":["zlib/1.0@_/_","mylib/0.1@_/_"]}`)
		case "/v2/conans/zlib/1.0/_/_/revisions/cac0f6daea041b0ccf42934163defb20/files":
			_, _ = io.WriteString(w, `{"files":{"conanfile.py":{},"conanmanifest.txt":{}}}`)
		case "/v2/conans/zlib/1.0/_/_/revisions/cac0f6daea041b0ccf42934163defb20/files/conanfile.py":
			_, _ = io.WriteString(w, "proxied conanfile")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	repo := func(id, name, typ string) *models.Repository {
		return &models.Repository{ID: id, Name: name, Format: "conan", RepoType: typ, CacheTTLSeconds: 60, NegativeCacheTTLSeconds: 60}
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

	// The hosted router has the route shapes of the real conan handler, for the
	// requests the group sends a hosted member.
	hosted := chi.NewRouter()
	hosted.Get("/v2/conans/search", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":["mylib/1.0@_/_"]}`)
	})
	hosted.Get("/v2/conans/{name}/{version}/{user}/{channel}/latest", func(w http.ResponseWriter, r *http.Request) {
		if chi.URLParam(r, "name") != "mylib" {
			http.NotFound(w, r)
			return
		}

		_, _ = io.WriteString(w, `{"revision":"1111","time":"2025-01-01T00:00:00.000+0000"}`)
	})
	hosted.Get("/v1/files/*", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "hosted:"+r.URL.Path) })
	hosted.Get("/v1/conans/{name}/{version}/{user}/{channel}/download_urls", func(w http.ResponseWriter, r *http.Request) {
		if chi.URLParam(r, "name") != "mylib" {
			http.NotFound(w, r)
			return
		}

		_, _ = io.WriteString(w, `{"conanfile.py":"`+conanRemoteBase+`/repos/local/v1/files/mylib/1.0/_/_/0/export/conanfile.py"}`)
	})

	d := New(
		log,
		db,
		proxy.NewService(log, db, store),
		denyAuthorizer{},
		map[string]http.Handler{"conan": hosted},
	).WithBaseURL(conanRemoteBase)

	r := chi.NewRouter()
	r.Route("/repos/{name}", func(r chi.Router) {
		r.Use(d.Middleware)
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "format-handler", http.StatusTeapot) })
	})

	return r
}

func TestConanDispatch_ProxyServesTheUpstreamsRecipe(t *testing.T) {
	h := conanEnv(t)

	for range 2 {
		w := npmGet(h, "/repos/mirror/v2/conans/zlib/1.0/_/_/latest")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "cac0f6daea041b0ccf42934163defb20") {
			t.Errorf("latest = %d %s", w.Code, w.Body.String())
		}
	}

	w := npmGet(h, "/repos/mirror/v2/conans/zlib/1.0/_/_/revisions/cac0f6daea041b0ccf42934163defb20/files/conanfile.py")
	if w.Code != http.StatusOK || w.Body.String() != "proxied conanfile" {
		t.Errorf("file = %d %q", w.Code, w.Body.String())
	}
}

func TestConanDispatch_ProxyAnswersConan1URLsAtItself(t *testing.T) {
	h := conanEnv(t)

	w := npmGet(h, "/repos/mirror/v1/conans/zlib/1.0/_/_/download_urls")
	want := conanRemoteBase + "/repos/mirror/v1/files/zlib/1.0/_/_/cac0f6daea041b0ccf42934163defb20/export/conanfile.py"

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
		t.Errorf("download_urls = %d %s, want a URL %s", w.Code, w.Body.String(), want)
	}

	if w := npmGet(
		h,
		"/repos/mirror/v1/files/zlib/1.0/_/_/cac0f6daea041b0ccf42934163defb20/export/conanfile.py",
	); w.Body.String() != "proxied conanfile" {
		t.Errorf("v1 file = %d %q", w.Code, w.Body.String())
	}
}

func TestConanDispatch_GroupResolvesHostedAndProxiedRecipes(t *testing.T) {
	h := conanEnv(t)

	if w := npmGet(
		h,
		"/repos/all/v2/conans/mylib/1.0/_/_/latest",
	); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "1111") {
		t.Errorf("hosted latest = %d %s", w.Code, w.Body.String())
	}

	if w := npmGet(
		h,
		"/repos/all/v2/conans/zlib/1.0/_/_/latest",
	); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "cac0f6dae") {
		t.Errorf("proxied latest = %d %s", w.Code, w.Body.String())
	}
}

func TestConanDispatch_GroupSearchIsMergedAndHostedWins(t *testing.T) {
	h := conanEnv(t)

	w := npmGet(h, "/repos/all/v2/conans/search?q=*")

	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "mylib/1.0@_/_") || !strings.Contains(body, "zlib/1.0@_/_") ||
		strings.Contains(body, "mylib/0.1") {
		t.Errorf("search = %d %s", w.Code, body)
	}
}

func TestConanDispatch_GroupPointsConan1URLsAtTheGroup(t *testing.T) {
	h := conanEnv(t)

	w := npmGet(h, "/repos/all/v1/conans/mylib/1.0/_/_/download_urls")
	if want := conanRemoteBase + "/repos/all/v1/files/mylib/1.0/_/_/0/export/conanfile.py"; !strings.Contains(w.Body.String(), want) {
		t.Errorf("download_urls = %d %s, want %s", w.Code, w.Body.String(), want)
	}
}

func TestConanDispatch_ProxyAndGroupAreReadOnly(t *testing.T) {
	h := conanEnv(t)

	for _, repo := range []string{"mirror", "all"} {
		for _, m := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
			req := httptest.NewRequestWithContext(
				t.Context(),
				m,
				"/repos/"+repo+"/v2/conans/zlib/1.0/_/_/revisions/x/files/conanfile.py",
				nil,
			)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", m, repo, w.Code)
			}
		}
	}
}
