// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
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

// failingCreateDB makes the row insert fail with err.
type failingCreateDB struct {
	*condaDB

	err error
}

func (d *failingCreateDB) CreateCondaPackage(context.Context, *models.CondaPackage) error {
	return d.err
}

const (
	quotaFilename = "demo-1.0-py_0.tar.bz2"
	quotaUploadTo = "/repos/myrepo/linux-64?filename=" + quotaFilename
	quotaDeleteOf = "/repos/myrepo/linux-64/" + quotaFilename
)

func uploadPayload(h *HTTPHandler, body string) int {
	return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPost, quotaUploadTo, strings.NewReader(body))).Code
}

func TestUploadReservesQuotaAndDeleteReleasesIt(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	if code := uploadPayload(h, "data"); code != http.StatusCreated {
		t.Fatalf("upload status = %d", code)
	}

	if db.used != 4 || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d, want 4/1", db.used, db.artifacts)
	}

	if code := formattest.Do(
		h.RegisterRepoRoutes,
		formattest.Request(http.MethodDelete, quotaDeleteOf, nil),
	).Code; code != http.StatusNoContent {
		t.Fatalf("delete status = %d", code)
	}

	if db.used != 0 || db.artifacts != 0 {
		t.Errorf("quota after delete used=%d artifacts=%d, want 0/0", db.used, db.artifacts)
	}
}

func TestUploadOverQuotaReturns507AndPersistsNothing(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)
	db.limit = 3

	if code := uploadPayload(h, "data"); code != http.StatusInsufficientStorage {
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

func TestUploadDuplicateDoesNotConsumeQuota(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	if code := uploadPayload(h, "data"); code != http.StatusCreated {
		t.Fatalf("first upload status = %d", code)
	}

	if code := uploadPayload(h, "data"); code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want 409", code)
	}

	if db.used != 4 || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d after duplicate, want 4/1", db.used, db.artifacts)
	}
}

func TestUploadCreateFailureReleasesQuotaAndBlob(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"generic failure": {errCreateFailed, http.StatusInternalServerError},
		"duplicate race":  {database.ErrDuplicate, http.StatusConflict},
	} {
		t.Run(name, func(t *testing.T) {
			db := &condaDB{DB: formattest.NewDB()}
			bs := formattest.NewBlobStore()
			h := NewHTTPHandler(formattest.NewLogger(t), &failingCreateDB{condaDB: db, err: tc.err}, bs, "http://localhost")

			if code := uploadPayload(h, "data"); code != tc.want {
				t.Fatalf("status = %d, want %d", code, tc.want)
			}

			if db.used != 0 || db.artifacts != 0 {
				t.Errorf("quota not released: used=%d artifacts=%d", db.used, db.artifacts)
			}

			if len(bs.Blobs) != 0 {
				t.Error("orphan blob left after failed insert")
			}
		})
	}
}
