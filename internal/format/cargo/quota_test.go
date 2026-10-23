// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
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

const quotaMeta = `{"name":"demo","vers":"0.1.0"}`

// quotaCrateSize is the size of publishCrate's payload.
func quotaCrateSize(t *testing.T) int64 {
	t.Helper()

	return int64(len(plainCrate(t)))
}

func TestPublishReservesQuotaAndYankKeepsIt(t *testing.T) {
	h, db, bs, pub := newCargoHandler(t)

	if w := publishCrate(t, db, bs, pub, quotaMeta); w.Code != http.StatusOK {
		t.Fatalf("publish status = %d: %s", w.Code, w.Body.String())
	}

	if db.used != quotaCrateSize(t) || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d, want %d/1", db.used, db.artifacts, quotaCrateSize(t))
	}

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodDelete, "/repos/myrepo/api/v1/crates/demo/0.1.0/yank", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("yank status = %d", w.Code)
	}

	if db.used != quotaCrateSize(t) || db.artifacts != 1 {
		t.Errorf("yank changed quota: used=%d artifacts=%d", db.used, db.artifacts)
	}
}

func TestPublishOverQuotaReturns507AndPersistsNothing(t *testing.T) {
	_, db, bs, pub := newCargoHandler(t)
	db.limit = 3

	w := publishCrate(t, db, bs, pub, quotaMeta)
	if w.Code != http.StatusInsufficientStorage || !strings.Contains(w.Body.String(), `"quota_exceeded"`) {
		t.Fatalf("status=%d body=%q, want 507 quota_exceeded", w.Code, w.Body.String())
	}

	if len(db.crates) != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0 || db.used != 0 || db.artifacts != 0 {
		t.Errorf("crates=%d blobs=%d events=%d used=%d artifacts=%d; want nothing persisted", len(db.crates), len(bs.Blobs), len(pub.Events()), db.used, db.artifacts)
	}
}

func TestPublishCreateFailureReleasesQuota(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"duplicate race": {&pgconn.PgError{Code: "23505"}, http.StatusConflict},
		"other failure":  {errors.New("boom"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			_, db, bs, pub := newCargoHandler(t)
			db.createErr = tc.err

			if w := publishCrate(t, db, bs, pub, quotaMeta); w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}

			if db.used != 0 || db.artifacts != 0 {
				t.Errorf("quota not released: used=%d artifacts=%d", db.used, db.artifacts)
			}
		})
	}
}

func TestPublishDuplicateDoesNotConsumeQuota(t *testing.T) {
	_, db, bs, pub := newCargoHandler(t)

	if w := publishCrate(t, db, bs, pub, quotaMeta); w.Code != http.StatusOK {
		t.Fatalf("first publish status = %d", w.Code)
	}

	if w := publishCrate(t, db, bs, pub, quotaMeta); w.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want 409", w.Code)
	}

	if db.used != quotaCrateSize(t) || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d after duplicate, want %d/1", db.used, db.artifacts, quotaCrateSize(t))
	}
}
