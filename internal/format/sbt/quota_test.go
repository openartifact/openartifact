// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbt

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// quotaFake counts storage usage like the real reservation: limit 0 means
// unlimited, a reservation that does not fit changes nothing.
type quotaFake struct {
	limit     int64
	used      int64
	artifacts int
}

func (q *quotaFake) ReserveStorageUsage(_ context.Context, _ string, size int64, count int) (bool, error) {
	if q.limit > 0 && q.used+size > q.limit {
		return false, nil
	}

	q.used += size
	q.artifacts += count

	return true, nil
}

func (q *quotaFake) DecrementStorageUsage(_ context.Context, _ string, size int64, count int) error {
	q.used -= size
	q.artifacts -= count

	return nil
}

var errCreateFailed = errors.New("create failed")

// failingCreateDB makes the row insert fail.
type failingCreateDB struct{ *sbtDB }

func (failingCreateDB) CreateSBTArtifact(context.Context, *models.SBTArtifact) error {
	return errCreateFailed
}

func TestUploadReservesQuotaAndDeleteReleasesIt(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	if w := send(t, h, http.MethodPut, artifactURL, "data"); w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d", w.Code)
	}

	if db.used != 4 || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d, want 4/1", db.used, db.artifacts)
	}

	if w := send(t, h, http.MethodDelete, artifactURL, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", w.Code)
	}

	if db.used != 0 || db.artifacts != 0 {
		t.Errorf("quota after delete used=%d artifacts=%d, want 0/0", db.used, db.artifacts)
	}
}

func TestUploadOverQuotaReturns507AndPersistsNothing(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)
	db.limit = 3

	if w := send(t, h, http.MethodPut, artifactURL, "data"); w.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507", w.Code)
	}

	if len(db.rows) != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0 || db.used != 0 || db.artifacts != 0 {
		t.Errorf(
			"rows=%d blobs=%d events=%d used=%d artifacts=%d; want nothing persisted",
			len(db.rows),
			len(bs.Blobs),
			len(pub.Events()),
			db.used,
			db.artifacts,
		)
	}
}

func TestIdenticalRepublishDoesNotConsumeQuota(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	for range 2 {
		if w := send(t, h, http.MethodPut, artifactURL, "data"); w.Code != http.StatusCreated {
			t.Fatalf("upload status = %d", w.Code)
		}
	}

	if db.used != 4 || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d after identical re-publish, want 4/1", db.used, db.artifacts)
	}
}

func TestOverwriteReleasesPreviousSize(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	send(t, h, http.MethodPut, artifactURL, "first")

	if w := send(t, h, http.MethodPut, artifactURL, "second-longer"); w.Code != http.StatusCreated {
		t.Fatalf("overwrite status = %d", w.Code)
	}

	if want := int64(len("second-longer")); db.used != want || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d after overwrite, want %d/1", db.used, db.artifacts, want)
	}
}

func TestOverwriteOverQuotaKeepsPreviousUsage(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	db.limit = 6

	send(t, h, http.MethodPut, artifactURL, "first")

	if w := send(t, h, http.MethodPut, artifactURL, "second-longer"); w.Code != http.StatusInsufficientStorage {
		t.Fatalf("overwrite status = %d, want 507", w.Code)
	}

	if db.used != 5 || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d after refused overwrite, want 5/1", db.used, db.artifacts)
	}
}

func TestUploadCreateFailureReleasesQuota(t *testing.T) {
	db := &sbtDB{DB: formattest.NewDB()}
	h := NewHTTPHandler(formattest.NewLogger(t), &failingCreateDB{sbtDB: db}, formattest.NewBlobStore(), "http://localhost")

	if w := send(t, h, http.MethodPut, artifactURL, "data"); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}

	if db.used != 0 || db.artifacts != 0 {
		t.Errorf("quota not released: used=%d artifacts=%d", db.used, db.artifacts)
	}
}
