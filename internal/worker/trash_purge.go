// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"database/sql"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// trashPurgeLockKey names the cluster-wide advisory lock (hashed by
	// pglock.Try) that the replica running a purge pass holds.
	trashPurgeLockKey = "openartifact:trash-purge"

	// trashPurgeBatch is how many expired items one listing asks for.
	trashPurgeBatch = 100
	// trashPurgeMaxItems caps the items one pass processes so a huge backlog
	// cannot keep a pass (and its pinned lock connection) busy forever; the
	// next pass continues where this one stopped.
	trashPurgeMaxItems = 1000
	// trashPurgeRunTimeout bounds one scheduled pass.
	trashPurgeRunTimeout = 30 * time.Minute
	// trashPurgeMaxOrphanPasses bounds the orphan sweep that closes a pass
	// (trashPurgeMaxOrphanPasses * storage.OrphanBatch blobs at most).
	trashPurgeMaxOrphanPasses = 100
)

// PurgeStats is the outcome of one purge pass.
type PurgeStats struct {
	// Purged counts the items (repositories included) removed from the database.
	Purged int
	// Repositories counts the repositories among Purged.
	Repositories int
	// Failed counts items whose purge failed (a repository still referenced by
	// a release bundle, a database error). They stay in the trash and are
	// retried by the next pass.
	Failed int
	// BlobErrors counts blobs of purged items that could not be freed. Their
	// rows are gone, so the orphan collector reclaims them later.
	BlobErrors int
	// OrphansRemoved counts the blobs removed by the orphan sweep that closes
	// a pass which purged something.
	OrphansRemoved int
	// Busy is set when another replica holds the purge lock; nothing was done.
	Busy bool
	// Capped is set when the pass stopped at the per-pass item cap; more
	// expired items may be waiting.
	Capped bool
}

// TrashPurgeStore is the part of the database TrashPurgeWorker uses needs.
type TrashPurgeStore interface {
	BlobReferenceCount(ctx context.Context, sha256 string) (int, error)
	DecrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error
	GetDB() *sql.DB
}

// TrashPurgeWorker permanently removes soft-deleted items (and repositories)
// whose retention window has passed: it purges the rows, frees their blobs via
// storage.SafeDeleteBlob (which keeps blobs other rows or repositories still
// reference), releases quota, and records the audit event and webhook.
//
// Every pass is resumable and idempotent. The database purge commits first;
// a crash before the blobs are freed leaves unreferenced blobs that the orphan
// collector (this worker's closing sweep, or the cleanup worker) reclaims, and
// quota counters a crash left off are corrected by the periodic reconcile.
// Replicas are excluded from each other by a Postgres advisory lock held on a
// pinned connection for the whole pass.
type TrashPurgeWorker struct {
	*lifecycle

	db        TrashPurgeStore
	store     database.TrashStore
	blobStore storage.BlobStore
	log       *logger.Logger
	interval  time.Duration
	publisher webhook.Publisher
	reg       *metrics.Registry

	// lock takes the cluster-wide purge lock. It is a field so tests can
	// simulate another replica holding it.
	lock func(ctx context.Context) (release func(), acquired bool, err error)
}

// NewTrashPurgeWorker creates the worker. interval is the pause between
// scheduled passes; a non-positive interval disables the schedule (RunOnce
// still works). Without a database that implements database.TrashStore the
// worker does nothing.
func NewTrashPurgeWorker(db TrashPurgeStore, blobStore storage.BlobStore, log *logger.Logger, interval time.Duration) *TrashPurgeWorker {
	store, _ := db.(database.TrashStore)

	w := &TrashPurgeWorker{
		lifecycle: newLifecycle(),
		db:        db,
		store:     store,
		blobStore: blobStore,
		log:       log,
		interval:  interval,
	}
	w.lock = w.acquireLock

	return w
}

// SetPublisher wires the webhook publisher for artifact.purged and
// repository.purged events. It must be called before Start.
//
// A purged repository's own webhooks are removed with it, so only global
// webhooks (those not tied to a repository) receive repository.purged.
func (w *TrashPurgeWorker) SetPublisher(p webhook.Publisher) { w.publisher = p }

// WithMetrics counts purged items in reg.TrashPurgedTotal.
func (w *TrashPurgeWorker) WithMetrics(reg *metrics.Registry) *TrashPurgeWorker {
	w.reg = reg

	return w
}

// Start begins the scheduled passes. It does nothing when the interval is not
// positive or the database has no trash support. Only the first call counts.
func (w *TrashPurgeWorker) Start() {
	w.startEvery(w.store != nil, w.interval, w.runScheduled)
}

// runScheduled runs one pass of the schedule. A panic ends the pass, not the
// schedule.
func (w *TrashPurgeWorker) runScheduled() {
	defer func() {
		if r := recover(); r != nil {
			w.log.Errorw("panic in trash purge pass", "panic", r)
		}
	}()

	ctx, cancel := context.WithTimeout(w.runCtx, trashPurgeRunTimeout)
	defer cancel()

	stats, err := w.RunOnce(ctx)
	if err != nil && w.runCtx.Err() == nil {
		w.log.Errorw("trash purge failed", "error", err)
	}

	if stats.Purged > 0 || stats.Failed > 0 || stats.BlobErrors > 0 {
		w.log.Infow("trash purge finished",
			"purged", stats.Purged, "repositories", stats.Repositories, "failed", stats.Failed,
			"blob_errors", stats.BlobErrors, "orphans_removed", stats.OrphansRemoved, "capped", stats.Capped)
	}
}

// RunOnce runs one purge pass: it removes every item whose retention has
// passed (at most trashPurgeMaxItems) and, when it removed anything, sweeps
// orphaned blobs. A pass that finds the purge lock held by another replica does
// nothing and reports Busy. A failing item is logged, counted in Failed and
// skipped; it never aborts the pass. The error is non-nil only when the pass
// could not run to the end (lock or listing failure, cancelled ctx).
func (w *TrashPurgeWorker) RunOnce(ctx context.Context) (PurgeStats, error) {
	var stats PurgeStats

	if w.store == nil {
		return stats, nil
	}

	release, acquired, err := w.lock(ctx)
	if err != nil {
		return stats, fmt.Errorf("lock trash purge: %w", err)
	}

	if !acquired {
		stats.Busy = true

		return stats, nil
	}
	defer release()

	err = w.purgeExpired(ctx, &stats)

	if stats.Purged > 0 && ctx.Err() == nil {
		w.sweepOrphans(ctx, &stats)
	}

	return stats, err
}

// acquireLock takes the advisory lock on a pinned connection. A database
// without an SQL pool (test fakes) has no other replicas to exclude.
func (w *TrashPurgeWorker) acquireLock(ctx context.Context) (func(), bool, error) {
	return tryClusterLock(ctx, w.db.GetDB(), trashPurgeLockKey)
}

// purgeExpired purges expired items batch by batch until none is left, the
// per-pass cap is reached, or ctx ends.
//
// Every ref is attempted at most once per pass: an item whose purge fails stays
// in the listing, so retrying it would loop forever. The listing is widened by
// the number of failures so a backlog of failing items at the front never
// hides the purgeable ones behind it.
func (w *TrashPurgeWorker) purgeExpired(ctx context.Context, stats *PurgeStats) error {
	attempted := map[models.TrashRef]struct{}{}

	for len(attempted) < trashPurgeMaxItems {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("trash purge interrupted: %w", err)
		}

		refs, err := w.store.ListExpiredTrash(ctx, trashPurgeBatch+stats.Failed)
		if err != nil {
			return fmt.Errorf("list expired trash: %w", err)
		}

		progressed := false

		for _, ref := range refs {
			if _, done := attempted[ref]; done {
				continue
			}

			if len(attempted) >= trashPurgeMaxItems {
				break
			}

			if err := ctx.Err(); err != nil {
				return fmt.Errorf("trash purge interrupted: %w", err)
			}

			attempted[ref] = struct{}{}
			progressed = true

			w.purgeOne(ctx, ref, stats)
		}

		if !progressed {
			return nil
		}
	}

	stats.Capped = true

	return nil
}

// purgeOne removes one expired item and runs the follow-up cleanup.
func (w *TrashPurgeWorker) purgeOne(ctx context.Context, ref models.TrashRef, stats *PurgeStats) {
	res, err := w.store.PurgeTrash(ctx, ref.Kind, ref.ID)

	switch {
	case errors.Is(err, database.ErrTrashNotFound):
		// Restored or purged by someone else since it was listed.
		return
	case err != nil && ctx.Err() != nil:
		// Interrupted; the caller reports the cancellation.
		return
	case err != nil:
		stats.Failed++
		w.logPurgeFailure(ref, err)

		return
	}

	stats.Purged++

	if res.Kind == models.TrashKindRepository {
		stats.Repositories++
	}

	w.afterPurge(ctx, res, stats)
}

func (w *TrashPurgeWorker) logPurgeFailure(ref models.TrashRef, err error) {
	if database.IsForeignKeyViolation(err) {
		w.log.Warnw("trash purge skipped: item is still referenced", "kind", ref.Kind, "id", ref.ID, "error", err)

		return
	}

	w.log.Errorw("trash purge failed for item", "kind", ref.Kind, "id", ref.ID, "error", err)
}

// afterPurge does what the database purge leaves to the caller. Each step is
// best effort: the row is already gone, so a failure is logged and the same
// state is repaired later (orphan collector, quota reconcile) rather than
// retried here. The slow storage deletes go last.
func (w *TrashPurgeWorker) afterPurge(ctx context.Context, res *models.TrashPurgeResult, stats *PurgeStats) {
	w.releaseQuota(ctx, res)

	trash.AuditPurged(ctx, res, "retention")

	event, payload := purgedEvent(res)
	webhook.Emit(ctx, w.publisher, res.RepositoryID, event, payload)

	if w.reg != nil {
		w.reg.TrashPurgedTotal.WithLabelValues(string(res.Kind)).Inc()
	}

	stats.BlobErrors += w.freeBlobs(ctx, res)
	stats.BlobErrors += w.freeMetadataBlobs(ctx, res)
}

// freeMetadataBlobs reclaims the generated-metadata blobs of a purged
// repository (they have no blobs row, so the orphan collector never sees them).
func (w *TrashPurgeWorker) freeMetadataBlobs(ctx context.Context, res *models.TrashPurgeResult) int {
	if w.blobStore == nil {
		return 0
	}

	failed := 0

	for _, sha := range res.MetadataBlobSHA256 {
		if ctx.Err() != nil {
			return failed + 1
		}

		if err := storage.ReclaimMetadataBlob(ctx, w.db, w.blobStore, sha); err != nil {
			w.log.Warnw("trash purge: freeing metadata blob failed", "sha256", sha, "repository", res.RepositoryName, "error", err)

			failed++
		}
	}

	return failed
}

// releaseQuota gives back the quota the purged rows were holding (soft-deleted
// rows keep counting against it until now).
func (w *TrashPurgeWorker) releaseQuota(ctx context.Context, res *models.TrashPurgeResult) {
	if res.QuotaArtifacts <= 0 {
		return
	}

	if err := w.db.DecrementStorageUsage(ctx, res.RepositoryID, res.QuotaBytes, res.QuotaArtifacts); err != nil {
		w.log.Warnw("trash purge: quota release failed (the quota reconcile corrects the counters)",
			"repository_id", res.RepositoryID, "error", err)
	}
}

// freeBlobs deletes the blobs the purged rows referenced, through
// storage.SafeDeleteBlob so blobs still referenced elsewhere stay. It returns
// the number of blobs it could not free.
func (w *TrashPurgeWorker) freeBlobs(ctx context.Context, res *models.TrashPurgeResult) int {
	if w.blobStore == nil {
		return 0
	}

	failed := 0

	for _, sha := range slices.Compact(slices.Sorted(slices.Values(res.BlobSHA256))) {
		if ctx.Err() != nil {
			// Shutting down: the orphan collector frees the rest later.
			return failed + 1
		}

		if err := storage.SafeDeleteBlob(ctx, w.db, w.blobStore, sha); err != nil {
			w.log.Warnw("trash purge: freeing blob failed (the orphan collector retries)",
				"sha256", sha, "kind", res.Kind, "id", res.ID, "error", err)

			failed++
		}
	}

	return failed
}

// sweepOrphans removes blobs no row references any more: what a repository
// purge's cascade left behind and whatever an earlier crash orphaned. It
// mirrors the sweep a repository delete runs (internal/api/repositories.go).
func (w *TrashPurgeWorker) sweepOrphans(ctx context.Context, stats *PurgeStats) {
	if w.blobStore == nil {
		return
	}

	for range trashPurgeMaxOrphanPasses {
		removed, err := storage.CollectOrphanBlobs(ctx, w.db, w.blobStore, storage.OrphanMinAge, storage.OrphanBatch)
		stats.OrphansRemoved += removed

		if err != nil {
			w.log.Warnw("trash purge: orphan blob collection failed", "removed", removed, "error", err)

			return
		}

		if removed < storage.OrphanBatch {
			return
		}
	}
}

// purgedEvent builds the webhook event of a purged item. The repository row is
// gone, so the repository payload carries no repository type.
func purgedEvent(res *models.TrashPurgeResult) (string, map[string]any) {
	if res.Kind == models.TrashKindRepository {
		payload := webhook.RepositoryPayload(webhook.EventRepoPurged, res.RepositoryName, res.Format, "")
		delete(payload, "type")

		return webhook.EventRepoPurged, payload
	}

	return webhook.EventArtifactPurged, webhook.ArtifactPayload(webhook.EventArtifactPurged, webhook.Artifact{
		Repository: res.RepositoryName,
		Format:     res.Format,
		Name:       res.Name,
		Version:    res.Version,
		Size:       res.SizeBytes,
	})
}
