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

	"database/sql"

	"github.com/openartifact/openartifact/internal/database"
)

// SafeDeleteBlobStore is the part of the database SafeDeleteBlob needs.
type SafeDeleteBlobStore interface {
	BlobReferenceCount(ctx context.Context, sha256 string) (int, error)
}

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
//
// SafeDeleteBlob takes one of the database's bounded blob-lock slots, so it
// MUST NOT be called from inside a WithBlobLock(s) callback or a
// FlipBlobPlacement verify (see database.WithBlobLocks); do it after the
// callback returns.
func SafeDeleteBlob(ctx context.Context, db SafeDeleteBlobStore, blobStore BlobStore, sha256 string) error {
	_, err := safeDeleteBlob(ctx, db, blobStore, sha256)

	return err
}

// safeDeleteBlob is SafeDeleteBlob that also reports whether the blob was
// handed to deletion (true) or kept because a row still references it (false).
// Only the orphan collector needs the answer, to count what it really removed;
// every other caller releases a blob it has no use for the outcome of.
func safeDeleteBlob(ctx context.Context, db SafeDeleteBlobStore, blobStore BlobStore, sha256 string) (bool, error) {
	if err := ValidateBlobID(sha256); err != nil {
		return false, err
	}

	// Preferred path: check + blobs-row delete + queueing the bytes for
	// deletion, in one transaction under an advisory lock shared with uploaders
	// (WithBlobLock). The bytes themselves are removed later by the blob
	// deletion sweeper, once the grace period has passed and no running backup
	// can still need them.
	if d, ok := db.(blobDeleter); ok {
		return d.DeleteBlobIfUnreferenced(ctx, sha256)
	}

	// Databases without the queue (test doubles) delete the bytes right away.
	count, err := db.BlobReferenceCount(ctx, sha256)
	if err != nil {
		return false, fmt.Errorf("failed to check blob reference count: %w", err)
	}

	if count > 0 {
		return false, nil
	}

	if err := blobStore.Delete(ctx, sha256); err != nil {
		return false, fmt.Errorf("failed to delete blob from storage: %w", err)
	}

	return true, nil
}

// orphanDeleteChunk is how many orphan blobs one batched delete handles. It is
// the database's batch size, so one call is one transaction: it removes all of
// its blobs or none, and a failed call can be retried blob by blob without
// counting a blob twice.
const orphanDeleteChunk = database.BlobBatchLockChunk

type blobBatchDeleter interface {
	DeleteBlobsIfUnreferenced(ctx context.Context, shas []string) (int, error)
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
//
// fn MUST NOT take another blob lock or call SafeDeleteBlob: each holds one of
// the database's bounded lock slots, and a nested acquisition can deadlock once
// concurrent holders fill them (see database.WithBlobLocks). With the Postgres
// database an error wrapping database.ErrBlobLockLost means fn returned nil but
// its lock was lost while it ran; treat it as a failed operation.
func WithBlobLock(ctx context.Context, db any, sha256 string, fn func(ctx context.Context) error) error {
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
func WithBlobLocks(ctx context.Context, db any, shas []string, fn func(ctx context.Context) error) error {
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

// OrphanCursor is one caller's position in its sweep of the blobs table: where
// its next CollectOrphanBlobs call resumes (the zero value starts at the
// beginning). Every caller that sweeps (the scheduled cleanup, the trash purge,
// a repository delete) owns its own, so one caller's passes neither move
// another's position nor leave it a stretch of the table behind: each cursor
// continues its sweep window by window, a blob that cannot be removed is
// scanned past, and a window a pass could not finish (shutdown) is redone, not
// skipped. A cursor is safe for concurrent use; passes sharing one run one at a
// time, the later one continuing where the earlier stopped.
type OrphanCursor struct {
	mu    sync.Mutex
	after string
}

// OrphanBatch bounds how many orphan blobs one CollectOrphanBlobs call removes.
const OrphanBatch = 500

// CollectOrphanBlobsStore is the part of the database CollectOrphanBlobs needs.
type CollectOrphanBlobsStore interface {
	collectOrphanWindowStore
}

// CollectOrphanBlobs deletes blobs (storage object + blobs row) that no
// package row references any more — e.g. after a repository delete cascaded
// its package rows away. Only blobs older than minAge are considered. It
// returns the number of blobs it handed to deletion (one a row still
// references again by now is left alone and not counted); databases without
// ListUnreferencedBlobs support are a no-op. One call examines at most
// OrphanScanWindow blobs rows starting at cur and advances it (wrapping to the
// start at the end of the table), so a full sweep takes several calls.
//
// A blob that cannot be removed (storage error, invalid id) does not stop the
// pass: the remaining blobs are still processed and every failure is returned
// joined, so one poisoned blob cannot starve the rest of the batch forever.
func CollectOrphanBlobs(
	ctx context.Context,
	db CollectOrphanBlobsStore,
	blobStore BlobStore,
	cur *OrphanCursor,
	minAge time.Duration,
	batch int,
) (int, error) {
	removed, _, err := collectOrphanWindow(ctx, db, blobStore, cur, minAge, batch)

	return removed, err
}

// CollectOrphanBlobsSweepStore is the part of the database CollectOrphanBlobsSweep needs.
type CollectOrphanBlobsSweepStore interface {
	collectOrphanWindowStore
}

// CollectOrphanBlobsSweep runs up to maxWindows consecutive CollectOrphanBlobs
// windows and stops early once cur wrapped past the end of the blobs table,
// i.e. every blob was examined once. The scheduled sweep uses it so that a
// large table is covered within one tick, in bounded steps.
func CollectOrphanBlobsSweep(
	ctx context.Context,
	db CollectOrphanBlobsSweepStore,
	blobStore BlobStore,
	cur *OrphanCursor,
	minAge time.Duration,
	batch, maxWindows int,
) (int, error) {
	total := 0

	var errs []error

	for range maxWindows {
		removed, next, err := collectOrphanWindow(ctx, db, blobStore, cur, minAge, batch)
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

// collectOrphanWindowStore is the part of the database collectOrphanWindow needs.
type collectOrphanWindowStore interface {
	SafeDeleteBlobStore
}

// collectOrphanWindow does one window from cur and reports the cursor it left
// behind. cur stays locked for the whole window, so concurrent passes on the
// same cursor take consecutive windows instead of scanning one twice, and it
// only advances once the window was worked through: a pass cut short by ctx
// leaves it where it was.
func collectOrphanWindow(
	ctx context.Context,
	db collectOrphanWindowStore,
	blobStore BlobStore,
	cur *OrphanCursor,
	minAge time.Duration,
	batch int,
) (int, string, error) {
	l, ok := db.(unreferencedLister)
	if !ok {
		return 0, "", nil
	}

	cur.mu.Lock()
	defer cur.mu.Unlock()

	shas, next, err := l.ListUnreferencedBlobs(ctx, minAge, cur.after, OrphanScanWindow, batch)
	if err != nil {
		return 0, "", err
	}

	removed, errs := deleteOrphans(ctx, db, blobStore, shas)

	if ctx.Err() == nil {
		cur.after = next
	}

	return removed, next, errors.Join(errs...)
}

// deleteOrphans hands the listed orphans to deletion and returns how many were
// really removed, with one error per blob that could not be.
func deleteOrphans(ctx context.Context, db collectOrphanWindowStore, blobStore BlobStore, shas []string) (int, []error) {
	var (
		removed int
		errs    []error
	)

	// Preferred path: one transaction and one anti-join per chunk (the lock
	// and recheck protocol of DeleteBlobIfUnreferenced, for many blobs). A
	// chunk that fails is retried blob by blob below, so one poisoned blob
	// still cannot hold back the rest.
	if bd, ok := db.(blobBatchDeleter); ok {
		var rest []string

		valid := make([]string, 0, len(shas))

		for _, sha := range shas {
			if err := ValidateBlobID(sha); err != nil {
				rest = append(rest, sha) // reported per blob below

				continue
			}

			valid = append(valid, sha)
		}

		for chunk := range slices.Chunk(valid, orphanDeleteChunk) {
			if ctx.Err() != nil {
				break
			}

			n, err := bd.DeleteBlobsIfUnreferenced(ctx, chunk)
			removed += n // what committed before a failure still counts

			if err != nil {
				rest = append(rest, chunk...)
			}
		}

		shas = rest
	}

	for _, sha := range shas {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)

			break
		}

		deleted, err := safeDeleteBlob(ctx, db, blobStore, sha)
		if err != nil {
			errs = append(errs, fmt.Errorf("collect orphan blob %s: %w", sha, err))

			continue
		}

		if deleted {
			removed++
		}
	}

	return removed, errs
}

// ReclaimMetadataBlobStore is the part of the database ReclaimMetadataBlob needs.
type ReclaimMetadataBlobStore interface {
	BlobReferenceCount(ctx context.Context, sha256 string) (int, error)
	GetDB() *sql.DB
}

// ReclaimMetadataBlob deletes a superseded generated-metadata blob (maven
// maven-metadata.xml, ...) when nothing references it any more. Such blobs have
// no blobs row, so neither SafeDeleteBlob's package-table count nor the orphan
// collector sees them: the repository_metadata rows (which identical content in
// another repository may share) and the package tables are checked here, under
// the per-blob lock a generator holds while it stores the blob and commits its
// row, so a concurrent regeneration of the same content is never undercut.
func ReclaimMetadataBlob(ctx context.Context, db ReclaimMetadataBlobStore, blobStore BlobStore, sha string) error {
	if err := ValidateBlobID(sha); err != nil {
		return err
	}

	sqlDB := db.GetDB()
	if sqlDB == nil || blobStore == nil {
		return nil
	}

	return WithBlobLock(ctx, db, sha, func(ctx context.Context) error {
		var refs int
		if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM repository_metadata WHERE blob_sha256 = $1`, sha).
			Scan(&refs); err != nil {
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
func DiscardUnreferencedBytes(ctx context.Context, db any, blobStore BlobStore, sha string) error {
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
