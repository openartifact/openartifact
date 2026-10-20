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
var ErrExceeded = errors.New("repository storage quota exceeded")

// Enforce atomically reserves size bytes and one artifact slot in the
// repository's storage quota. It returns ErrExceeded when the reservation does
// not fit. Callers MUST pair a successful reservation with Release if the
// artifact is not persisted afterwards, and call Release when the artifact is
// deleted.
func Enforce(ctx context.Context, db database.Database, repoID string, size int64) error {
	if size < 0 {
		size = 0
	}

	ok, err := db.ReserveStorageUsage(ctx, repoID, size, 1)
	if err != nil {
		return fmt.Errorf("enforce quota: %w", err)
	}

	if !ok {
		return ErrExceeded
	}

	return nil
}

// Release returns a previous reservation (size bytes and one artifact). It uses
// a context detached from request cancellation so a client that hung up still
// gets its reservation back.
func Release(ctx context.Context, db database.Database, repoID string, size int64) error {
	if size < 0 {
		size = 0
	}

	if err := db.DecrementStorageUsage(context.WithoutCancel(ctx), repoID, size, 1); err != nil {
		return fmt.Errorf("release quota: %w", err)
	}

	return nil
}

// WriteExceeded writes the standard 507 response for ErrExceeded.
func WriteExceeded(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInsufficientStorage)
	_, _ = w.Write([]byte(`{"error":"repository storage quota exceeded","code":"quota_exceeded"}` + "\n"))
}
