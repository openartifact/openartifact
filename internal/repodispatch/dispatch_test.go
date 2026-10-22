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
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

type fakeDB struct {
	database.BaseMockDB

	mu      sync.Mutex
	repos   map[string]*models.Repository // by name
	links   map[string][]*models.RepositoryGroupMember
	entries map[string]*models.ProxyCacheEntry
}

func (d *fakeDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return d.repos[name], nil
}

func (d *fakeDB) GetRepositoryByID(_ context.Context, id string) (*models.Repository, error) {
	for _, r := range d.repos {
		if r.ID == id {
			return r, nil
		}
	}

	return nil, nil
}

func (d *fakeDB) ListGroupMemberRepositories(ctx context.Context, id string) ([]*models.Repository, error) {
	var repos []*models.Repository

	for _, l := range d.links[id] {
		if r, _ := d.GetRepositoryByID(ctx, l.MemberRepoID); r != nil {
			repos = append(repos, r)
		}
	}

	return repos, nil
}

func (d *fakeDB) GetProxyCacheEntry(_ context.Context, repoID, key string) (*models.ProxyCacheEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.entries[repoID+"|"+key], nil
}

func (d *fakeDB) UpsertProxyCacheEntry(_ context.Context, e *models.ProxyCacheEntry) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.entries[e.RepositoryID+"|"+e.CacheKey] = e

	return nil
}

func (d *fakeDB) EnsureBlob(_ context.Context, sha, _ string, _ int64) (string, error) {
	return sha, nil
}

// denyAuthorizer refuses reads of the named repositories.
type denyAuthorizer map[string]bool

func (a denyAuthorizer) Evaluate(_ context.Context, _ auth.Principal, repo, _, action string) (bool, error) {
	return action == "read" && !a[repo], nil
}

type env struct {
	router   http.Handler
	upstream *int
	nextHit  *bool
}

func newEnv(t *testing.T, authz Authorizer) *env {
	t.Helper()
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true") // httptest upstreams are loopback

	upstreamHits := 0

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++

		if r.URL.Path == "/up/mirrored.txt" {
			_, _ = io.WriteString(w, "from-upstream")
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)

	repo := func(id, name, format, typ string) *models.Repository {
		return &models.Repository{ID: id, Name: name, Format: format, RepoType: typ, CacheTTLSeconds: 60, NegativeCacheTTLSeconds: 60}
	}

	mirror := repo("3", "mirror", "raw", "proxy")
	mirror.UpstreamURL = upstream.URL + "/up"

	db := &fakeDB{
		repos: map[string]*models.Repository{
			"hosted":  repo("1", "hosted", "raw", "hosted"),
			"private": repo("2", "private", "raw", "hosted"),
			"mirror":  mirror,
			"all":     repo("4", "all", "raw", "group"),
			"dockerx": repo("5", "dockerx", "docker", "proxy"),
			"ivyx":    repo("6", "ivyx", "ivy", "proxy"),
		},
		links: map[string][]*models.RepositoryGroupMember{
			"4": {{MemberRepoID: "2", Priority: 0}, {MemberRepoID: "1", Priority: 1}, {MemberRepoID: "3", Priority: 2}},
		},
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

	// The hosted router reports which repository and path it was asked for.
	hosted := chi.NewRouter()
	hosted.HandleFunc("/*", func(w http.ResponseWriter, r *http.Request) {
		name, path := chi.URLParam(r, "name"), chi.URLParam(r, "*")
		if path == "shared.txt" || (name == "private" && path == "secret.txt") || (name == "hosted" && path == "own.txt") {
			_, _ = io.WriteString(w, name+":"+path)
			return
		}

		http.NotFound(w, r)
	})

	d := New(log, db, proxy.NewService(log, db, store), authz, map[string]http.Handler{"raw": hosted})
	nextHit := false

	r := chi.NewRouter()
	r.Route("/repos/raw/{name}", func(r chi.Router) {
		r.Use(d.Middleware)
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) {
			nextHit = true
			_, _ = io.WriteString(w, "format-handler")
		})
	})

	return &env{router: r, upstream: &upstreamHits, nextHit: &nextHit}
}

func (e *env) do(method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{IsAnonymous: true}))

	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)

	return w
}

func TestHostedRepoFallsThroughToFormatHandler(t *testing.T) {
	e := newEnv(t, denyAuthorizer{})

	if w := e.do(http.MethodGet, "/repos/raw/hosted/own.txt"); w.Body.String() != "format-handler" || !*e.nextHit {
		t.Errorf("body %q, next called %v", w.Body.String(), *e.nextHit)
	}

	if w := e.do(http.MethodGet, "/repos/raw/unknown/x"); w.Body.String() != "format-handler" {
		t.Errorf("unknown repo body %q, want the format handler to produce its own 404", w.Body.String())
	}
}

func TestProxyRepoIsServedFromUpstreamAndCached(t *testing.T) {
	e := newEnv(t, denyAuthorizer{})

	for range 2 {
		if w := e.do(http.MethodGet, "/repos/raw/mirror/mirrored.txt"); w.Code != 200 || w.Body.String() != "from-upstream" {
			t.Fatalf("= %d %q", w.Code, w.Body.String())
		}
	}

	if *e.upstream != 1 {
		t.Errorf("upstream requests = %d, want 1", *e.upstream)
	}

	if *e.nextHit {
		t.Error("format handler ran for a proxy repository")
	}
}

func TestWritesToProxyAndGroupAreRefusedBeforeFormatHandler(t *testing.T) {
	e := newEnv(t, denyAuthorizer{})

	for _, repo := range []string{"mirror", "all"} {
		for _, m := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
			if w := e.do(m, "/repos/raw/"+repo+"/f.txt"); w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", m, repo, w.Code)
			}
		}
	}

	if *e.nextHit {
		t.Error("a write reached the format handler")
	}
}

func TestUnsupportedProxyFormatIs501(t *testing.T) {
	e := newEnv(t, denyAuthorizer{})

	if w := e.do(http.MethodGet, "/repos/raw/ivyx/f"); w.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", w.Code)
	}
}

func TestGroupResolvesHostedAndProxyMembersInOrder(t *testing.T) {
	e := newEnv(t, denyAuthorizer{})

	cases := map[string]string{
		"own.txt":      "hosted:own.txt",     // private (first) lacks it, hosted has it
		"shared.txt":   "private:shared.txt", // first member wins
		"mirrored.txt": "from-upstream",      // only the proxy member has it
	}

	for path, want := range cases {
		if w := e.do(http.MethodGet, "/repos/raw/all/"+path); w.Code != 200 || w.Body.String() != want {
			t.Errorf("%s = %d %q, want %q", path, w.Code, w.Body.String(), want)
		}
	}

	if w := e.do(http.MethodGet, "/repos/raw/all/absent.txt"); w.Code != http.StatusNotFound {
		t.Errorf("absent = %d, want 404", w.Code)
	}
}

func TestGroupDoesNotExposeMemberTheCallerCannotRead(t *testing.T) {
	e := newEnv(t, denyAuthorizer{"private": true})

	if w := e.do(http.MethodGet, "/repos/raw/all/secret.txt"); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "secret") {
		t.Errorf("secret.txt = %d %q, want 404", w.Code, w.Body.String())
	}

	if w := e.do(http.MethodGet, "/repos/raw/all/shared.txt"); w.Body.String() != "hosted:shared.txt" {
		t.Errorf("shared.txt = %q, want the readable member's copy", w.Body.String())
	}
}

func TestGroupWithoutPrincipalFailsClosed(t *testing.T) {
	e := newEnv(t, denyAuthorizer{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/raw/all/shared.txt", nil)
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
