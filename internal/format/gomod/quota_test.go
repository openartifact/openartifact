// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gomod

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
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

func TestUploadReservesZipPlusModAsOneArtifact(t *testing.T) {
	router, db, _, _ := newTestRouter(t)
	zipData := moduleZip(t, testModule, "v1.0.0")
	goMod := "module " + testModule + "\n\ngo 1.21\n"

	if w := serve(router, http.MethodPut, "/repos/myrepo/"+testModule+"/@v/v1.0.0", zipData); w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
	}

	if want := int64(len(zipData) + len(goMod)); db.used != want || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d, want %d/1", db.used, db.artifacts, want)
	}
}

func TestUploadOverQuotaReturns507AndPersistsNothing(t *testing.T) {
	router, db, bs, pub := newTestRouter(t)
	db.limit = 10

	w := serve(router, http.MethodPut, "/repos/myrepo/"+testModule+"/@v/v1.0.0", moduleZip(t, testModule, "v1.0.0"))
	if w.Code != http.StatusInsufficientStorage || !strings.Contains(w.Body.String(), `"quota_exceeded"`) {
		t.Fatalf("status=%d body=%q, want 507 quota_exceeded", w.Code, w.Body.String())
	}

	if len(db.mods) != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0 || db.used != 0 || db.artifacts != 0 {
		t.Errorf(
			"mods=%d blobs=%d events=%d used=%d artifacts=%d; want nothing persisted",
			len(db.mods),
			len(bs.Blobs),
			len(pub.Events()),
			db.used,
			db.artifacts,
		)
	}
}

func TestUploadCreateFailureReleasesQuota(t *testing.T) {
	router, db, _, pub := newTestRouter(t)
	db.createErr = errors.New("boom")

	w := serve(router, http.MethodPut, "/repos/myrepo/"+testModule+"/@v/v1.0.0", moduleZip(t, testModule, "v1.0.0"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}

	if db.used != 0 || db.artifacts != 0 || len(pub.Events()) != 0 {
		t.Errorf("quota not released: used=%d artifacts=%d events=%d", db.used, db.artifacts, len(pub.Events()))
	}
}
