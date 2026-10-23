// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/database/models"
)

func TestDockerHandler_Meta(t *testing.T) {
	h := NewDockerHandler()
	if h.Name() != "docker" {
		t.Errorf("Name() = %q, want docker", h.Name())
	}

	if h.GetFileExtension() != ".json" {
		t.Errorf("GetFileExtension() = %q, want .json", h.GetFileExtension())
	}
}

func TestDockerHandler_Validate(t *testing.T) {
	h := NewDockerHandler()
	ctx := context.Background()

	t.Run("valid manifest with mediaType", func(t *testing.T) {
		manifest := `{"mediaType":"application/vnd.docker.distribution.manifest.v2+json","schemaVersion":2}`
		if err := h.Validate(ctx, strings.NewReader(manifest)); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("valid manifest with schemaVersion only", func(t *testing.T) {
		manifest := `{"schemaVersion":1,"name":"nginx"}`
		if err := h.Validate(ctx, strings.NewReader(manifest)); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("invalid: not JSON", func(t *testing.T) {
		if err := h.Validate(ctx, strings.NewReader("not json")); err == nil {
			t.Error("expected error for non-JSON")
		}
	})

	t.Run("invalid: missing required fields", func(t *testing.T) {
		manifest := `{"layers":[]}`
		if err := h.Validate(ctx, strings.NewReader(manifest)); err == nil {
			t.Error("expected error for missing mediaType/schemaVersion")
		}
	})
}

func TestDockerHandler_Parse(t *testing.T) {
	h := NewDockerHandler()
	ctx := context.Background()

	manifest := `{"mediaType":"application/vnd.docker.distribution.manifest.v2+json","schemaVersion":2}`

	result, err := h.Parse(ctx, strings.NewReader(manifest))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result["mediaType"] != "application/vnd.docker.distribution.manifest.v2+json" {
		t.Errorf("mediaType = %v, want application/vnd.docker.distribution.manifest.v2+json", result["mediaType"])
	}
}

func TestHandleUploadChunk_MismatchedRepositoryReturnsUnknownUpload(t *testing.T) {
	uid := uuid.New().String()

	db := &fakeDB{
		repo: &models.Repository{ID: "repo-other", Format: "docker"},
		upload: &models.DockerUpload{
			RepositoryID: "repo-actual",
			UploadUUID:   uid,
			Offset:       0,
		},
	}
	h, _ := newTestHandler(t, db)

	req := withChiVars(
		httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/v2/myrepo/blobs/uploads/"+uid, strings.NewReader("chunk")),
		map[string]string{"name": "myrepo", "uuid": uid},
	)
	rec := httptest.NewRecorder()

	h.handleUploadChunk(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}

	if !strings.Contains(rec.Body.String(), "BLOB_UPLOAD_UNKNOWN") {
		t.Fatalf("body = %q, want BLOB_UPLOAD_UNKNOWN", rec.Body.String())
	}

	if db.offset != 0 {
		t.Fatalf("offset = %d, want 0 (must not mutate on mismatch)", db.offset)
	}

	if db.deletedUpload {
		t.Fatal("upload record must not be deleted on mismatch")
	}

	if _, err := os.Stat(tempUploadPath(t, uid)); !os.IsNotExist(err) {
		t.Fatalf("expected no temp file on mismatch, stat err = %v", err)
	}
}
