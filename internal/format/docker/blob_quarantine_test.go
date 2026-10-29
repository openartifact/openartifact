// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

var errBlobQuarantineLookup = errors.New("quarantine lookup unavailable")

// fakeBlobHolds answers DockerBlobQuarantined: the digests that belong to a
// quarantined image (the real query joins the manifest references to the
// quarantine state) and an error to fail the lookup with.
type fakeBlobHolds struct {
	held    map[string]bool
	holdErr error
}

func (f *fakeBlobHolds) DockerBlobQuarantined(_ context.Context, _, digest string) (bool, error) {
	if f.holdErr != nil {
		return false, f.holdErr
	}

	return f.held[digest], nil
}

// TestBlobOfQuarantinedImage: a layer or config of an image quarantined under a
// tag row id is refused on GET and HEAD, a failing lookup is refused too (fail
// closed), and a blob of another image is still served.
func TestBlobOfQuarantinedImage(t *testing.T) {
	db := newEventsDB()
	h, store := newTestHandler(t, db)

	put := func(payload string) string {
		sum := sha256.Sum256([]byte(payload))

		sha, err := store.Put(
			context.Background(),
			bytes.NewReader([]byte(payload)),
			map[string]string{"sha256": hex.EncodeToString(sum[:])},
		)
		if err != nil {
			t.Fatal(err)
		}

		return "sha256:" + sha
	}

	layer, sibling := put("layer of the quarantined image"), put("layer of a clean image")
	blobs := map[string]*models.DockerBlob{}

	for _, d := range []string{layer, sibling} {
		blobs[d] = &models.DockerBlob{ID: "b-" + d[:15], RepositoryID: "repo-1", Digest: d, BlobSHA256: d[7:], Size: 5}
	}

	serve := func(method, digest string) int {
		db.blob = blobs[digest]
		req := withChiVars(
			httptest.NewRequestWithContext(context.Background(), method, "/v2/myrepo/blobs/"+digest, nil),
			map[string]string{"name": "myrepo", "digest": digest},
		)
		w := httptest.NewRecorder()

		if method == http.MethodHead {
			h.handleHeadBlob(w, req)
		} else {
			h.handleGetBlob(w, req)
		}

		return w.Code
	}

	for _, m := range []string{http.MethodGet, http.MethodHead} {
		if c := serve(m, layer); c != http.StatusOK {
			t.Fatalf("%s before quarantine = %d, want 200", m, c)
		}
	}

	db.held = map[string]bool{layer: true}

	for _, m := range []string{http.MethodGet, http.MethodHead} {
		if c := serve(m, layer); c != http.StatusForbidden {
			t.Errorf("%s of a quarantined image's blob = %d, want 403", m, c)
		}

		if c := serve(m, sibling); c != http.StatusOK {
			t.Errorf("%s of a clean image's blob = %d, want 200", m, c)
		}
	}

	db.holdErr = errBlobQuarantineLookup

	for _, m := range []string{http.MethodGet, http.MethodHead} {
		if c := serve(m, sibling); c != http.StatusInternalServerError {
			t.Errorf("%s with a failing lookup = %d, want 500 (fail closed)", m, c)
		}
	}
}

// TestMountRefusesBlobOfQuarantinedImage: a cross-repository mount does not copy
// a layer of a quarantined image, the upload falls back to a session.
func TestMountRefusesBlobOfQuarantinedImage(t *testing.T) {
	layer := []byte("mounted layer")
	digest := digestOf(layer)
	db := newRegFakeDB("dst", "src")
	db.blobs[scoped("id-src", digest)] = &models.DockerBlob{
		ID: "b1", RepositoryID: "id-src", Digest: digest, BlobSHA256: digest[7:], Size: int64(len(layer)),
	}
	db.held = map[string]bool{digest: true}

	g := newRegistry(t, db)
	g.h.SetAuthorizer(&fakeAuthz{allow: true})

	rec := g.do(http.MethodPost, "/v2/dst/team/app/blobs/uploads/?mount="+digest+"&from=src/lib/base", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (upload fallback)", rec.Code)
	}

	if db.blobs[scoped("id-dst", digest)] != nil {
		t.Fatal("a layer of a quarantined image was mounted")
	}
}

// DockerBlobQuarantined names the fake's answer: the embedded handler store
// carries the method too.
func (f *manifestFakeDB) DockerBlobQuarantined(ctx context.Context, repoID, digest string) (bool, error) {
	return f.fakeBlobHolds.DockerBlobQuarantined(ctx, repoID, digest)
}

func (d *regFakeDB) DockerBlobQuarantined(ctx context.Context, repoID, digest string) (bool, error) {
	return d.fakeBlobHolds.DockerBlobQuarantined(ctx, repoID, digest)
}
