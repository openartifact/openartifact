// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/storage"
)

// tpgStagingStore gives the harness' filesystem blob store the staging area of
// an object store: objects the test "uploaded" to the presigned URL live in
// memory, are versioned by content hash (as an ETag is) and are promoted into
// the real blob store.
type tpgStagingStore struct {
	storage.BlobStore

	mu     sync.Mutex
	staged map[string][]byte
}

func tpgVersion(b []byte) string { return `"` + tpoSHA(b) + `"` }

func (s *tpgStagingStore) put(id string, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.staged[id] = bytes.Clone(b)
}

func (s *tpgStagingStore) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, ok := s.staged[id]

	return ok
}

func (s *tpgStagingStore) StagingSupported() bool { return true }

func (s *tpgStagingStore) PresignStagingPut(_ context.Context, id string, _ int64, ttl time.Duration) (storage.PresignedPut, error) {
	return storage.PresignedPut{URL: "https://s3.example/staging/" + id, Expires: time.Now().Add(ttl)}, nil
}

func (s *tpgStagingStore) StatStaging(_ context.Context, id string) (storage.StagedObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, ok := s.staged[id]
	if !ok {
		return storage.StagedObject{}, storage.ErrStagingNotFound
	}

	return storage.StagedObject{Size: int64(len(b)), Version: tpgVersion(b)}, nil
}

func (s *tpgStagingStore) OpenStaging(_ context.Context, id, version string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, ok := s.staged[id]
	if !ok {
		return nil, storage.ErrStagingNotFound
	}

	if tpgVersion(b) != version {
		return nil, storage.ErrStagingChanged
	}

	return io.NopCloser(bytes.NewReader(bytes.Clone(b))), nil
}

func (s *tpgStagingStore) PromoteStaging(ctx context.Context, id, sha string, version string, metadata map[string]string) error {
	s.mu.Lock()
	b, ok := s.staged[id]
	s.mu.Unlock()

	if !ok {
		return storage.ErrStagingNotFound
	}

	if tpgVersion(b) != version {
		return storage.ErrStagingChanged
	}

	if metadata == nil {
		metadata = map[string]string{}
	}

	metadata["sha256"] = sha

	_, err := s.Put(ctx, bytes.NewReader(b), metadata)

	return err
}

func (s *tpgStagingStore) DeleteStaging(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.staged, id)

	return nil
}

func (s *tpgStagingStore) WalkStaging(context.Context, time.Time, func([]string) error) error {
	return nil
}

// tpgStart creates a presigned upload of content to path as the writer and
// "uploads" the bytes to its staging object.
func tpgStart(t *testing.T, env *tpoEnv, store *tpgStagingStore, repo, path string, content []byte) string {
	t.Helper()

	body, err := json.Marshal(api.CreatePresignedUploadRequest{
		Path: path, Size: int64(len(content)), SHA256: tpoSHA(content), ContentType: "application/octet-stream",
	})
	if err != nil {
		t.Fatalf("marshal create body: %v", err)
	}

	rec := tpoSend(t, env, tpoWriter, http.MethodPost, "/api/v1/repositories/"+repo+"/uploads", body)
	tpoExpect(t, rec, http.StatusCreated, "create presigned upload")

	id := tpoDecode[api.CreatePresignedUploadResponse](t, rec).ID
	store.put(id, content)

	return id
}

func tpgComplete(t *testing.T, env *tpoEnv, who, repo, id, query string) *httptest.ResponseRecorder {
	t.Helper()

	return tpoSend(t, env, who, http.MethodPost, "/api/v1/repositories/"+repo+"/uploads/"+id+"/complete"+query, nil)
}

func TestTrashPresignGuard_CompleteOverImmutableRaw(t *testing.T) {
	env := tpoNewEnv(t)

	pg := harness.Database
	store := &tpgStagingStore{BlobStore: harness.BlobStore, staged: map[string][]byte{}}
	evaluator := auth.NewEvaluator(pg, harness.Log, false)
	api.NewPresignedUploadHandler(harness.Log, pg, store, evaluator, 30*time.Minute, 0, nil).Register(env.router)

	const (
		repo = "tpg-imm-raw"
		path = "dist/tool-1.0.tgz"
	)

	tpoCreateRepo(t, env, repo, "raw", map[string]any{"immutable_releases": true})

	target := tpoRawURL(repo, path)
	original := tpoContent("tpg-original")

	tpoPut(t, env, target, original, http.StatusCreated)

	replacement := tpoContent("tpg-replacement")
	id := tpgStart(t, env, store, repo, path, replacement)

	// The presigned completion enforces the same guard as a raw PUT.
	rec := tpgComplete(t, env, tpoWriter, repo, id, "")
	tpoExpectError(t, rec, http.StatusConflict, "immutable_release", "complete over an existing release")

	if got := rec.Body.String(); !strings.Contains(got, "repository has immutable releases: cannot overwrite "+path) {
		t.Errorf("409 body = %s, want the static immutable-release message", got)
	}

	tpoAssertServes(t, env, target, original)

	if !tpoBlobStored(t, tpoSHA(original)) {
		t.Error("the original blob was removed")
	}

	if tpoBlobStored(t, tpoSHA(replacement)) {
		t.Error("the replacement blob was promoted although the guard refused it")
	}

	// The refused upload is not completed: a retry is refused again instead of
	// replaying a result, and a non-admin's ?force=true changes nothing.
	if n := tpoCount(t, `SELECT count(*) FROM presigned_uploads WHERE id = $1 AND completed_at IS NULL AND completing_since IS NULL`, id); n != 1 {
		t.Fatalf("pending unclaimed upload rows = %d, want 1 (the upload must stay retryable)", n)
	}

	tpoExpectError(t, tpgComplete(t, env, tpoWriter, repo, id, ""), http.StatusConflict, "immutable_release", "retried complete")
	tpoExpectError(t, tpgComplete(t, env, tpoWriter, repo, id, "?force=true"), http.StatusConflict, "immutable_release", "non-admin force")
	tpoAssertServes(t, env, target, original)

	// An admin may override the one request, completing the same upload.
	rec = tpgComplete(t, env, tpoAdmin, repo, id, "?force=true")
	tpoExpect(t, rec, http.StatusCreated, "admin force complete")
	tpoAssertServes(t, env, target, replacement)

	if store.has(id) {
		t.Error("the staging object was left behind after a successful complete")
	}

	// Now completed: the retry is the idempotent replay.
	tpoExpect(t, tpgComplete(t, env, tpoAdmin, repo, id, ""), http.StatusOK, "replay of the completed upload")
}

func TestTrashPresignGuard_NewPathAndMutableRepo(t *testing.T) {
	env := tpoNewEnv(t)

	pg := harness.Database
	store := &tpgStagingStore{BlobStore: harness.BlobStore, staged: map[string][]byte{}}
	evaluator := auth.NewEvaluator(pg, harness.Log, false)
	api.NewPresignedUploadHandler(harness.Log, pg, store, evaluator, 30*time.Minute, 0, nil).Register(env.router)

	const repo = "tpg-imm-new"

	tpoCreateRepo(t, env, repo, "raw", map[string]any{"immutable_releases": true})

	// A new path is not an overwrite: allowed in an immutable repository.
	fresh := tpoContent("tpg-fresh")
	id := tpgStart(t, env, store, repo, "dist/new.tgz", fresh)
	tpoExpect(t, tpgComplete(t, env, tpoWriter, repo, id, ""), http.StatusCreated, "complete on a new path")
	tpoAssertServes(t, env, tpoRawURL(repo, "dist/new.tgz"), fresh)

	// Without immutable_releases an overwrite goes through.
	tpoPatchRepo(t, env, repo, `{"immutable_releases":false}`)

	replaced := tpoContent("tpg-replaced")
	id = tpgStart(t, env, store, repo, "dist/new.tgz", replaced)
	tpoExpect(t, tpgComplete(t, env, tpoWriter, repo, id, ""), http.StatusCreated, "overwrite in a mutable repository")
	tpoAssertServes(t, env, tpoRawURL(repo, "dist/new.tgz"), replaced)
}
