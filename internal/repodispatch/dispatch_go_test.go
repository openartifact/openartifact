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
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	formatgomod "github.com/openartifact/openartifact/internal/format/gomod"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// goDB adds the hosted Go module rows the real GOPROXY handler reads.
type goDB struct {
	*fakeDB

	mods map[string]*models.GoModule // repository ID|module|version
}

func (d *goDB) ListGoModuleVersions(_ context.Context, repoID, module string) ([]*models.GoModule, error) {
	var out []*models.GoModule

	for _, m := range d.mods {
		if m.RepositoryID == repoID && m.ModulePath == module {
			out = append(out, m)
		}
	}

	return out, nil
}

func (d *goDB) GetGoModule(_ context.Context, repoID, module, version string) (*models.GoModule, error) {
	return d.mods[repoID+"|"+module+"|"+version], nil
}

// goEnv wires a Go proxy ("mirror"), a hosted Go repository ("local") and a
// group over both ("all") behind the real /repos/{name} route shape, with the
// real hosted GOPROXY handler as the hosted member.
func goEnv(t *testing.T) http.Handler {
	t.Helper()
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")
	t.Setenv("GOSUMDB", "off") // no checksum database to reach from a unit test

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/example.com/remote/@v/list":
			_, _ = io.WriteString(w, "v1.0.0\nv1.1.0\n")
		case "/example.com/remote/@v/v1.1.0.mod":
			_, _ = io.WriteString(w, "module example.com/remote\n")
		case "/example.com/!shared/@v/list":
			_, _ = io.WriteString(w, "v2.0.0\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	repo := func(id, name, typ string) *models.Repository {
		return &models.Repository{ID: id, Name: name, Format: "go", RepoType: typ, CacheTTLSeconds: 60, NegativeCacheTTLSeconds: 60}
	}

	mirror := repo("1", "mirror", "proxy")
	mirror.UpstreamURL = upstream.URL

	base := &fakeDB{
		repos:   map[string]*models.Repository{"local": repo("2", "local", "hosted"), "mirror": mirror, "all": repo("3", "all", "group")},
		links:   map[string][]*models.RepositoryGroupMember{"3": {{MemberRepoID: "2", Priority: 0}, {MemberRepoID: "1", Priority: 1}}},
		entries: map[string]*models.ProxyCacheEntry{},
	}

	db := &goDB{fakeDB: base, mods: map[string]*models.GoModule{
		"2|example.com/local|v0.1.0":  {RepositoryID: "2", ModulePath: "example.com/local", Version: "v0.1.0", GoModContent: "module example.com/local\n"},
		"2|example.com/Shared|v1.0.0": {RepositoryID: "2", ModulePath: "example.com/Shared", Version: "v1.0.0", GoModContent: "module example.com/Shared\n"},
	}}

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}

	hosted := chi.NewRouter()
	formatgomod.NewHTTPHandler(log, db, store, "http://oa.test").RegisterRepoRoutes(hosted)

	d := New(log, db, proxy.NewService(log, db, store), denyAuthorizer{}, map[string]http.Handler{"go": hosted})

	r := chi.NewRouter()
	r.Route("/repos/{name}", func(r chi.Router) {
		r.Use(d.Middleware)
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "format-handler", http.StatusTeapot) })
	})

	return r
}

func goReq(h http.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{IsAnonymous: true}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	return w
}

func TestGoProxyAndGroupThroughDispatch(t *testing.T) {
	h := goEnv(t)

	cases := []struct {
		name, method, path string
		code               int
		body               string
	}{
		{"proxy list", http.MethodGet, "/repos/mirror/example.com/remote/@v/list", 200, "v1.0.0\nv1.1.0\n"},
		{"proxy mod", http.MethodGet, "/repos/mirror/example.com/remote/@v/v1.1.0.mod", 200, "module example.com/remote\n"},
		{"proxy unknown module", http.MethodGet, "/repos/mirror/example.com/nope/@v/list", 404, ""},
		{"proxy refuses uploads", http.MethodPut, "/repos/mirror/example.com/remote/@v/v1.2.0.zip", 405, ""},
		{"group list of a shared module is the hosted member's alone", http.MethodGet, "/repos/all/example.com/!shared/@v/list", 200, "v1.0.0"},
		{"group list of a proxy-only module", http.MethodGet, "/repos/all/example.com/remote/@v/list", 200, "v1.1.0\nv1.0.0"},
		{"group list of a hosted-only module", http.MethodGet, "/repos/all/example.com/local/@v/list", 200, "v0.1.0"},
		{"group mod from the hosted member", http.MethodGet, "/repos/all/example.com/local/@v/v0.1.0.mod", 200, "module example.com/local\n"},
		{"group mod from the proxy member", http.MethodGet, "/repos/all/example.com/remote/@v/v1.1.0.mod", 200, "module example.com/remote\n"},
		{"group unknown", http.MethodGet, "/repos/all/example.com/nope/@v/list", 404, ""},
		{"group refuses uploads", http.MethodPut, "/repos/all/example.com/local/@v/v0.2.0.zip", 405, ""},
		{"group has no checksum database", http.MethodGet, "/repos/all/sumdb/sum.golang.org/supported", 404, ""},
		{"hosted repository still reaches the format handler", http.MethodPut, "/repos/local/example.com/local/@v/v0.2.0.zip", http.StatusTeapot, "format-handler\n"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := goReq(h, c.method, c.path)
			if w.Code != c.code {
				t.Fatalf("status %d, want %d (%s)", w.Code, c.code, w.Body.String())
			}

			if c.body != "" && w.Body.String() != c.body {
				t.Errorf("body %q, want %q", w.Body.String(), c.body)
			}
		})
	}

	// The merged list is sorted highest first, once per version.
	if w := goReq(h, http.MethodGet, "/repos/all/example.com/remote/@v/list"); strings.Count(w.Body.String(), "v1.1.0") != 1 {
		t.Errorf("list = %q", w.Body.String())
	}
}
