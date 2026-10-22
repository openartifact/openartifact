// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repodispatch

import (
	"encoding/json"
	"fmt"
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

const nugetBase = "http://oa.test"

// nugetEnv wires a nuget proxy ("mirror") and a hosted nuget repository
// ("local") into a group ("all") behind the real chi route shape of
// /repos/{name}.
func nugetEnv(t *testing.T) http.Handler {
	t.Helper()
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	var upstream *httptest.Server //nolint:wsl // referenced by its own handler

	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/index.json":
			_, _ = fmt.Fprintf(w, `{"version":"3.0.0","resources":[{"@id":"%[1]s/flat/","@type":"PackageBaseAddress/3.0.0"},{"@id":"%[1]s/reg/","@type":"RegistrationsBaseUrl/3.6.0"}]}`, upstream.URL)
		case "/flat/remote/index.json":
			_, _ = io.WriteString(w, `{"versions":["1.0.0"]}`)
		case "/reg/remote/index.json":
			_, _ = fmt.Fprintf(w, `{"@id":"%[1]s/reg/remote/index.json","count":1,"items":[{"@id":"%[1]s/reg/remote/index.json#page/1.0.0/1.0.0","lower":"1.0.0","upper":"1.0.0","count":1,"items":`+
				`[{"@id":"%[1]s/reg/remote/1.0.0.json","packageContent":"%[1]s/flat/remote/1.0.0/remote.1.0.0.nupkg","catalogEntry":{"version":"1.0.0"}}]}]}`, upstream.URL)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	repo := func(id, name, typ string) *models.Repository {
		return &models.Repository{ID: id, Name: name, Format: "nuget", RepoType: typ, CacheTTLSeconds: 60, NegativeCacheTTLSeconds: 60}
	}

	mirror := repo("1", "mirror", "proxy")
	mirror.UpstreamURL = upstream.URL + "/v3/index.json"

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

	// The hosted router has the route shapes of the real nuget handler.
	hosted := chi.NewRouter()
	hosted.Get("/v3/package/{id}/index.json", func(w http.ResponseWriter, r *http.Request) {
		if chi.URLParam(r, "id") != "local" {
			http.NotFound(w, r)
			return
		}

		_, _ = io.WriteString(w, `{"versions":["2.0.0"]}`)
	})
	hosted.Get("/v3/registration/{id}/index.json", func(w http.ResponseWriter, r *http.Request) {
		if chi.URLParam(r, "id") != "local" {
			http.NotFound(w, r)
			return
		}

		idx := nugetBase + "/repos/local/v3/registration/local/index.json"
		_, _ = fmt.Fprintf(w, `{"@id":%[1]q,"count":1,"items":[{"@id":"%[1]s#page/2.0.0/2.0.0","lower":"2.0.0","upper":"2.0.0","count":1,"items":`+
			`[{"@id":"%[2]s/repos/local/v3/registration/local/2.0.0.json","packageContent":"%[2]s/repos/local/v3/package/local/2.0.0/local.2.0.0.nupkg","catalogEntry":{"version":"2.0.0"}}]}]}`, idx, nugetBase)
	})
	hosted.Get("/v3/package/{id}/{version}/{file}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hosted-file:"+chi.URLParam(r, "id")+"/"+chi.URLParam(r, "version")+"/"+chi.URLParam(r, "file"))
	})

	d := New(log, db, proxy.NewService(log, db, store), denyAuthorizer{}, map[string]http.Handler{"nuget": hosted}).WithBaseURL(nugetBase)

	r := chi.NewRouter()
	r.Route("/repos/{name}", func(r chi.Router) {
		r.Use(d.Middleware)
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "format-handler", http.StatusTeapot) })
	})

	return r
}

func TestNuGetProxyAndGroupThroughDispatch(t *testing.T) {
	h := nugetEnv(t)

	packageContents := func(w *httptest.ResponseRecorder) []string {
		var d struct {
			Items []struct {
				Items []struct {
					PackageContent string `json:"packageContent"` //nolint:tagliatelle // NuGet wire format
				} `json:"items"`
			} `json:"items"`
		}

		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatalf("not JSON: %v: %s", err, w.Body.String())
		}

		var out []string

		for _, p := range d.Items {
			for _, it := range p.Items {
				out = append(out, it.PackageContent)
			}
		}

		return out
	}

	t.Run("proxy rewrites the registration to itself", func(t *testing.T) {
		w := npmGet(h, "/repos/mirror/v3/registration/remote/index.json")

		got := packageContents(w)
		if w.Code != 200 || len(got) != 1 || got[0] != nugetBase+"/repos/mirror/v3/package/remote/1.0.0/remote.1.0.0.nupkg" {
			t.Errorf("= %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("group merges a hosted and a proxy member", func(t *testing.T) {
		for pkg, want := range map[string]string{
			"local":  nugetBase + "/repos/all/v3/package/local/2.0.0/local.2.0.0.nupkg",
			"remote": nugetBase + "/repos/all/v3/package/remote/1.0.0/remote.1.0.0.nupkg",
		} {
			w := npmGet(h, "/repos/all/v3/registration/"+pkg+"/index.json")
			if got := packageContents(w); w.Code != 200 || len(got) != 1 || got[0] != want {
				t.Errorf("registration of %s = %d %s", pkg, w.Code, w.Body.String())
			}

			if w := npmGet(h, "/repos/all/v3/package/"+pkg+"/index.json"); w.Code != 200 || !strings.Contains(w.Body.String(), `"versions"`) {
				t.Errorf("versions of %s = %d %s", pkg, w.Code, w.Body.String())
			}
		}
	})

	t.Run("group package file is answered by the first member that has it", func(t *testing.T) {
		w := npmGet(h, "/repos/all/v3/package/local/2.0.0/local.2.0.0.nupkg")
		if w.Code != 200 || w.Body.String() != "hosted-file:local/2.0.0/local.2.0.0.nupkg" {
			t.Errorf("= %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("group service index is its own", func(t *testing.T) {
		w := npmGet(h, "/repos/all/v3/index.json")
		if w.Code != 200 || !strings.Contains(w.Body.String(), nugetBase+"/repos/all/v3/registration/") {
			t.Errorf("= %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("unknown package", func(t *testing.T) {
		if w := npmGet(h, "/repos/all/v3/registration/nope/index.json"); w.Code != http.StatusNotFound {
			t.Errorf("= %d, want 404", w.Code)
		}
	})
}
