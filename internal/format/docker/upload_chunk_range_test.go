// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/database/models"
)

// TestHandleUploadChunk_RejectsNonContiguousRange exercises the HTTP wiring
// around parseContentRange/validateChunkOffset: a PATCH whose declared
// Content-Range start does not match the upload's current offset must be
// rejected with 416 before anything is stored.
func TestHandleUploadChunk_RejectsNonContiguousRange(t *testing.T) {
	uid := uuid.New().String()

	db := &fakeDB{
		repo:   &models.Repository{ID: "repo-1", Format: "docker"},
		upload: &models.DockerUpload{Offset: 10, HashState: hashStateOf(t, "0123456789")},
	}
	h, _ := newTestHandler(t, db)

	req := withChiVars(
		httptest.NewRequestWithContext(
			context.Background(),
			http.MethodPatch,
			"/v2/myrepo/blobs/uploads/"+uid,
			strings.NewReader("gap chunk"),
		),
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

	// The offset must be untouched and no chunk stored.
	if db.offset != 0 || db.upload.Offset != 10 {
		t.Fatalf("offset = %d / %d, want the session untouched at 10", db.offset, db.upload.Offset)
	}

	if ids := uploadObjects(t, h); len(ids) != 0 {
		t.Fatalf("expected nothing to be stored on a rejected range, found %v", ids)
	}

	// The lease is given back: the client's next request is not refused as in use.
	again := withChiVars(
		httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/v2/myrepo/blobs/uploads/"+uid, strings.NewReader("x")),
		map[string]string{"name": "myrepo", "uuid": uid},
	)
	again.Header.Set("Content-Range", "50-50")

	rec = httptest.NewRecorder()
	h.handleUploadChunk(rec, again)

	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("second request = %d, want 416 again (the lease must have been released)", rec.Code)
	}
}

// TestHandleUploadChunk_AcceptsContiguousRange is the mirror-image happy
// path: a Content-Range whose start matches the current offset proceeds
// exactly like the no-header case.
func TestHandleUploadChunk_AcceptsContiguousRange(t *testing.T) {
	uid := uuid.New().String()

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
