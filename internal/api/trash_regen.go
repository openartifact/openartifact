// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
)

// TrashRegenerator schedules the metadata refresh a restored item needs: a
// row that left the trash changes what the format's generated metadata lists,
// exactly like a fresh upload. Every method only enqueues work.
type TrashRegenerator interface {
	NotifyDebStale(repoID string)
	NotifyRPMStale(repoID string)
	NotifyPacmanStale(repoID string)
	NotifyApkStale(repoID string)
	NotifyMavenStale(repoID, groupID, artifactID string)
	NotifyNPMStale(packageID string)
}

// Narrow views of the metadata workers (*worker.DebMetadataWorker and friends).
type (
	repoStaleNotifier  interface{ NotifyStale(repoID string) }
	mavenStaleNotifier interface {
		NotifyStale(repoID, groupID, artifactID string)
	}
	npmStaleNotifier interface{ NotifyStale(packageID string) }
)

// workerRegenerator adapts the metadata workers to TrashRegenerator. A nil
// worker (feature disabled) is skipped.
type workerRegenerator struct {
	apk, deb, pacman, rpm repoStaleNotifier
	maven                 mavenStaleNotifier
	npm                   npmStaleNotifier
}

// NewWorkerRegenerator builds the TrashRegenerator over the metadata workers
// constructed in main.go. Pass nil for a worker that is not running.
func NewWorkerRegenerator(deb, rpm, apk, pacman repoStaleNotifier, maven mavenStaleNotifier, npm npmStaleNotifier) TrashRegenerator {
	return &workerRegenerator{apk: apk, deb: deb, maven: maven, npm: npm, pacman: pacman, rpm: rpm}
}

func (w *workerRegenerator) NotifyDebStale(repoID string) {
	if w.deb != nil {
		w.deb.NotifyStale(repoID)
	}
}

func (w *workerRegenerator) NotifyRPMStale(repoID string) {
	if w.rpm != nil {
		w.rpm.NotifyStale(repoID)
	}
}

func (w *workerRegenerator) NotifyPacmanStale(repoID string) {
	if w.pacman != nil {
		w.pacman.NotifyStale(repoID)
	}
}

func (w *workerRegenerator) NotifyApkStale(repoID string) {
	if w.apk != nil {
		w.apk.NotifyStale(repoID)
	}
}

func (w *workerRegenerator) NotifyMavenStale(repoID, groupID, artifactID string) {
	if w.maven != nil {
		w.maven.NotifyStale(repoID, groupID, artifactID)
	}
}

func (w *workerRegenerator) NotifyNPMStale(packageID string) {
	if w.npm != nil {
		w.npm.NotifyStale(packageID)
	}
}

// regenerateRestored refreshes the generated metadata of the repository an
// item was restored into, through the Restored hook of its format. Formats
// without one compute their indexes live from the tables; restoring a whole
// repository changes no index. Failures are logged: the restore itself already
// succeeded.
func (h *TrashHandler) regenerateRestored(ctx context.Context, item *models.TrashItem) {
	if h.workers == nil || item.Kind == models.TrashKindRepository {
		return
	}

	hooks, ok := lifecycle.Lookup(item.Format)
	if !ok || hooks.Restored == nil {
		return
	}

	hooks.Restored(ctx, &lifecycle.Env{
		Log:                h.log,
		Workers:            h.workers,
		MarkRepoStale:      h.db.MarkRepoStale,
		MarkPackumentStale: h.db.MarkPackumentStale,
	}, lifecycle.Restoration{
		RepositoryID:   item.RepositoryID,
		RepositoryName: item.RepositoryName,
		Kind:           item.Kind,
		RegenKeys: func(ctx context.Context) (string, string, error) {
			store, _ := h.db.(database.TrashOps)
			if store == nil {
				return "", "", errNoRegenKeys
			}

			return store.RegenKeys(ctx, item.Kind, item.ID) //nolint:wrapcheck // the caller logs it with context
		},
	})
}

// errNoRegenKeys: the store cannot resolve the keys a regeneration needs.
var errNoRegenKeys = errors.New("store cannot resolve regeneration keys")
