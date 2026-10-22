// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// manifestFakeDB is a minimal in-memory Database for manifest-path tests
// (push, get, referrers, delete). Embeds BaseMockDB so any method these
// tests don't exercise returns a descriptive error instead of panicking.
// Manifests and tags are scoped per (image, digest|name) like the real tables,
// and the create/upsert methods are idempotent like the real queries.
type manifestFakeDB struct {
	database.BaseMockDB

	repo *models.Repository

	manifests map[string]*models.DockerManifest // image|digest -> manifest
	tags      map[string]*models.DockerTag      // image|name -> tag

	ledger quotaLedger // quota reservations (see quota_test.go)
}

func newManifestFakeDB(repoID string) *manifestFakeDB {
	return &manifestFakeDB{
		repo:      &models.Repository{ID: repoID},
		manifests: map[string]*models.DockerManifest{},
		tags:      map[string]*models.DockerTag{},
	}
}

func scoped(image, key string) string { return image + "|" + key }

// manifest returns the manifest stored for the default image.
func (f *manifestFakeDB) manifest(digest string) *models.DockerManifest {
	return f.manifests[scoped("", digest)]
}

func (f *manifestFakeDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if f.repo != nil && f.repo.Name != "" && f.repo.Name != name {
		return nil, nil
	}

	return f.repo, nil
}

func (f *manifestFakeDB) EnsureBlob(_ context.Context, sha256, _ string, _ int64) (string, error) {
	return "blob-" + sha256, nil
}

func (f *manifestFakeDB) CreateDockerManifest(_ context.Context, m *models.DockerManifest) error {
	k := scoped(m.Image, m.Digest)
	_, existed := f.manifests[k]
	f.manifests[k] = m
	m.Inserted = !existed

	return nil
}

func (f *manifestFakeDB) GetDockerManifest(_ context.Context, _, image, digest string) (*models.DockerManifest, error) {
	return f.manifests[scoped(image, digest)], nil
}

func (f *manifestFakeDB) DeleteDockerManifest(_ context.Context, _, image, digest string) error {
	k := scoped(image, digest)
	if _, ok := f.manifests[k]; !ok {
		return database.ErrDockerRowNotFound
	}

	delete(f.manifests, k)

	return nil
}

func (f *manifestFakeDB) UpsertDockerTag(_ context.Context, tag *models.DockerTag) error {
	f.tags[scoped(tag.Image, tag.Name)] = tag
	return nil
}

func (f *manifestFakeDB) GetDockerTag(_ context.Context, _, image, name string) (*models.DockerTag, error) {
	return f.tags[scoped(image, name)], nil
}

func (f *manifestFakeDB) DeleteDockerTag(_ context.Context, _, image, name string) error {
	delete(f.tags, scoped(image, name))
	return nil
}

func (f *manifestFakeDB) DeleteDockerTagsByDigest(_ context.Context, _, image, digest string) error {
	for k, t := range f.tags {
		if t.Image == image && t.ManifestDigest == digest {
			delete(f.tags, k)
		}
	}

	return nil
}

func (f *manifestFakeDB) ListDockerTags(_ context.Context, _, image, last string, n int) ([]*models.DockerTag, error) {
	var out []*models.DockerTag

	for _, t := range f.tags {
		if t.Image == image && t.Name > last {
			out = append(out, t)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	if len(out) > n {
		out = out[:n]
	}

	return out, nil
}

func (f *manifestFakeDB) ListDockerManifestsBySubject(_ context.Context, _, image, subjectDigest string) ([]*models.DockerManifest, error) {
	var out []*models.DockerManifest

	for _, m := range f.manifests {
		if m.Image == image && m.SubjectDigest == subjectDigest {
			out = append(out, m)
		}
	}

	return out, nil
}

// BlobReferenceCount mirrors the real docker_manifests join: a blob is
// referenced as long as some manifest in the (fake) table still points at it.
func (f *manifestFakeDB) BlobReferenceCount(_ context.Context, sha256 string) (int, error) {
	for _, m := range f.manifests {
		if manifestBlobKey(m.Digest) == sha256 {
			return 1, nil
		}
	}

	return 0, nil
}

func putManifest(t *testing.T, h *HTTPHandler, reference string, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	req := withChiVars(
		httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/v2/myrepo/manifests/"+reference, bytes.NewReader(body)),
		map[string]string{"name": "myrepo", "reference": reference},
	)
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")

	rec := httptest.NewRecorder()
	h.handlePutManifest(rec, req)

	return rec
}

func TestHandlePutManifest_PersistsBlobAndRoundTrips(t *testing.T) {
	db := newManifestFakeDB("repo-1")
	h, store := newTestHandler(t, db)

	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)

	rec := putManifest(t, h, "latest", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	digest := rec.Header().Get("Docker-Content-Digest")
	if digest == "" {
		t.Fatal("expected Docker-Content-Digest header")
	}

	stored := db.manifest(digest)
	if stored == nil {
		t.Fatal("expected manifest to be persisted")
	}

	if stored.BlobID == "" {
		t.Fatal("expected BlobID to be set (blob_id is NOT NULL on docker_manifests)")
	}

	// Content round-trips through the blob store, not a DB Content column.
	rc, err := store.Get(context.Background(), manifestBlobKey(digest))
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read stored manifest: %v", err)
	}

	if string(got) != string(body) {
		t.Fatalf("stored content = %q, want %q", got, body)
	}

	// GET (by the tag just pushed) serves the same content back.
	getReq := withChiVars(
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v2/myrepo/manifests/latest", nil),
		map[string]string{"name": "myrepo", "reference": "latest"},
	)
	getRec := httptest.NewRecorder()
	h.handleGetManifest(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}

	if getRec.Body.String() != string(body) {
		t.Fatalf("GET body = %q, want %q", getRec.Body.String(), body)
	}
}

func TestHandlePutManifest_DigestMismatchRejected(t *testing.T) {
	db := newManifestFakeDB("repo-1")
	h, _ := newTestHandler(t, db)

	body := []byte(`{"schemaVersion":2}`)
	wrongDigest := "sha256:" + strings.Repeat("0", 64)

	rec := putManifest(t, h, wrongDigest, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	if len(db.manifests) != 0 {
		t.Fatal("manifest must not be persisted on digest mismatch")
	}
}

func TestHandlePutManifest_SubjectAndReferrers(t *testing.T) {
	db := newManifestFakeDB("repo-1")
	h, _ := newTestHandler(t, db)

	baseBody := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)

	baseRec := putManifest(t, h, "base", baseBody)
	if baseRec.Code != http.StatusCreated {
		t.Fatalf("base push status = %d, want 201; body=%s", baseRec.Code, baseRec.Body.String())
	}

	baseDigest := baseRec.Header().Get("Docker-Content-Digest")

	referrerBody := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json",` +
		`"artifactType":"application/vnd.example.sbom.v1",` +
		`"subject":{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + baseDigest + `","size":1}}`)

	refRec := putManifest(t, h, "sbom", referrerBody)
	if refRec.Code != http.StatusCreated {
		t.Fatalf("referrer push status = %d, want 201; body=%s", refRec.Code, refRec.Body.String())
	}

	if got := refRec.Header().Get("Oci-Subject"); got != baseDigest {
		t.Fatalf("OCI-Subject header = %q, want %q", got, baseDigest)
	}

	getReq := withChiVars(
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v2/myrepo/referrers/"+baseDigest, nil),
		map[string]string{"name": "myrepo", "digest": baseDigest},
	)
	getRec := httptest.NewRecorder()
	h.handleGetReferrers(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("referrers status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}

	var idx struct {
		Manifests []struct {
			Digest       string `json:"digest"`
			ArtifactType string `json:"artifactType"` //nolint:tagliatelle // OCI image-spec wire format is camelCase
		} `json:"manifests"`
	}

	if err := json.Unmarshal(getRec.Body.Bytes(), &idx); err != nil {
		t.Fatalf("decode referrers response: %v", err)
	}

	if len(idx.Manifests) != 1 {
		t.Fatalf("manifests = %d, want 1; body=%s", len(idx.Manifests), getRec.Body.String())
	}

	if idx.Manifests[0].ArtifactType != "application/vnd.example.sbom.v1" {
		t.Fatalf("artifactType = %q, want application/vnd.example.sbom.v1", idx.Manifests[0].ArtifactType)
	}
}

func TestHandleDeleteManifest_ReclaimsUnreferencedBlob(t *testing.T) {
	db := newManifestFakeDB("repo-1")
	h, store := newTestHandler(t, db)

	body := []byte(`{"schemaVersion":2}`)

	putRec := putManifest(t, h, "latest", body)
	digest := putRec.Header().Get("Docker-Content-Digest")

	delReq := withChiVars(
		httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/v2/myrepo/manifests/"+digest, nil),
		map[string]string{"name": "myrepo", "reference": digest},
	)
	delRec := httptest.NewRecorder()
	h.handleDeleteManifest(delRec, delReq)

	if delRec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", delRec.Code, delRec.Body.String())
	}

	if db.manifest(digest) != nil {
		t.Fatal("expected manifest row to be deleted")
	}

	exists, err := store.Exists(context.Background(), manifestBlobKey(digest))
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}

	if exists {
		t.Fatal("expected the now-unreferenced manifest blob to be reclaimed from storage")
	}
}

// The manifest media type is whatever the pusher sent; /v2/ responses come
// from the application origin, so only registry JSON types may be echoed.
func TestManifestContentTypeIsRestrictedOnRead(t *testing.T) {
	for in, want := range map[string]string{
		"text/html":     "application/octet-stream",
		"image/svg+xml": "application/octet-stream",
		"application/vnd.oci.image.manifest.v1+json": "application/vnd.oci.image.manifest.v1+json",
	} {
		db := newManifestFakeDB("repo-1")
		h, _ := newTestHandler(t, db)

		body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
		put := withChiVars(
			httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/v2/myrepo/manifests/latest", bytes.NewReader(body)),
			map[string]string{"name": "myrepo", "reference": "latest"},
		)
		put.Header.Set("Content-Type", in)

		rec := httptest.NewRecorder()
		h.handlePutManifest(rec, put)

		if rec.Code != http.StatusCreated {
			t.Fatalf("%q: PUT status = %d: %s", in, rec.Code, rec.Body.String())
		}

		for name, serve := range map[string]func(http.ResponseWriter, *http.Request){"GET": h.handleGetManifest, "HEAD": h.handleHeadManifest} {
			req := withChiVars(
				httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v2/myrepo/manifests/latest", nil),
				map[string]string{"name": "myrepo", "reference": "latest"},
			)
			w := httptest.NewRecorder()
			serve(w, req)

			if got := w.Header().Get("Content-Type"); got != want {
				t.Errorf("%s after pushing %q: Content-Type = %q, want %q", name, in, got, want)
			}
		}
	}
}
