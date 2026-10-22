// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	presignedUploadSweepInterval = 5 * time.Minute
	presignedUploadSweepBatch    = 200
	presignedUploadSweepTimeout  = 2 * time.Minute
)

// PresignedUploadWorker expires presigned uploads that were never completed:
// it deletes their staging object and their database row once the complete
// deadline (URL expiry plus grace) has passed, so abandoned uploads do not
// accumulate in the bucket. It also sweeps the staging prefix for objects that
// have no row at all (a client re-PUT to a still-valid URL after its upload
// was cleaned up or rejected, a repository deleted with uploads in flight) once
// they are older than any URL could still be valid.
type PresignedUploadWorker struct {
	db      database.Database
	staging storage.StagingStore
	log     *logger.Logger
	// orphanAge is how old a staging object without a row must be to be deleted.
	orphanAge time.Duration

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// NewPresignedUploadWorker builds the worker. urlTTL is the upload URL
// lifetime the server signs with; staging objects without a row are only
// deleted once they are older than that plus the complete grace. A blobStore
// that cannot stage uploads yields a worker that only removes the rows.
func NewPresignedUploadWorker(db database.Database, blobStore storage.BlobStore, urlTTL time.Duration, log *logger.Logger) *PresignedUploadWorker {
	var staging storage.StagingStore
	if s, ok := blobStore.(storage.StagingStore); ok && s.StagingSupported() {
		staging = s
	}

	return &PresignedUploadWorker{
		db:        db,
		staging:   staging,
		log:       log,
		orphanAge: storage.ClampStagingTTL(urlTTL) + time.Hour,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// Start launches the sweep loop.
func (w *PresignedUploadWorker) Start() {
	w.startOnce.Do(w.start)
}

func (w *PresignedUploadWorker) start() {
	go func() {
		defer close(w.doneCh)

		ticker := time.NewTicker(presignedUploadSweepInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				w.safeSweep()
			case <-w.stopCh:
				return
			}
		}
	}()
}

// safeSweep contains a panic to one sweep so the loop keeps running.
func (w *PresignedUploadWorker) safeSweep() {
	defer func() {
		if r := recover(); r != nil {
			w.log.Errorw("panic in presigned upload sweep; continuing", "panic", r)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), presignedUploadSweepTimeout)
	defer cancel()

	w.Sweep(ctx)
}

// Stop ends the loop and waits for the in-flight sweep.
func (w *PresignedUploadWorker) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })

	// Never started: nothing will close doneCh, and a later Start must not.
	w.startOnce.Do(func() { close(w.doneCh) })

	<-w.doneCh
}

// Sweep removes every expired upload (in batches) and every orphaned staging
// object, and returns how many it removed. A failure on one leaves it for the
// next sweep.
func (w *PresignedUploadWorker) Sweep(ctx context.Context) int {
	return w.sweepExpired(ctx) + w.sweepOrphans(ctx)
}

func (w *PresignedUploadWorker) sweepExpired(ctx context.Context) int {
	removed := 0

	for ctx.Err() == nil {
		ids, err := w.db.ListExpiredPresignedUploads(ctx, presignedUploadSweepBatch)
		if err != nil {
			w.log.Errorw("failed to list expired presigned uploads", "error", err)
			return removed
		}

		done := 0

		for _, id := range ids {
			if w.expire(ctx, id) {
				done++
			}
		}

		removed += done

		if len(ids) < presignedUploadSweepBatch || done == 0 {
			return removed
		}
	}

	return removed
}

func (w *PresignedUploadWorker) expire(ctx context.Context, id string) bool {
	if w.staging != nil {
		if err := w.staging.DeleteStaging(ctx, id); err != nil {
			w.log.Warnw("failed to delete expired staging object", "upload_id", id, "error", err)
			return false
		}
	}

	if err := w.db.DeletePresignedUpload(ctx, id); err != nil {
		w.log.Warnw("failed to delete expired presigned upload", "upload_id", id, "error", err)
		return false
	}

	return true
}

// sweepOrphans deletes staging objects that no upload row refers to and that
// are older than orphanAge. It returns how many it deleted.
func (w *PresignedUploadWorker) sweepOrphans(ctx context.Context) int {
	if w.staging == nil {
		return 0
	}

	removed := 0

	err := w.staging.WalkStaging(ctx, time.Now().Add(-w.orphanAge), func(ids []string) error {
		known, err := w.db.ExistingPresignedUploadIDs(ctx, ids)
		if err != nil {
			return err //nolint:wrapcheck // logged by the caller
		}

		withRow := make(map[string]struct{}, len(known))
		for _, id := range known {
			withRow[id] = struct{}{}
		}

		for _, id := range ids {
			if _, ok := withRow[id]; ok {
				continue
			}

			if err := w.staging.DeleteStaging(ctx, id); err != nil {
				w.log.Warnw("failed to delete orphaned staging object", "upload_id", id, "error", err)
				continue
			}

			removed++
		}

		return ctx.Err()
	})
	if err != nil {
		w.log.Warnw("failed to sweep orphaned staging objects", "error", err)
	}

	return removed
}
