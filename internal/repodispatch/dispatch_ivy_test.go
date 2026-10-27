// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repodispatch

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// moduleDB is the fake database with the module-ownership queries of hosted ivy
// and sbt members: the hosted member "local" holds org.example/lib.
type moduleDB struct{ *fakeDB }

func (d moduleDB) held(repoID, group, module string) (bool, error) {
	return repoID == "2" && group == "org.example" && module == "lib", nil
}

func (d moduleDB) IvyModuleExists(_ context.Context, repoID, org, module string) (bool, error) {
	return d.held(repoID, org, module)
}

func (d moduleDB) SBTModuleExists(_ context.Context, repoID, group, module string) (bool, error) {
	return d.held(repoID, group, module)
}

// ivyEnv wires a proxy ("mirror"), a hosted repository ("local") and a group
// ("all") over both of format behind the route shape of /repos/<format>/{name}.
func ivyEnv(t *testing.T, format string) http.Handler {
	t.Helper()
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	sha1Of := func(s string) string {
		h := sha1.Sum([]byte(s))

		return hex.EncodeToString(h[:])
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ivy/org.example/lib/9.9.9/jars/lib.jar":
			_, _ = io.WriteString(w, "evil")
		case "/ivy/org.example/other/1.0/jars/other.jar":
			_, _ = io.WriteString(w, "other-jar")
		case "/ivy/org.example/other/1.0/jars/other.jar.sha1":
			_, _ = io.WriteString(w, sha1Of("other-jar"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	repo := func(id, name, typ string) *models.Repository {
		return &models.Repository{ID: id, Name: name, Format: format, RepoType: typ, CacheTTLSeconds: 60, NegativeCacheTTLSeconds: 60}
	}

	mirror := repo("1", "mirror", "proxy")
	mirror.UpstreamURL = upstream.URL + "/ivy"

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

	// The hosted router has the route shape of the real filerepo handler.
	hosted := chi.NewRouter()
	hosted.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		if chi.URLParam(r, "name") == "local" && chi.URLParam(r, "*") == "org.example/lib/1.0/jars/lib.jar" {
			_, _ = io.WriteString(w, "hosted-jar")
			return
		}

		http.NotFound(w, r)
	})

	d := New(log, moduleDB{db}, proxy.NewService(log, db, store), denyAuthorizer{}, map[string]http.Handler{format: hosted})

	r := chi.NewRouter()
	r.Route("/repos/"+format+"/{name}", func(r chi.Router) {
		r.Use(d.Middleware)
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "format-handler", http.StatusTeapot) })
	})

	return r
}

func TestIvySbtProxyAndGroupThroughDispatch(t *testing.T) {
	for _, format := range []string{"ivy", "sbt"} {
		t.Run(format, func(t *testing.T) {
			h := ivyEnv(t, format)
			base := "/repos/" + format + "/"

			if w := npmGet(h, base+"mirror/org.example/other/1.0/jars/other.jar"); w.Code != 200 || w.Body.String() != "other-jar" {
				t.Errorf("proxy = %d %q", w.Code, w.Body.String())
			}

			if w := npmGet(h, base+"mirror/org.example/other/1.0/jars/missing.jar"); w.Code != http.StatusNotFound {
				t.Errorf("proxy miss = %d, want 404", w.Code)
			}

			put := httptest.NewRequestWithContext(
				context.Background(), http.MethodPut, base+"mirror/org.example/other/1.0/jars/other.jar", nil,
			)
			pw := httptest.NewRecorder()

			h.ServeHTTP(pw, put)

			if pw.Code != http.StatusMethodNotAllowed {
				t.Errorf("PUT to a proxy = %d, want 405", pw.Code)
			}

			if w := npmGet(h, base+"all/org.example/lib/1.0/jars/lib.jar"); w.Code != 200 || w.Body.String() != "hosted-jar" {
				t.Errorf("group, hosted file = %d %q", w.Code, w.Body.String())
			}

			if w := npmGet(h, base+"all/org.example/other/1.0/jars/other.jar"); w.Code != 200 || w.Body.String() != "other-jar" {
				t.Errorf("group, proxy-only module = %d %q", w.Code, w.Body.String())
			}

			if w := npmGet(h, base+"all/org.example/lib/9.9.9/jars/lib.jar"); w.Code != http.StatusNotFound {
				t.Errorf("group, proxy's version of a hosted module = %d %q, want 404", w.Code, w.Body.String())
			}
		})
	}
}
