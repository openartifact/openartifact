// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package quota reserves and releases repository storage quota on the upload
// and delete paths of every format handler.
package quota

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/openartifact/openartifact/internal/database"
)

// ErrExceeded is returned by Enforce when storing the artifact would exceed the
// repository's size or artifact-count limit.
var ErrExceeded = errors.New(Message)

// Message is the text every format answers with when the quota is exceeded. It
// tells the client that trashed items still count, so the way out is purging
// the repository trash.
const Message = "repository storage quota exceeded (bytes of trashed items count until purged; see the repository trash)"

// Enforce atomically reserves size bytes and one artifact slot in the
// repository's storage quota. It returns ErrExceeded when the reservation does
// not fit. Callers MUST pair a successful reservation with Release if the
// artifact is not persisted afterwards, and call Release when the artifact is
// deleted.
func Enforce(ctx context.Context, db database.Database, repoID string, size int64) error {
	return EnforceN(ctx, db, repoID, size, 1)
}

// EnforceN is Enforce for an upload that stands for count artifacts (0 for the
// bytes-only parts of one: a Go module's go.mod, a Docker layer blob). A
// reservation of zero bytes and zero artifacts always fits. Pair it with
// ReleaseN using the same size and count.
func EnforceN(ctx context.Context, db database.Database, repoID string, size int64, count int) error {
	if size < 0 {
		size = 0
	}

	if count < 0 {
		count = 0
	}

	ok, err := db.ReserveStorageUsage(ctx, repoID, size, count)
	if err != nil {
		return fmt.Errorf("enforce quota: %w", err)
	}

	if !ok {
		return ErrExceeded
	}

	return nil
}

// ChargeN adds the usage of count artifacts totalling size bytes whose rows
// already exist, past any limit and without a reservation to match later: for
// a row that was created although no reservation was made for it.
func ChargeN(ctx context.Context, db database.Database, repoID string, size int64, count int) error {
	if err := db.ChargeStorageUsage(ctx, repoID, max(size, 0), max(count, 0)); err != nil {
		return fmt.Errorf("charge quota: %w", err)
	}

	return nil
}

// Release returns a previous reservation (size bytes and one artifact). It uses
// a context detached from request cancellation so a client that hung up still
// gets its reservation back.
func Release(ctx context.Context, db database.Database, repoID string, size int64) error {
	return ReleaseN(ctx, db, repoID, size, 1)
}

// ReleaseN returns the usage of count artifacts totalling size bytes, e.g. a
// batch of rows removed by a cleanup policy. The unit of usage is one package
// row (its blob's size, counted once per row even when blobs are shared), the
// same one Enforce reserves; the counters never go below zero. Like Release it
// is detached from ctx cancellation.
func ReleaseN(ctx context.Context, db database.Database, repoID string, size int64, count int) error {
	if size < 0 {
		size = 0
	}

	if count < 0 {
		count = 0
	}

	if size == 0 && count == 0 {
		return nil
	}

	if err := db.DecrementStorageUsage(context.WithoutCancel(ctx), repoID, size, count); err != nil {
		return fmt.Errorf("release quota: %w", err)
	}

	return nil
}

// WriteExceeded writes the standard 507 response for ErrExceeded.
func WriteExceeded(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInsufficientStorage)
	_, _ = w.Write([]byte(`{"error":"` + Message + `","code":"quota_exceeded"}` + "\n"))
}

// Room returns how many more bytes the repository's size quota can take and
// whether it is limited at all. It reserves nothing; see Precheck. Callers that
// stream bytes of unknown length (a Docker layer) cap the stream at room.
func Room(ctx context.Context, db database.Database, repoID string) (int64, bool, error) {
	q, err := db.GetOrCreateStorageQuota(ctx, repoID)
	if err != nil {
		return 0, false, fmt.Errorf("quota room: %w", err)
	}

	if q == nil || q.MaxSizeBytes <= 0 {
		return 0, false, nil
	}

	return max(q.MaxSizeBytes-q.CurrentSizeBytes, 0), true, nil
}

// Precheck reports ErrExceeded when size more bytes and one more artifact
// cannot fit the repository's quota, without reserving anything. Handlers call
// it with the declared Content-Length before spooling a body to disk so a
// writer on a full repository cannot stage bytes the reservation would refuse
// afterwards. It is advisory: a size <= 0 (unknown length) or a quota that
// cannot be read passes, because the reservation (Enforce) stays authoritative
// and reports such failures itself. It returns only nil or ErrExceeded.
func Precheck(ctx context.Context, db database.Database, repoID string, size int64) error {
	if size <= 0 {
		return nil
	}

	q, err := db.GetOrCreateStorageQuota(ctx, repoID)
	if err != nil || q == nil {
		return nil //nolint:nilerr // advisory; Enforce reports lookup failures
	}

	if (q.MaxSizeBytes > 0 && q.CurrentSizeBytes+size > q.MaxSizeBytes) ||
		(q.MaxArtifactCount > 0 && q.CurrentArtifactCount >= q.MaxArtifactCount) {
		return ErrExceeded
	}

	return nil
}
