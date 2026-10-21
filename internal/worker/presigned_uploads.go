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
// accumulate in the bucket.
type PresignedUploadWorker struct {
	db      database.Database
	staging storage.StagingStore
	log     *logger.Logger

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// NewPresignedUploadWorker builds the worker. blobStore that cannot stage
// uploads yields a worker that only removes the rows.
func NewPresignedUploadWorker(db database.Database, blobStore storage.BlobStore, log *logger.Logger) *PresignedUploadWorker {
	staging, _ := blobStore.(storage.StagingStore)

	return &PresignedUploadWorker{
		db:      db,
		staging: staging,
		log:     log,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
}

// Start launches the sweep loop.
func (w *PresignedUploadWorker) Start() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				w.log.Errorw("panic in presigned upload worker", "panic", r)
			}
		}()
		defer close(w.doneCh)

		ticker := time.NewTicker(presignedUploadSweepInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), presignedUploadSweepTimeout)
				w.Sweep(ctx)
				cancel()
			case <-w.stopCh:
				return
			}
		}
	}()
}

// Stop ends the loop and waits for the in-flight sweep.
func (w *PresignedUploadWorker) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
	<-w.doneCh
}

// Sweep removes every expired upload (in batches) and returns how many it
// removed. A failure on one upload leaves it for the next sweep.
func (w *PresignedUploadWorker) Sweep(ctx context.Context) int {
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
