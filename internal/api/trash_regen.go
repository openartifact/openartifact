// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// TrashRegenerator schedules the metadata refresh a restored item needs: a
// row that left the trash changes what the format's generated metadata lists,
// exactly like a fresh upload. Every method only enqueues work.
type TrashRegenerator interface {
	NotifyDebStale(repoID string)
	NotifyRPMStale(repoID string)
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
	deb, rpm, apk repoStaleNotifier
	maven         mavenStaleNotifier
	npm           npmStaleNotifier
}

// NewWorkerRegenerator builds the TrashRegenerator over the metadata workers
// constructed in main.go. Pass nil for a worker that is not running.
func NewWorkerRegenerator(deb, rpm, apk repoStaleNotifier, maven mavenStaleNotifier, npm npmStaleNotifier) TrashRegenerator {
	return &workerRegenerator{deb: deb, rpm: rpm, apk: apk, maven: maven, npm: npm}
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
// item was restored into, by format: rpm marks the repository stale and
// notifies the worker, deb and apk only notify it, npm marks the packument stale and
// notifies, maven notifies per artifact, and every other format computes its
// indexes live from the tables. Failures are logged: the restore itself
// already succeeded.
func (h *TrashHandler) regenerateRestored(ctx context.Context, item *models.TrashItem) {
	if h.regen == nil {
		return
	}

	switch item.Kind { //nolint:exhaustive // only these kinds have generated metadata
	case models.TrashKindDeb:
		// No stale flag exists for deb: the worker refresh is the whole job.
		h.regen.NotifyDebStale(item.RepositoryID)
	case models.TrashKindApk:
		h.regen.NotifyApkStale(item.RepositoryID)
	case models.TrashKindRPM:
		h.markRepoStale(ctx, item)
		h.regen.NotifyRPMStale(item.RepositoryID)
	case models.TrashKindMaven:
		groupID, artifactID, ok := h.regenKeys(ctx, item)
		if ok {
			h.regen.NotifyMavenStale(item.RepositoryID, groupID, artifactID)
		}
	case models.TrashKindNPM:
		packageID, _, ok := h.regenKeys(ctx, item)
		if ok {
			// Flip the packument's stale marker first so its ETag changes at once.
			h.markPackumentStale(ctx, item, packageID)
			h.regen.NotifyNPMStale(packageID)
		}
	}
}

func (h *TrashHandler) markRepoStale(ctx context.Context, item *models.TrashItem) {
	if err := h.db.MarkRepoStale(ctx, item.RepositoryID); err != nil {
		h.log.Warnw("trash restore: mark repository stale failed", "repository", item.RepositoryName, "kind", item.Kind, "error", err)
	}
}

func (h *TrashHandler) markPackumentStale(ctx context.Context, item *models.TrashItem, packageID string) {
	if err := h.db.MarkPackumentStale(ctx, packageID); err != nil {
		h.log.Warnw("trash restore: mark packument stale failed", "repository", item.RepositoryName, "package_id", packageID, "error", err)
	}
}

func (h *TrashHandler) regenKeys(ctx context.Context, item *models.TrashItem) (string, string, bool) {
	store, _ := h.db.(database.TrashStore)
	if store == nil {
		return "", "", false
	}

	a, b, err := store.RegenKeys(ctx, item.Kind, item.ID)
	if err != nil {
		h.log.Warnw("trash restore: resolve regeneration keys failed", "repository", item.RepositoryName, "kind", item.Kind, "error", err)
		return "", "", false
	}

	return a, b, true
}
