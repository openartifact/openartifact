// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	formatdocker "github.com/openartifact/openartifact/internal/format/docker"
	"github.com/openartifact/openartifact/internal/storage"
)

// dockerRegistry serves the docker handler over a real Postgres schema (all
// migrations applied) and a filesystem blob store.
type dockerRegistry struct {
	t      *testing.T
	pg     *database.PostgresDB
	router chi.Router
}

func newDockerRegistry(t *testing.T) *dockerRegistry {
	t.Helper()

	pg := freshPostgres(t, 5)

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	r := chi.NewRouter()
	formatdocker.NewHTTPHandler(newTestLogger(t), pg, store, "http://localhost").RegisterRoutes(r)

	return &dockerRegistry{t: t, pg: pg, router: r}
}

func (g *dockerRegistry) do(method, target, body string, headers ...string) *httptest.ResponseRecorder {
	g.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	rec := httptest.NewRecorder()
	g.router.ServeHTTP(rec, req)

	return rec
}

func ociDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (g *dockerRegistry) pushBlob(name string, content []byte) *httptest.ResponseRecorder {
	g.t.Helper()

	start := g.do(http.MethodPost, "/v2/"+name+"/blobs/uploads/", "")
	if start.Code != http.StatusAccepted {
		g.t.Fatalf("upload start = %d; body=%s", start.Code, start.Body.String())
	}

	return g.do(http.MethodPut, start.Header().Get("Location")+"?digest="+ociDigest(content), string(content))
}

func (g *dockerRegistry) putManifest(name, ref, body string) *httptest.ResponseRecorder {
	g.t.Helper()

	return g.do(http.MethodPut, "/v2/"+name+"/manifests/"+ref, body,
		"Content-Type", "application/vnd.oci.image.manifest.v1+json")
}

// TestDockerRegistry_RepushAndImagesAgainstPostgres exercises the unique
// constraints the handler unit tests cannot: re-pushing blobs and manifests
// (same and different tag), per-image tag scoping, and delete semantics.
func TestDockerRegistry_RepushAndImagesAgainstPostgres(t *testing.T) {
	g := newDockerRegistry(t)
	repoID := newRepo(t, g.pg, "reg", "docker")

	layer := []byte("layer pushed more than once")
	m1 := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","annotations":{"v":"1"}}`
	m2 := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","annotations":{"v":"2"}}`

	const img = "reg/org/team/app"

	for i := 1; i <= 2; i++ {
		if rec := g.pushBlob(img, layer); rec.Code != http.StatusCreated {
			t.Fatalf("blob push #%d = %d, want 201; body=%s", i, rec.Code, rec.Body.String())
		}
	}

	// The same blob pushed to another image and to the default image of the repo.
	for _, name := range []string{"reg/org/other", "reg"} {
		if rec := g.pushBlob(name, layer); rec.Code != http.StatusCreated {
			t.Fatalf("blob push to %s = %d, want 201; body=%s", name, rec.Code, rec.Body.String())
		}
	}

	for _, step := range []struct{ name, ref, body string }{
		{img, "1", m1},
		{img, "1", m1},                   // same tag, same manifest
		{img, "2", m1},                   // other tag, same manifest
		{img, "1", m2},                   // tag moves to a new manifest
		{img, ociDigest([]byte(m2)), m2}, // by digest, already stored
		{"reg/org/other", "1", m2},
		{"reg", "1", m1},
	} {
		if rec := g.putManifest(step.name, step.ref, step.body); rec.Code != http.StatusCreated {
			t.Fatalf("PUT %s:%s = %d, want 201; body=%s", step.name, step.ref, rec.Code, rec.Body.String())
		}
	}

	get := func(name, ref string) string {
		rec := g.do(http.MethodGet, "/v2/"+name+"/manifests/"+ref, "")
		if rec.Code != http.StatusOK {
			return "HTTP " + http.StatusText(rec.Code)
		}

		return rec.Body.String()
	}

	for _, c := range []struct{ name, ref, want string }{
		{img, "1", m2}, {img, "2", m1}, {"reg/org/other", "1", m2}, {"reg", "1", m1},
	} {
		if got := get(c.name, c.ref); got != c.want {
			t.Fatalf("GET %s:%s = %q, want %q", c.name, c.ref, got, c.want)
		}
	}

	g.checkListings(repoID)
	g.checkDeletes(img, m1, m2, get)
}

func (g *dockerRegistry) checkListings(repoID string) {
	t := g.t
	t.Helper()

	tags, err := g.pg.ListDockerTags(context.Background(), repoID, "org/team/app", "", 10)
	if err != nil || len(tags) != 2 {
		t.Fatalf("ListDockerTags(org/team/app) = %d tags, err=%v; want 2", len(tags), err)
	}

	all, err := g.pg.ListDockerTagsByRepo(context.Background(), repoID, 100)
	if err != nil || len(all) != 4 {
		t.Fatalf("ListDockerTagsByRepo = %d tags, err=%v; want 4", len(all), err)
	}

	comps, _, err := g.pg.ListComponentsByRepo(context.Background(), repoID, "docker", 1, 50)
	if err != nil {
		t.Fatalf("ListComponentsByRepo: %v", err)
	}

	names := map[string]int{}
	for _, c := range comps {
		names[c.Name]++
	}

	if names["reg/org/team/app"] != 2 || names["reg/org/other"] != 1 || names["reg"] != 1 {
		t.Fatalf("component names = %v, want reg/org/team/app x2, reg/org/other x1, reg x1", names)
	}
}

func (g *dockerRegistry) checkDeletes(img, m1, m2 string, get func(name, ref string) string) {
	t := g.t
	t.Helper()

	// Deleting a tag keeps the manifest; deleting by digest drops its tags.
	if rec := g.do(http.MethodDelete, "/v2/"+img+"/manifests/2", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("DELETE tag = %d; body=%s", rec.Code, rec.Body.String())
	}

	if got := get(img, ociDigest([]byte(m1))); got != m1 {
		t.Fatalf("manifest after tag delete = %q, want it kept", got)
	}

	if rec := g.do(http.MethodDelete, "/v2/"+img+"/manifests/"+ociDigest([]byte(m2)), ""); rec.Code != http.StatusAccepted {
		t.Fatalf("DELETE digest = %d; body=%s", rec.Code, rec.Body.String())
	}

	if got := get(img, "1"); !strings.HasPrefix(got, "HTTP") {
		t.Fatalf("tag 1 after its manifest was deleted = %q, want 404", got)
	}

	// The other image's copy of m2 shares the stored content and survives.
	if got := get("reg/org/other", "1"); got != m2 {
		t.Fatalf("other image manifest after delete = %q, want %q", got, m2)
	}
}
