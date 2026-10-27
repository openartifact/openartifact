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
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	gemRemoteFile = "remote-gem"
	gemLocalFile  = "local-gem"
)

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// rubygemsEnv wires a rubygems proxy ("mirror"), a hosted repository ("local")
// and a group ("all") over both behind the route shape of /repos/{name}.
func rubygemsEnv(t *testing.T) http.Handler {
	t.Helper()
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/info/remote":
			_, _ = io.WriteString(w, "---\n1.0.0 |checksum:"+sha256Hex(gemRemoteFile)+"\n")
		case "/gems/remote-1.0.0.gem":
			_, _ = io.WriteString(w, gemRemoteFile)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	repo := func(id, name, typ string) *models.Repository {
		return &models.Repository{ID: id, Name: name, Format: "rubygems", RepoType: typ, CacheTTLSeconds: 60, NegativeCacheTTLSeconds: 60}
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

	hosted := chi.NewRouter()
	hosted.Get("/info/{gem}", func(w http.ResponseWriter, r *http.Request) {
		if chi.URLParam(r, "gem") != "local" {
			http.NotFound(w, r)
			return
		}

		_, _ = io.WriteString(w, "---\n2.0.0 |checksum:"+sha256Hex(gemLocalFile)+"\n")
	})
	hosted.Get("/gems/{file}", func(w http.ResponseWriter, r *http.Request) {
		if chi.URLParam(r, "file") != "local-2.0.0.gem" {
			http.NotFound(w, r)
			return
		}

		_, _ = io.WriteString(w, gemLocalFile)
	})

	d := New(log, db, proxy.NewService(log, db, store), denyAuthorizer{}, map[string]http.Handler{"rubygems": hosted})

	r := chi.NewRouter()
	r.Route("/repos/{name}", func(r chi.Router) {
		r.Use(d.Middleware)
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "format-handler", http.StatusTeapot) })
	})

	return r
}

func TestRubyGemsProxyAndGroupThroughDispatch(t *testing.T) {
	h := rubygemsEnv(t)

	tests := []struct {
		name, path string
		code       int
		contains   string
	}{
		{"proxy info", "/repos/mirror/info/remote", 200, "1.0.0 |checksum:"},
		{"proxy gem", "/repos/mirror/gems/remote-1.0.0.gem", 200, gemRemoteFile},
		{"group info from the proxy", "/repos/all/info/remote", 200, "1.0.0 |checksum:"},
		{"group info from the hosted member", "/repos/all/info/local", 200, "2.0.0 |checksum:"},
		{"group gem from the proxy", "/repos/all/gems/remote-1.0.0.gem", 200, gemRemoteFile},
		{"group gem from the hosted member", "/repos/all/gems/local-2.0.0.gem", 200, gemLocalFile},
		{"group unknown gem", "/repos/all/info/nothing", 404, ""},
		{"group legacy specs", "/repos/all/specs.4.8.gz", 404, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := npmGet(h, tt.path)
			if w.Code != tt.code || !strings.Contains(w.Body.String(), tt.contains) {
				t.Errorf("= %d %q", w.Code, w.Body.String())
			}
		})
	}

	for _, name := range []string{"mirror", "all"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/repos/"+name+"/api/v1/gems", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("push to %s = %d, want 405", name, w.Code)
		}
	}
}
