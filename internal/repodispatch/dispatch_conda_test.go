// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repodispatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/klauspost/compress/zstd"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// condaDB is fakeDB that also knows the package names the hosted repository
// holds.
type condaDB struct {
	*fakeDB

	names map[string][]string
}

func (d *condaDB) ListCondaPackageNames(_ context.Context, repoID string) ([]string, error) {
	return d.names[repoID], nil
}

func (d *condaDB) HasCondaPackageName(_ context.Context, repoID, name string) (bool, error) {
	return slices.Contains(d.names[repoID], name), nil
}

// condaEnv wires a conda proxy ("mirror") over a fake channel and a hosted
// conda repository ("local") into a group ("all") behind the chi route shape of
// /repos/{name}.
func condaEnv(t *testing.T) (http.Handler, map[string][]byte) {
	t.Helper()
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	files := map[string][]byte{
		"linux-64/numpy-1.0-0.conda": []byte("numpy package bytes"),
		"linux-64/demo-2.0-0.conda":  []byte("evil demo"),
	}

	entry := func(name, file string) string {
		sum := sha256.Sum256(files[file])

		return fmt.Sprintf(`%q:{"name":%q,"version":"1.0","build":"0","sha256":%q,"size":%d}`,
			filepath.Base(file), name, hex.EncodeToString(sum[:]), len(files[file]))
	}

	repodata := []byte(`{"info":{"subdir":"linux-64"},"packages":{},"packages.conda":{` +
		entry("numpy", "linux-64/numpy-1.0-0.conda") + "," + entry("demo", "linux-64/demo-2.0-0.conda") + `}}`)

	var zbuf bytes.Buffer

	enc, err := zstd.NewWriter(&zbuf)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = enc.Write(repodata)
	_ = enc.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/channel/")

		switch {
		case p == "linux-64/repodata.json.zst":
			_, _ = w.Write(zbuf.Bytes())
		case p == "linux-64/repodata.json":
			_, _ = w.Write(repodata)
		case files[p] != nil:
			_, _ = w.Write(files[p])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	repo := func(id, name, typ string) *models.Repository {
		return &models.Repository{
			ID: id, Name: name, Format: "conda", RepoType: typ, CacheTTLSeconds: 60, NegativeCacheTTLSeconds: 60,
		}
	}

	mirror := repo("1", "mirror", "proxy")
	mirror.UpstreamURL = upstream.URL + "/channel"

	all := repo("3", "all", "group")

	db := &condaDB{
		fakeDB: &fakeDB{
			repos:   map[string]*models.Repository{"local": repo("2", "local", "hosted"), "mirror": mirror, "all": all},
			links:   map[string][]*models.RepositoryGroupMember{"3": {{MemberRepoID: "2", Priority: 0}, {MemberRepoID: "1", Priority: 1}}},
			entries: map[string]*models.ProxyCacheEntry{},
		},
		names: map[string][]string{"2": {"demo"}},
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
	hosted.Get("/{platform}/repodata.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"info":{"subdir":"linux-64"},"packages":{},"packages.conda":{"demo-1.0-0.conda":`+
			`{"name":"demo","version":"1.0","build":"0","sha256":"`+strings.Repeat("0", 64)+`","size":1}}}`)
	})
	hosted.Get("/{platform}/{filename}", func(w http.ResponseWriter, r *http.Request) {
		if chi.URLParam(r, "filename") != "demo-1.0-0.conda" {
			http.NotFound(w, r)
			return
		}

		_, _ = io.WriteString(w, "hosted demo")
	})

	d := New(log, db, proxy.NewService(log, db, store), denyAuthorizer{}, map[string]http.Handler{"conda": hosted})

	r := chi.NewRouter()
	r.Route("/repos/{name}", func(r chi.Router) {
		r.Use(d.Middleware)
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "format-handler", http.StatusTeapot) })
	})

	return r, files
}

func TestDispatch_CondaProxyAndGroup(t *testing.T) {
	h, files := condaEnv(t)

	get := func(path string) *httptest.ResponseRecorder { return npmGet(h, path) }

	t.Run("proxy passes the channel through and verifies packages", func(t *testing.T) {
		if w := get("/repos/mirror/linux-64/repodata.json.zst"); w.Code != http.StatusOK || w.Body.Len() == 0 {
			t.Errorf("repodata.json.zst = %d (%d bytes)", w.Code, w.Body.Len())
		}

		if w := get("/repos/mirror/linux-64/numpy-1.0-0.conda"); w.Code != http.StatusOK ||
			!bytes.Equal(w.Body.Bytes(), files["linux-64/numpy-1.0-0.conda"]) {
			t.Errorf("package = %d %q", w.Code, w.Body.String())
		}

		if w := get("/repos/mirror/linux-64/unlisted-1.0-0.conda"); w.Code != http.StatusNotFound {
			t.Errorf("unlisted package = %d, want 404", w.Code)
		}
	})

	t.Run("group merges repodata with hosted wins", func(t *testing.T) {
		w := get("/repos/all/linux-64/repodata.json")
		if w.Code != http.StatusOK {
			t.Fatalf("= %d: %s", w.Code, w.Body.String())
		}

		var doc struct {
			Conda map[string]json.RawMessage `json:"packages.conda"` //nolint:tagliatelle // conda repodata wire format
		}

		if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}

		if _, ok := doc.Conda["demo-1.0-0.conda"]; !ok || len(doc.Conda) != 2 {
			t.Errorf("merged = %v", doc.Conda)
		}

		if _, ok := doc.Conda["demo-2.0-0.conda"]; ok {
			t.Error("the proxy's version of a hosted name is in the merged repodata")
		}
	})

	t.Run("group packages come from the owning member", func(t *testing.T) {
		if w := get("/repos/all/linux-64/demo-1.0-0.conda"); w.Body.String() != "hosted demo" {
			t.Errorf("hosted package = %d %q", w.Code, w.Body.String())
		}

		if w := get("/repos/all/linux-64/numpy-1.0-0.conda"); w.Code != http.StatusOK ||
			!bytes.Equal(w.Body.Bytes(), files["linux-64/numpy-1.0-0.conda"]) {
			t.Errorf("proxied package = %d %q", w.Code, w.Body.String())
		}

		if w := get("/repos/all/linux-64/demo-2.0-0.conda"); w.Code != http.StatusNotFound {
			t.Errorf("proxy's file of a hosted name = %d, want 404", w.Code)
		}
	})

	t.Run("uploads to proxy and group are refused", func(t *testing.T) {
		for _, name := range []string{"mirror", "all"} {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/repos/"+name+"/linux-64", strings.NewReader("x"))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("POST %s = %d, want 405", name, w.Code)
			}
		}
	})
}
