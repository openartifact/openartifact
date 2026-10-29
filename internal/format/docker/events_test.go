// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
)

// eventsDB adds blobs and quarantine lookups to manifestFakeDB.
type eventsDB struct {
	*manifestFakeDB

	blob *models.DockerBlob
}

func (d *eventsDB) GetDockerBlob(_ context.Context, _, _ string) (*models.DockerBlob, error) {
	return d.blob, nil
}

func newEventsDB() *eventsDB {
	d := &eventsDB{manifestFakeDB: newManifestFakeDB("repo-1")}
	d.Keys = map[string]bool{}

	return d
}

func TestManifestPushAndDeleteEmitEvents(t *testing.T) {
	db := newEventsDB()
	h, _ := newTestHandler(t, db)
	pub := &formattest.Publisher{}
	h.SetPublisher(pub)

	body := []byte(`{"schemaVersion":2}`)

	rec := putManifest(t, h, "latest", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("push status = %d", rec.Code)
	}

	digest := rec.Header().Get("Docker-Content-Digest")

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events after push = %+v", evs)
	}

	if a := evs[0].Artifact(); a["name"] != "myrepo" || a["version"] != "latest" || a["sha256"] != manifestBlobKey(digest) {
		t.Errorf("artifact = %v", a)
	}

	delReq := withChiVars(
		httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/v2/myrepo/manifests/"+digest, nil),
		map[string]string{"name": "myrepo", "reference": digest},
	)
	delRec := httptest.NewRecorder()
	h.handleDeleteManifest(delRec, delReq)

	if delRec.Code != http.StatusAccepted {
		t.Fatalf("delete status = %d", delRec.Code)
	}

	evs = pub.Events()
	if len(evs) != 2 || evs[1].Type != webhook.EventArtifactDeleted {
		t.Fatalf("events after delete = %+v", evs)
	}
}

func TestGetManifestQuarantine(t *testing.T) {
	db := newEventsDB()
	h, _ := newTestHandler(t, db)

	rec := putManifest(t, h, "latest", []byte(`{"schemaVersion":2}`))
	digest := rec.Header().Get("Docker-Content-Digest")

	get := func() int {
		req := withChiVars(
			httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v2/myrepo/manifests/latest", nil),
			map[string]string{"name": "myrepo", "reference": "latest"},
		)
		w := httptest.NewRecorder()
		h.handleGetManifest(w, req)

		return w.Code
	}

	if c := get(); c != http.StatusOK {
		t.Fatalf("status = %d, want 200", c)
	}

	db.Keys[digest] = true

	if c := get(); c != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", c)
	}
}

func TestGetBlobQuarantine(t *testing.T) {
	db := newEventsDB()
	h, store := newTestHandler(t, db)

	payload := []byte("layer-bytes")

	sum := sha256.Sum256(payload)

	sha, err := store.Put(context.Background(), bytes.NewReader(payload), map[string]string{
		"sha256": hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatal(err)
	}

	digest := "sha256:" + sha
	db.blob = &models.DockerBlob{
		ID:           "b1",
		RepositoryID: "repo-1",
		Digest:       digest,
		MediaType:    "application/octet-stream",
		Size:         int64(len(payload)),
		BlobSHA256:   sha,
	}

	get := func() int {
		req := withChiVars(
			httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v2/myrepo/blobs/"+digest, nil),
			map[string]string{"name": "myrepo", "digest": digest},
		)
		w := httptest.NewRecorder()
		h.handleGetBlob(w, req)

		return w.Code
	}

	if c := get(); c != http.StatusOK {
		t.Fatalf("status = %d, want 200", c)
	}

	db.Keys[digest] = true

	if c := get(); c != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", c)
	}
}
