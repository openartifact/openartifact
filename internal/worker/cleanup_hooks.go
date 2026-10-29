// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"

	"github.com/openartifact/openartifact/internal/database/models"
	formatlifecycle "github.com/openartifact/openartifact/internal/format/lifecycle"
)

// SetLifecycleWorkers wires the metadata workers a cleanup run needs to leave
// the generated metadata (rpm, deb, pacman, apk indexes, maven-metadata.xml, the
// npm packument, the docker registry) consistent with the rows it removed. A
// format with no worker in ws gets no refresh; the stale sweep recovers the
// formats that mark their repository stale.
func (w *CleanupWorker) SetLifecycleWorkers(ws *formatlifecycle.Workers) { w.workers = ws }

// refreshFormatMetadata is the per-format follow-up of a run that removed rows
// (moved to the trash or deleted): the lifecycle hook of the repository's
// format. Formats without one compute their indexes live from the tables.
func (w *CleanupWorker) refreshFormatMetadata(ctx context.Context, repo *models.Repository, run *cleanupRun) {
	hooks, ok := formatlifecycle.Lookup(repo.Format)
	if !ok || hooks.Removed == nil {
		return
	}

	hooks.Removed(ctx, &formatlifecycle.Env{
		Log:                w.log,
		Workers:            w.workers,
		MarkRepoStale:      w.db.MarkRepoStale,
		MarkPackumentStale: w.db.MarkPackumentStale,
		SQL:                w.db.GetDB(),
	}, formatlifecycle.Removal{Repo: repo, Names: run.names(), Hard: run.hard})
}
