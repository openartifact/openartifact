// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repodispatch

import (
	"crypto/sha256"
	"encoding/hex"
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
	pkghelm "github.com/openartifact/openartifact/pkg/helm"
	"github.com/openartifact/openartifact/pkg/logger"
)

const helmBase = "http://oa.test"

// helmEnv wires a Helm proxy ("mirror") and a hosted Helm repository ("local")
// into a group ("all") behind the real chi route shape of /repos/{name}.
func helmEnv(t *testing.T) (http.Handler, string) {
	t.Helper()
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	chart := []byte("remote chart bytes")
	digest := sha256.Sum256(chart)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/helm/index.yaml":
			_, _ = io.WriteString(w, "apiVersion: v1\nentries:\n  remote:\n  - name: remote\n    version: 1.0.0\n    digest: "+
				hex.EncodeToString(digest[:])+"\n    urls:\n    - charts/remote-1.0.0.tgz\n")
		case "/helm/charts/remote-1.0.0.tgz":
			_, _ = w.Write(chart)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	repo := func(id, name, typ string) *models.Repository {
		return &models.Repository{ID: id, Name: name, Format: "helm", RepoType: typ, CacheTTLSeconds: 60, NegativeCacheTTLSeconds: 60}
	}

	mirror := repo("1", "mirror", "proxy")
	mirror.UpstreamURL = upstream.URL + "/helm"

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

	// The hosted router has the route shapes of the real helm handler.
	hosted := chi.NewRouter()
	hosted.Get("/index.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "apiVersion: v1\nentries:\n  local:\n  - name: local\n    version: 2.0.0\n"+
			"    urls:\n    - charts/local-2.0.0.tgz\n")
	})
	hosted.Get("/charts/{filename}", func(w http.ResponseWriter, r *http.Request) {
		if chi.URLParam(r, "filename") != "local-2.0.0.tgz" {
			http.NotFound(w, r)
			return
		}

		_, _ = io.WriteString(w, "hosted-chart")
	})
	hosted.Get("/charts", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "[]") })

	d := New(log, db, proxy.NewService(log, db, store), denyAuthorizer{}, map[string]http.Handler{"helm": hosted}).WithBaseURL(helmBase)

	r := chi.NewRouter()
	r.Route("/repos/{name}", func(r chi.Router) {
		r.Use(d.Middleware)
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "format-handler", http.StatusTeapot) })
	})

	return r, string(chart)
}

func chartURLs(t *testing.T, w *httptest.ResponseRecorder) map[string]string {
	t.Helper()

	if w.Code != http.StatusOK {
		t.Fatalf("index = %d: %s", w.Code, w.Body.String())
	}

	ix, err := pkghelm.ParseIndex(w.Body.Bytes())
	if err != nil {
		t.Fatalf("not an index: %v: %s", err, w.Body.String())
	}

	out := map[string]string{}

	for cv := range ix.Versions() {
		if len(cv.URLs) != 1 {
			t.Fatalf("%s has urls %v", cv.Name, cv.URLs)
		}

		out[cv.Name] = cv.URLs[0]
	}

	return out
}

func TestHelmProxyAndGroupThroughDispatch(t *testing.T) {
	h, chart := helmEnv(t)

	t.Run("proxy rewrites chart URLs relative to itself and serves the chart", func(t *testing.T) {
		urls := chartURLs(t, npmGet(h, "/repos/mirror/index.yaml"))
		if urls["remote"] != "charts/remote-1.0.0.tgz" {
			t.Errorf("urls = %v", urls)
		}

		if w := npmGet(h, "/repos/mirror/charts/remote-1.0.0.tgz"); w.Code != 200 || w.Body.String() != chart {
			t.Errorf("chart = %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("group merges the hosted and the proxy member", func(t *testing.T) {
		urls := chartURLs(t, npmGet(h, "/repos/all/index.yaml"))
		if urls["local"] != "charts/local-2.0.0.tgz" ||
			urls["remote"] != "charts/remote-1.0.0.tgz" {
			t.Errorf("urls = %v", urls)
		}
	})

	t.Run("group charts are answered by the first member that has them", func(t *testing.T) {
		if w := npmGet(h, "/repos/all/charts/local-2.0.0.tgz"); w.Code != 200 || w.Body.String() != "hosted-chart" {
			t.Errorf("hosted chart = %d %q", w.Code, w.Body.String())
		}

		if w := npmGet(h, "/repos/all/charts/remote-1.0.0.tgz"); w.Code != 200 || w.Body.String() != chart {
			t.Errorf("proxied chart = %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("group does not expose the hosted chart listing", func(t *testing.T) {
		if w := npmGet(h, "/repos/all/charts"); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "[]") {
			t.Errorf("= %d %q, want 404", w.Code, w.Body.String())
		}
	})

	t.Run("uploads to proxy and group are refused", func(t *testing.T) {
		for _, name := range []string{"mirror", "all"} {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/repos/"+name+"/charts", strings.NewReader("x"))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("POST %s = %d, want 405", name, w.Code)
			}
		}
	})
}
