// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repodispatch

import (
	"context"
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
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const proxiedManifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","layers":[]}`

type dockerEnv struct {
	router   http.Handler
	nextHit  *bool
	upstream *int
}

// newDockerEnv serves /v2/ for: "dhost" (hosted), "dprox" (proxy of an
// anonymous upstream registry holding team/app:1 and tags b,c) and "dgrp"
// (group of dhost, then dprox). The hosted registry is a stub that has
// team/hosted:1 and the tags a,b of team/app.
func newDockerEnv(t *testing.T, authz Authorizer) *dockerEnv {
	t.Helper()
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	hits := 0
	sum := sha256.Sum256([]byte(proxiedManifest))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++

		switch r.URL.Path {
		case "/v2/team/app/manifests/1":
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Docker-Content-Digest", "sha256:"+hex.EncodeToString(sum[:]))
			_, _ = io.WriteString(w, proxiedManifest)
		case "/v2/team/app/tags/list":
			_, _ = io.WriteString(w, `{"name":"team/app","tags":["b","c"]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	repo := func(id, name, typ string) *models.Repository {
		return &models.Repository{ID: id, Name: name, Format: "docker", RepoType: typ, CacheTTLSeconds: 60, NegativeCacheTTLSeconds: 60}
	}

	// This group merges its members; the default (hosted members win a
	// name) is covered in internal/group.
	mergingGroup := func(g *models.Repository) *models.Repository {
		g.MergeProxyVersions = true
		return g
	}

	prox := repo("2", "dprox", "proxy")
	prox.UpstreamURL = upstream.URL

	db := &fakeDB{
		repos: map[string]*models.Repository{
			"dhost": repo("1", "dhost", "hosted"),
			"dprox": prox,
			"dgrp":  mergingGroup(repo("3", "dgrp", "group")),
		},
		links:   map[string][]*models.RepositoryGroupMember{"3": {{MemberRepoID: "1", Priority: 0}, {MemberRepoID: "2", Priority: 1}}},
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
	hosted.HandleFunc("/v2/*", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/dhost/team/hosted/manifests/1":
			w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
			_, _ = io.WriteString(w, "hosted-manifest")
		case "/v2/dhost/team/app/tags/list":
			_, _ = io.WriteString(w, `{"name":"dhost/team/app","tags":["a","b"]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`)
		}
	})

	d := New(log, db, proxy.NewService(log, db, store), authz, map[string]http.Handler{"docker": hosted})
	nextHit := false

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(d.OCIMiddleware)
		r.HandleFunc("/v2/*", func(w http.ResponseWriter, _ *http.Request) {
			nextHit = true
			_, _ = io.WriteString(w, "registry-handler")
		})
	})

	return &dockerEnv{router: r, nextHit: &nextHit, upstream: &hits}
}

func (e *dockerEnv) do(method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{IsAnonymous: true}))

	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)

	return w
}

func TestOCIHostedRepoAndRegistryRootFallThrough(t *testing.T) {
	e := newDockerEnv(t, denyAuthorizer{})

	for _, p := range []string{"/v2/", "/v2/dhost/team/hosted/manifests/1", "/v2/unknown/team/app/manifests/1"} {
		*e.nextHit = false

		if w := e.do(http.MethodGet, p); w.Body.String() != "registry-handler" || !*e.nextHit {
			t.Errorf("GET %s = %q, want the registry handler", p, w.Body.String())
		}
	}
}

func TestOCIProxyIsPulledThroughAndCached(t *testing.T) {
	e := newDockerEnv(t, denyAuthorizer{})

	for range 2 {
		w := e.do(http.MethodGet, "/v2/dprox/team/app/manifests/1")
		if w.Code != http.StatusOK || w.Body.String() != proxiedManifest {
			t.Fatalf("= %d %q", w.Code, w.Body.String())
		}
	}

	if *e.upstream != 1 || *e.nextHit {
		t.Errorf("upstream requests = %d (want 1), registry handler ran = %v", *e.upstream, *e.nextHit)
	}
}

func TestOCIWritesToProxyAndGroupAreDenied(t *testing.T) {
	e := newDockerEnv(t, denyAuthorizer{})

	for _, repo := range []string{"dprox", "dgrp"} {
		for _, c := range []struct{ method, path string }{
			{http.MethodPost, "/v2/" + repo + "/team/app/blobs/uploads/"},
			{http.MethodPut, "/v2/" + repo + "/team/app/manifests/2"},
			{http.MethodDelete, "/v2/" + repo + "/team/app/manifests/1"},
		} {
			if w := e.do(c.method, c.path); w.Code != http.StatusMethodNotAllowed || !strings.Contains(w.Body.String(), "UNSUPPORTED") {
				t.Errorf("%s %s = %d %q, want 405 UNSUPPORTED", c.method, c.path, w.Code, w.Body.String())
			}
		}
	}

	if *e.nextHit || *e.upstream != 0 {
		t.Errorf("a write reached the registry handler (%v) or the upstream (%d)", *e.nextHit, *e.upstream)
	}
}

func TestOCIGroupResolvesFirstMemberThatHasIt(t *testing.T) {
	e := newDockerEnv(t, denyAuthorizer{})

	tests := []struct{ path, want string }{
		{"/v2/dgrp/team/hosted/manifests/1", "hosted-manifest"}, // first member
		{"/v2/dgrp/team/app/manifests/1", proxiedManifest},      // only the proxy member
	}

	for _, tt := range tests {
		w := e.do(http.MethodGet, tt.path)
		if w.Code != http.StatusOK || w.Body.String() != tt.want {
			t.Errorf("%s = %d %q, want %q", tt.path, w.Code, w.Body.String(), tt.want)
		}
	}

	if got := e.do(http.MethodGet, "/v2/dgrp/team/app/manifests/1").Header().Get("Docker-Content-Digest"); !strings.HasPrefix(got, "sha256:") {
		t.Errorf("group lost the proxy member's Docker-Content-Digest: %q", got)
	}

	w := e.do(http.MethodGet, "/v2/dgrp/team/nowhere/manifests/1")
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "MANIFEST_UNKNOWN") {
		t.Errorf("absent manifest = %d %q, want 404 MANIFEST_UNKNOWN", w.Code, w.Body.String())
	}

	w = e.do(http.MethodGet, "/v2/dgrp/team/nowhere/blobs/sha256:"+strings.Repeat("a", 64))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "BLOB_UNKNOWN") {
		t.Errorf("absent blob = %d %q, want 404 BLOB_UNKNOWN", w.Code, w.Body.String())
	}
}

func TestOCIGroupDoesNotExposeUnreadableMember(t *testing.T) {
	e := newDockerEnv(t, denyAuthorizer{"dhost": true})

	w := e.do(http.MethodGet, "/v2/dgrp/team/hosted/manifests/1")
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "hosted-manifest") {
		t.Errorf("= %d %q, want the unreadable member to behave as if it lacked the image", w.Code, w.Body.String())
	}
}

func TestOCIGroupTagListIsMergedAndPaginated(t *testing.T) {
	e := newDockerEnv(t, denyAuthorizer{})

	type tagList struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}

	get := func(q string) (tagList, string) {
		w := e.do(http.MethodGet, "/v2/dgrp/team/app/tags/list"+q)

		var l tagList
		if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil || w.Code != http.StatusOK {
			t.Fatalf("%s = %d %q", q, w.Code, w.Body.String())
		}

		return l, w.Header().Get("Link")
	}

	if l, _ := get(""); l.Name != "dgrp/team/app" || strings.Join(l.Tags, ",") != "a,b,c" {
		t.Errorf("merged list = %+v, want a,b,c once each under the group's name", l)
	}

	l, link := get("?n=2")
	if strings.Join(l.Tags, ",") != "a,b" || link != `</v2/dgrp/team/app/tags/list?last=b&n=2>; rel="next"` {
		t.Errorf("first page = %v link %q", l.Tags, link)
	}

	if l, link := get("?n=2&last=b"); strings.Join(l.Tags, ",") != "c" || link != "" {
		t.Errorf("last page = %v link %q", l.Tags, link)
	}

	if w := e.do(http.MethodGet, "/v2/dgrp/team/ghost/tags/list"); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "NAME_UNKNOWN") {
		t.Errorf("tags of an unknown image = %d %q", w.Code, w.Body.String())
	}
}

func TestDockerProxyHasNoRepoFiles(t *testing.T) {
	e := newEnv(t, denyAuthorizer{})

	// A docker proxy/group is registry-only: it must not be mirrored by path
	// under /repos/{name}/.
	if w := e.do(http.MethodGet, "/repos/raw/dockerx/anything"); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}

	if *e.upstream != 0 {
		t.Errorf("upstream requests = %d, want 0", *e.upstream)
	}
}
