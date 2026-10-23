// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database"
)

// SafeDeleteBlob deletes the blob identified by sha256 only if no package-table
// row still references it (db.BlobReferenceCount == 0).
//
// With the Postgres database "deletes" means: the blobs row is removed at once
// and the bytes are queued for physical deletion by the blob deletion sweeper,
// which removes them after the configured grace period and once no running
// backup can still read them. blobStore is used only by databases without that
// queue (test doubles), which delete the bytes immediately.
//
// Callers MUST delete their own DB row referencing the blob BEFORE calling
// this function, otherwise BlobReferenceCount will still see that row and
// the blob will never be removed from storage.
func SafeDeleteBlob(ctx context.Context, db database.Database, blobStore BlobStore, sha256 string) error {
	if err := ValidateBlobID(sha256); err != nil {
		return err
	}

	// Preferred path: check + blobs-row delete + queueing the bytes for
	// deletion, in one transaction under an advisory lock shared with uploaders
	// (WithBlobLock). The bytes themselves are removed later by the blob
	// deletion sweeper, once the grace period has passed and no running backup
	// can still need them.
	if d, ok := db.(blobDeleter); ok {
		_, err := d.DeleteBlobIfUnreferenced(ctx, sha256)

		return err
	}

	// Databases without the queue (test doubles) delete the bytes right away.
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
	DeleteBlobIfUnreferenced(ctx context.Context, sha256 string) (bool, error)
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
	ListUnreferencedBlobs(ctx context.Context, minAge time.Duration, after string, scan, limit int) (shas []string, next string, err error)
}

// OrphanMinAge is how old an unreferenced blobs row must be before it is
// collected, so an in-flight upload (which creates the blobs row before the
// row that references it) is never mistaken for garbage.
const OrphanMinAge = time.Hour

// OrphanScanWindow bounds how many blobs rows one CollectOrphanBlobs call
// examines, so the cost of a pass does not grow with the table.
const OrphanScanWindow = 20000

// orphanCursor is where the next CollectOrphanBlobs call resumes (a sha256, ""
// = from the start). It is shared by every caller in the process, so each pass
// -- scheduled, after a trash purge or after a repository delete -- continues
// the sweep instead of rescanning the oldest rows, and a blob that cannot be
// removed is simply scanned past.
var orphanCursor struct { //nolint:gochecknoglobals // process-wide sweep position shared by every orphan collection caller
	sync.Mutex

	after string
}

// OrphanBatch bounds how many orphan blobs one CollectOrphanBlobs call removes.
const OrphanBatch = 500

// CollectOrphanBlobs deletes blobs (storage object + blobs row) that no
// package row references any more — e.g. after a repository delete cascaded
// its package rows away. Only blobs older than minAge are considered. It
// returns the number removed; databases without ListUnreferencedBlobs
// support are a no-op. One call examines at most OrphanScanWindow blobs rows
// starting at a process-wide cursor and advances it (wrapping to the start at
// the end of the table), so a full sweep takes several calls.
//
// A blob that cannot be removed (storage error, invalid id) does not stop the
// pass: the remaining blobs are still processed and every failure is returned
// joined, so one poisoned blob cannot starve the rest of the batch forever.
func CollectOrphanBlobs(ctx context.Context, db database.Database, blobStore BlobStore, minAge time.Duration, batch int) (int, error) {
	removed, _, err := collectOrphanWindow(ctx, db, blobStore, minAge, batch)

	return removed, err
}

// CollectOrphanBlobsSweep runs up to maxWindows consecutive CollectOrphanBlobs
// windows and stops early once the cursor wrapped past the end of the blobs
// table, i.e. every blob was examined once. The scheduled sweep uses it so
// that a large table is covered within one tick, in bounded steps.
func CollectOrphanBlobsSweep(ctx context.Context, db database.Database, blobStore BlobStore, minAge time.Duration, batch, maxWindows int) (int, error) {
	total := 0

	var errs []error

	for range maxWindows {
		removed, next, err := collectOrphanWindow(ctx, db, blobStore, minAge, batch)
		total += removed

		if err != nil {
			errs = append(errs, err)
		}

		if next == "" || ctx.Err() != nil {
			break
		}
	}

	return total, errors.Join(errs...)
}

// collectOrphanWindow does one window and reports the cursor it left behind.
func collectOrphanWindow(ctx context.Context, db database.Database, blobStore BlobStore, minAge time.Duration, batch int) (int, string, error) {
	l, ok := db.(unreferencedLister)
	if !ok {
		return 0, "", nil
	}

	orphanCursor.Lock()
	after := orphanCursor.after
	orphanCursor.Unlock()

	shas, next, err := l.ListUnreferencedBlobs(ctx, minAge, after, OrphanScanWindow, batch)
	if err != nil {
		return 0, "", err
	}

	orphanCursor.Lock()
	orphanCursor.after = next
	orphanCursor.Unlock()

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

	return removed, next, errors.Join(errs...)
}

// ReclaimMetadataBlob deletes a superseded generated-metadata blob (maven
// maven-metadata.xml, ...) when nothing references it any more. Such blobs have
// no blobs row, so neither SafeDeleteBlob's package-table count nor the orphan
// collector sees them: the repository_metadata rows (which identical content in
// another repository may share) and the package tables are checked here, under
// the per-blob lock a generator holds while it stores the blob and commits its
// row, so a concurrent regeneration of the same content is never undercut.
func ReclaimMetadataBlob(ctx context.Context, db database.Database, blobStore BlobStore, sha string) error {
	if err := ValidateBlobID(sha); err != nil {
		return err
	}

	sqlDB := db.GetDB()
	if sqlDB == nil || blobStore == nil {
		return nil
	}

	return WithBlobLock(ctx, db, sha, func(ctx context.Context) error {
		var refs int
		if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM repository_metadata WHERE blob_sha256 = $1`, sha).Scan(&refs); err != nil {
			return fmt.Errorf("count metadata references of %s: %w", sha, err)
		}

		if refs > 0 {
			return nil
		}

		pkgRefs, err := db.BlobReferenceCount(ctx, sha)
		if err != nil {
			return fmt.Errorf("count package references of %s: %w", sha, err)
		}

		if pkgRefs > 0 {
			return nil
		}

		return DiscardUnreferencedBytes(ctx, db, blobStore, sha)
	})
}

type blobDeletionQueue interface {
	EnqueueBlobDeletion(ctx context.Context, sha256, scope string) error
}

// DiscardUnreferencedBytes frees the bytes of a blob the caller has just found
// unreferenced under the blob's lock, without a blobs row of its own to delete
// (generated metadata). The bytes are queued like any other deleted blob, not
// removed at once: a running backup may still list a blobs row of the same
// content (an upload of byte-identical content that was deleted since) and must
// find its bytes. The sweeper re-checks references before it removes anything.
// Databases without the queue (test doubles) delete right away.
func DiscardUnreferencedBytes(ctx context.Context, db database.Database, blobStore BlobStore, sha string) error {
	if q, ok := db.(blobDeletionQueue); ok {
		if err := q.EnqueueBlobDeletion(ctx, sha, database.BlobDeletionScopeAll); err != nil {
			return fmt.Errorf("queue metadata blob %s for deletion: %w", sha, err)
		}

		return nil
	}

	if err := blobStore.Delete(ctx, sha); err != nil {
		return fmt.Errorf("delete metadata blob %s: %w", sha, err)
	}

	return nil
}
