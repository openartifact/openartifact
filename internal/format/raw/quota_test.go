// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package raw

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/quota"
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

const quotaTarget = "/repos/myrepo/dir/file.txt"

func put(h *HTTPHandler, body string) int {
	return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, quotaTarget, strings.NewReader(body))).Code
}

func TestPutReservesQuotaAndDeleteReleasesIt(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	if code := put(h, "hello"); code != http.StatusCreated {
		t.Fatalf("put status = %d", code)
	}

	if db.used != 5 || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d, want 5/1", db.used, db.artifacts)
	}

	if code := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodDelete, quotaTarget, nil)).Code; code != http.StatusNoContent {
		t.Fatalf("delete status = %d", code)
	}

	if db.used != 0 || db.artifacts != 0 {
		t.Errorf("quota after delete used=%d artifacts=%d, want 0/0", db.used, db.artifacts)
	}
}

func TestPutOverQuotaReturns507AndPersistsNothing(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)
	db.limit = 3

	if code := put(h, "hello"); code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507", code)
	}

	if db.art != nil || len(bs.Blobs) != 0 || len(pub.Events()) != 0 || db.used != 0 || db.artifacts != 0 {
		t.Errorf("art=%v blobs=%d events=%d used=%d artifacts=%d; want nothing persisted", db.art, len(bs.Blobs), len(pub.Events()), db.used, db.artifacts)
	}
}

func TestIdenticalPutDoesNotConsumeQuota(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	for range 2 {
		if code := put(h, "hello"); code != http.StatusCreated {
			t.Fatalf("put status = %d", code)
		}
	}

	if db.used != 5 || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d after identical re-put, want 5/1", db.used, db.artifacts)
	}
}

func TestOverwriteReleasesPreviousSize(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	put(h, "hello")

	if code := put(h, "hello, world"); code != http.StatusCreated {
		t.Fatalf("overwrite status = %d", code)
	}

	if want := int64(len("hello, world")); db.used != want || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d after overwrite, want %d/1", db.used, db.artifacts, want)
	}
}

func TestOverwriteOverQuotaKeepsPreviousUsage(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	db.limit = 8

	put(h, "hello")

	if code := put(h, "hello, world"); code != http.StatusInsufficientStorage {
		t.Fatalf("overwrite status = %d, want 507", code)
	}

	if db.used != 5 || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d after refused overwrite, want 5/1", db.used, db.artifacts)
	}
}

func TestImmutableOverwriteDoesNotConsumeQuota(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	db.Repo.ImmutableReleases = true

	put(h, "hello")

	if code := put(h, "tampered"); code != http.StatusConflict {
		t.Fatalf("overwrite status = %d, want 409", code)
	}

	if db.used != 5 || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d after refused overwrite, want 5/1", db.used, db.artifacts)
	}
}

// TestCommitQuota covers the shared write path of the streaming PUT and the
// presigned completion.
func TestCommitQuota(t *testing.T) {
	newArtifact := func() *models.RawArtifact {
		return &models.RawArtifact{
			ID: "id-1", RepositoryID: "repo-1", Path: "dir/file.txt", SHA256: "aa", SizeBytes: 5, Metadata: map[string]any{},
		}
	}
	commit := func(h *HTTPHandler, putBlob func(context.Context) error) error {
		_, err := CommitPromoted(formattest.Request(http.MethodPut, quotaTarget, nil), h.Handler, "myrepo", newArtifact(), putBlob)
		return err
	}

	t.Run("exceeded surfaces ErrExceeded without running putBlob", func(t *testing.T) {
		h, db, _, _ := newTestHandler(t)
		db.limit = 1
		ran := false

		err := commit(h, func(context.Context) error {
			ran = true
			return nil
		})
		if !errors.Is(err, quota.ErrExceeded) {
			t.Fatalf("err = %v, want quota.ErrExceeded", err)
		}

		if ran || db.used != 0 || db.artifacts != 0 {
			t.Errorf("putBlob ran=%v used=%d artifacts=%d; want nothing", ran, db.used, db.artifacts)
		}
	})

	t.Run("putBlob failure releases the reservation", func(t *testing.T) {
		h, db, _, _ := newTestHandler(t)
		boom := errors.New("promote failed")

		if err := commit(h, func(context.Context) error { return boom }); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}

		if db.used != 0 || db.artifacts != 0 || db.art != nil {
			t.Errorf("used=%d artifacts=%d art=%v; want everything released", db.used, db.artifacts, db.art)
		}
	})

	t.Run("success keeps the reservation", func(t *testing.T) {
		h, db, _, _ := newTestHandler(t)

		if err := commit(h, func(context.Context) error { return nil }); err != nil {
			t.Fatalf("commit: %v", err)
		}

		if db.used != 5 || db.artifacts != 1 {
			t.Errorf("used=%d artifacts=%d, want 5/1", db.used, db.artifacts)
		}
	})
}
