// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/openartifact/openartifact/internal/database"
)

// SafeDeleteBlob deletes the blob identified by sha256 from blobStore only if
// no package-table row still references it (db.BlobReferenceCount == 0).
//
// Callers MUST delete their own DB row referencing the blob BEFORE calling
// this function, otherwise BlobReferenceCount will still see that row and
// the blob will never be removed from storage.
func SafeDeleteBlob(ctx context.Context, db database.Database, blobStore BlobStore, sha256 string) error {
	if err := ValidateBlobID(sha256); err != nil {
		return err
	}

	// Preferred path: check + blobs-row delete + storage delete under an
	// advisory lock shared with uploaders (WithBlobLock).
	if d, ok := db.(blobDeleter); ok {
		_, err := d.DeleteBlobIfUnreferenced(ctx, sha256, func(ctx context.Context) error {
			if err := blobStore.Delete(ctx, sha256); err != nil {
				return fmt.Errorf("failed to delete blob from storage: %w", err)
			}

			return nil
		})

		return err
	}

	count, err := db.BlobReferenceCount(ctx, sha256)
	if err != nil {
		return fmt.Errorf("failed to check blob reference count: %w", err)
	}

	if count > 0 {
		return nil
	}

	if err := blobStore.Delete(ctx, sha256); err != nil {
		return fmt.Errorf("failed to delete blob from storage: %w", err)
	}

	return nil
}

type blobDeleter interface {
	DeleteBlobIfUnreferenced(ctx context.Context, sha256 string, deleteStorage func(ctx context.Context) error) (bool, error)
}

type blobLocker interface {
	WithBlobLock(ctx context.Context, sha256 string, fn func(ctx context.Context) error) error
}

// WithBlobLock runs fn while holding the per-blob lock that SafeDeleteBlob
// takes, so an uploader can store a blob and insert its referencing row
// without a concurrent SafeDeleteBlob removing the blob in between. When the
// database does not support locking, fn runs unguarded.
func WithBlobLock(ctx context.Context, db database.Database, sha256 string, fn func(ctx context.Context) error) error {
	if l, ok := db.(blobLocker); ok {
		return l.WithBlobLock(ctx, sha256, fn)
	}

	return fn(ctx)
}

type blobMultiLocker interface {
	WithBlobLocks(ctx context.Context, shas []string, fn func(ctx context.Context) error) error
}

// WithBlobLocks is WithBlobLock for several blobs held at once (a row that
// references two blobs, e.g. a Go module's zip and mod). The locks are taken
// in a deterministic order on a single connection. Databases that only support
// single-blob locks get nested WithBlobLock calls in the same order; databases
// without locking support run fn unguarded.
func WithBlobLocks(ctx context.Context, db database.Database, shas []string, fn func(ctx context.Context) error) error {
	if l, ok := db.(blobMultiLocker); ok {
		return l.WithBlobLocks(ctx, shas, fn)
	}

	keys := slices.Compact(slices.Sorted(slices.Values(shas)))

	var lock func(ctx context.Context, i int) error

	lock = func(ctx context.Context, i int) error {
		if i == len(keys) {
			return fn(ctx)
		}

		return WithBlobLock(ctx, db, keys[i], func(ctx context.Context) error { return lock(ctx, i+1) })
	}

	return lock(ctx, 0)
}

type unreferencedLister interface {
	ListUnreferencedBlobs(ctx context.Context, minAge time.Duration, limit int) ([]string, error)
}

// OrphanMinAge is how old an unreferenced blobs row must be before it is
// collected, so an in-flight upload (which creates the blobs row before the
// row that references it) is never mistaken for garbage.
const OrphanMinAge = time.Hour

// OrphanBatch bounds how many orphan blobs one CollectOrphanBlobs call removes.
const OrphanBatch = 500

// CollectOrphanBlobs deletes blobs (storage object + blobs row) that no
// package row references any more — e.g. after a repository delete cascaded
// its package rows away. Only blobs older than minAge are considered. It
// returns the number removed; databases without ListUnreferencedBlobs
// support are a no-op.
//
// A blob that cannot be removed (storage error, invalid id) does not stop the
// pass: the remaining blobs are still processed and every failure is returned
// joined, so one poisoned blob cannot starve the rest of the batch forever.
func CollectOrphanBlobs(ctx context.Context, db database.Database, blobStore BlobStore, minAge time.Duration, batch int) (int, error) {
	l, ok := db.(unreferencedLister)
	if !ok {
		return 0, nil
	}

	shas, err := l.ListUnreferencedBlobs(ctx, minAge, batch)
	if err != nil {
		return 0, err
	}

	var (
		removed int
		errs    []error
	)

	for _, sha := range shas {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)

			break
		}

		if err := SafeDeleteBlob(ctx, db, blobStore, sha); err != nil {
			errs = append(errs, fmt.Errorf("collect orphan blob %s: %w", sha, err))

			continue
		}

		removed++
	}

	return removed, errors.Join(errs...)
}
