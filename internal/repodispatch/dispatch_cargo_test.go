// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repodispatch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

const cargoBase = "http://oa.test"

func cargoLine(name, vers, crate string) string {
	h := sha256.Sum256([]byte(crate))
	b, _ := json.Marshal(map[string]any{"name": name, "vers": vers, "cksum": hex.EncodeToString(h[:]), "deps": []any{}})

	return string(b) + "\n"
}

// cargoEnv wires a cargo proxy ("mirror"), a hosted cargo repository ("local")
// and a group ("all") over both behind the route shape of /repos/{name}.
func cargoEnv(t *testing.T) http.Handler {
	t.Helper()
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	var upstream *httptest.Server //nolint:wsl // referenced by its own handler

	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/config.json":
			_, _ = io.WriteString(w, `{"dl":"`+upstream.URL+`/crates","api":"https://x"}`)
		case "/re/mo/remote":
			_, _ = io.WriteString(w, cargoLine("remote", "1.0.0", "remote-crate"))
		case "/crates/remote/1.0.0/download":
			_, _ = io.WriteString(w, "remote-crate")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	repo := func(id, name, typ string) *models.Repository {
		return &models.Repository{ID: id, Name: name, Format: "cargo", RepoType: typ, CacheTTLSeconds: 60, NegativeCacheTTLSeconds: 60}
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

	// The hosted router has the route shapes of the real cargo handler.
	hosted := chi.NewRouter()
	hosted.Get("/index/{p1}/{p2}/{crate}", func(w http.ResponseWriter, r *http.Request) {
		if chi.URLParam(r, "crate") != "local" {
			http.NotFound(w, r)
			return
		}

		_, _ = io.WriteString(w, cargoLine("local", "2.0.0", "local-crate"))
	})
	hosted.Get("/api/v1/crates/{crate}/{version}/download", func(w http.ResponseWriter, r *http.Request) {
		if chi.URLParam(r, "crate") != "local" {
			http.NotFound(w, r)
			return
		}

		_, _ = io.WriteString(w, "local-crate")
	})

	d := New(log, db, proxy.NewService(log, db, store), denyAuthorizer{}, map[string]http.Handler{"cargo": hosted}).WithBaseURL(cargoBase)

	r := chi.NewRouter()
	r.Route("/repos/{name}", func(r chi.Router) {
		r.Use(d.Middleware)
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "format-handler", http.StatusTeapot) })
	})

	return r
}

func TestCargoProxyAndGroupThroughDispatch(t *testing.T) {
	h := cargoEnv(t)

	t.Run("proxy serves index and crate", func(t *testing.T) {
		if w := npmGet(h, "/repos/mirror/index/re/mo/remote"); w.Code != 200 || !strings.Contains(w.Body.String(), `"vers":"1.0.0"`) {
			t.Errorf("index = %d %s", w.Code, w.Body.String())
		}

		if w := npmGet(h, "/repos/mirror/api/v1/crates/remote/1.0.0/download"); w.Code != 200 || w.Body.String() != "remote-crate" {
			t.Errorf("crate = %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("proxy and group config point at themselves", func(t *testing.T) {
		for _, name := range []string{"mirror", "all"} {
			w := npmGet(h, "/repos/"+name+"/index/config.json")

			var cfg map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil || cfg["dl"] != cargoBase+"/repos/"+name+"/api/v1/crates" {
				t.Errorf("%s config = %d %s", name, w.Code, w.Body.String())
			}
		}
	})

	t.Run("group merges a hosted and a proxy member", func(t *testing.T) {
		for crate, version := range map[string]string{"lo/ca/local": "2.0.0", "re/mo/remote": "1.0.0"} {
			w := npmGet(h, "/repos/all/index/"+crate)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"vers":"`+version+`"`) {
				t.Errorf("group index %s = %d %s", crate, w.Code, w.Body.String())
			}
		}
	})

	t.Run("group downloads come from the member that has the crate", func(t *testing.T) {
		for _, tc := range []struct{ path, want string }{
			{"local/2.0.0", "local-crate"},
			{"remote/1.0.0", "remote-crate"},
		} {
			w := npmGet(h, "/repos/all/api/v1/crates/"+tc.path+"/download")
			if w.Code != 200 || w.Body.String() != tc.want {
				t.Errorf("%s = %d %q", tc.path, w.Code, w.Body.String())
			}
		}
	})

	t.Run("publish to a proxy or group is 405", func(t *testing.T) {
		for _, name := range []string{"mirror", "all"} {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/repos/"+name+"/api/v1/crates/new", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("PUT %s = %d, want 405", name, w.Code)
			}
		}
	})
}
