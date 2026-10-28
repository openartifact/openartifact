// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"time"

	"github.com/openartifact/openartifact/internal/storage"
)

const (
	// nixPendingMaxAge is how long an uploaded NAR waits for its narinfo before
	// it is considered abandoned (an interrupted nix copy).
	nixPendingMaxAge = 24 * time.Hour
	// nixPendingInterval is the minimum time between two sweeps.
	nixPendingInterval = time.Hour
	// nixPendingBatch bounds the rows one database call removes.
	nixPendingBatch = 500
)

// sweepNixPendingNarsIfDue deletes the staged NAR files whose narinfo never
// arrived, at most once per nixPendingInterval. The staging rows go first; a
// blob that a committed path of another repository (or of the same NAR) still
// references is kept by storage.SafeDeleteBlob.
func (w *CleanupWorker) sweepNixPendingNarsIfDue(ctx context.Context, now time.Time) {
	if w.blobStore == nil || ctx.Err() != nil || now.Sub(w.lastNixPendingSweep) < nixPendingInterval {
		return
	}

	w.lastNixPendingSweep = now

	before := now.Add(-nixPendingMaxAge)
	removed := 0

	for ctx.Err() == nil {
		shas, err := w.db.DeleteStaleNixPendingNars(ctx, before, nixPendingBatch)
		if err != nil {
			w.log.Warnw("nix pending nar sweep failed", "removed", removed, "error", err)

			return
		}

		for _, sha := range shas {
			if err := storage.SafeDeleteBlob(ctx, w.db, w.blobStore, sha); err != nil {
				w.log.Warnw("failed to delete stale nix nar blob", "sha256", sha, "error", err)
			}
		}

		removed += len(shas)

		if len(shas) < nixPendingBatch {
			break
		}
	}

	if removed > 0 {
		w.log.Infow("removed stale nix pending nars", "removed", removed)
	}
}
