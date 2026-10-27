// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

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

const quotaPayloadSize = int64(len("payload"))

func TestUploadReservesQuotaAndDeleteReleasesIt(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	if code := uploadAs(t, h, "demo", "1.0", "demo-1.0.tar.gz"); code != http.StatusOK {
		t.Fatalf("upload status = %d", code)
	}

	if db.used != quotaPayloadSize || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d, want %d/1", db.used, db.artifacts, quotaPayloadSize)
	}

	w := formattest.Do(register(h), formattest.Request(http.MethodDelete, "/repos/myrepo/files/demo-1.0.tar.gz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("delete status = %d", w.Code)
	}

	if db.used != 0 || db.artifacts != 0 {
		t.Errorf("quota after delete used=%d artifacts=%d, want 0/0", db.used, db.artifacts)
	}
}

func TestUploadOverQuotaReturns507AndPersistsNothing(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)
	db.limit = 3

	if code := uploadAs(t, h, "demo", "1.0", "demo-1.0.tar.gz"); code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507", code)
	}

	if len(db.pkgs) != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0 || db.used != 0 || db.artifacts != 0 {
		t.Errorf(
			"pkgs=%d blobs=%d events=%d used=%d artifacts=%d; want nothing persisted",
			len(db.pkgs),
			len(bs.Blobs),
			len(pub.Events()),
			db.used,
			db.artifacts,
		)
	}
}

func TestUploadCreateFailureReleasesQuota(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"duplicate race": {errors.New("duplicate key value violates unique constraint"), http.StatusConflict},
		"other failure":  {errors.New("boom"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			h, db, _, _ := newTestHandler(t)
			db.createErr = tc.err

			if code := uploadAs(t, h, "demo", "1.0", "demo-1.0.tar.gz"); code != tc.want {
				t.Fatalf("status = %d, want %d", code, tc.want)
			}

			if db.used != 0 || db.artifacts != 0 {
				t.Errorf("quota not released: used=%d artifacts=%d", db.used, db.artifacts)
			}
		})
	}
}

func TestOverQuotaBodyCarriesCode(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	db.limit = 1

	w := formattest.Do(register(h), uploadRequest(t, "demo_pkg-1.0.tar.gz", []byte("payload")))
	if w.Code != http.StatusInsufficientStorage || !strings.Contains(w.Body.String(), `"quota_exceeded"`) {
		t.Fatalf("status=%d body=%q, want 507 quota_exceeded", w.Code, w.Body.String())
	}
}
