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

// TestHandleUploadChunk_RejectsNonContiguousRange exercises the HTTP wiring
// around parseContentRange/validateChunkOffset: a PATCH whose declared
// Content-Range start does not match the upload's current offset must be
// rejected with 416 before anything is written to the temp file.
func TestHandleUploadChunk_RejectsNonContiguousRange(t *testing.T) {
	if err := os.MkdirAll("/tmp/openartifact/docker-uploads", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	uid := uuid.New().String()
	defer func() { _ = os.Remove(tempUploadPath(uid)) }()

	db := &fakeDB{
		repo:   &models.Repository{ID: "repo-1", Format: "docker"},
		upload: &models.DockerUpload{Offset: 10},
	}
	h, _ := newTestHandler(t, db)

	req := withChiVars(
		httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/v2/myrepo/blobs/uploads/"+uid, strings.NewReader("gap chunk")),
		map[string]string{"name": "myrepo", "uuid": uid},
	)
	req.Header.Set("Content-Range", "50-58")

	rec := httptest.NewRecorder()
	h.handleUploadChunk(rec, req)

	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416; body=%s", rec.Code, rec.Body.String())
	}

	if got, want := rec.Header().Get("Range"), "0-9"; got != want {
		t.Fatalf("Range header = %q, want %q", got, want)
	}

	// The offset must be untouched and nothing written to the temp file.
	if db.offset != 0 {
		t.Fatalf("offset = %d, want 0 (UpdateDockerUploadOffset must not be called)", db.offset)
	}

	if _, err := os.Stat(tempUploadPath(uid)); !os.IsNotExist(err) {
		t.Fatalf("expected no temp file to be written on a rejected chunk, stat err = %v", err)
	}
}

// TestHandleUploadChunk_AcceptsContiguousRange is the mirror-image happy
// path: a Content-Range whose start matches the current offset proceeds
// exactly like the no-header case.
func TestHandleUploadChunk_AcceptsContiguousRange(t *testing.T) {
	if err := os.MkdirAll("/tmp/openartifact/docker-uploads", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	uid := uuid.New().String()
	defer func() { _ = os.Remove(tempUploadPath(uid)) }()

	db := &fakeDB{
		repo:   &models.Repository{ID: "repo-1", Format: "docker"},
		upload: &models.DockerUpload{Offset: 0},
	}
	h, _ := newTestHandler(t, db)

	chunk := "first nine"
	req := withChiVars(
		httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/v2/myrepo/blobs/uploads/"+uid, strings.NewReader(chunk)),
		map[string]string{"name": "myrepo", "uuid": uid},
	)
	req.Header.Set("Content-Range", "0-9")

	rec := httptest.NewRecorder()
	h.handleUploadChunk(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}

	if db.offset != int64(len(chunk)) {
		t.Fatalf("offset = %d, want %d", db.offset, len(chunk))
	}
}
