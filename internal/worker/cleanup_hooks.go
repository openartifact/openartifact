// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// MavenNotifier schedules the regeneration of one artifact's maven-metadata.xml
// files. It is satisfied by *MavenMetadataWorker.
type MavenNotifier interface {
	NotifyStale(repoID, groupID, artifactID string)
}

// NPMNotifier schedules the regeneration of one package's denormalized
// packument fields (latest version). It is satisfied by *NPMMetadataWorker.
type NPMNotifier interface{ NotifyStale(packageID string) }

// DockerGCRunner collects the manifests and layers a removed tag left behind.
// It is satisfied by *DockerGCWorker.
type DockerGCRunner interface {
	RunRepo(ctx context.Context, repo *models.Repository, opts DockerGCOptions) (*DockerGCReport, error)
	DefaultGrace() time.Duration
}

// SetRegenNotifiers wires what a cleanup run needs to leave the generated
// metadata of maven (metadata files) and npm (packument), and the docker
// registry (untagged manifests), consistent with the rows it removed. Any may
// be nil when that worker is not running.
func (w *CleanupWorker) SetRegenNotifiers(maven MavenNotifier, npm NPMNotifier, dockerGC DockerGCRunner) {
	w.mavenNotifier, w.npmNotifier, w.dockerGC = maven, npm, dockerGC
}

// refreshMetadata is the follow-up of a run that removed rows (moved to the
// trash or deleted), once per run and repository. It is the ONE place that maps
// a format to the metadata worker that must refresh afterwards. Formats not
// listed compute their indexes live from the tables (cargo index, conan, conda
// repodata, go, helm index.yaml, ivy, nuget, pypi simple, raw, rubygems specs,
// sbt, terraform). rpm is marked stale in the database first, so the stale
// sweep recovers the refresh even if the notifier is absent or the process dies.
func (w *CleanupWorker) refreshMetadata(ctx context.Context, repo *models.Repository, run *cleanupRun) {
	switch repo.Format {
	case "rpm":
		w.markRPMStale(ctx, repo)
		notifyRepo(w.rpmNotifier, repo.ID)
	case "deb":
		notifyRepo(w.debNotifier, repo.ID)
	case "pacman":
		notifyRepo(w.pacmanNotifier, repo.ID)
	case "apk":
		notifyRepo(w.apkNotifier, repo.ID)
	case "maven":
		w.refreshMaven(repo, run.names())
	case "npm":
		w.refreshNPM(ctx, repo, run.packageIDs())
	case dockerFormat:
		w.collectDocker(ctx, repo, run)
	}
}

func notifyRepo(n MetadataNotifier, repoID string) {
	if n != nil {
		n.NotifyStale(repoID)
	}
}

func (w *CleanupWorker) markRPMStale(ctx context.Context, repo *models.Repository) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), policyRecordTimeout)
	defer cancel()

	if err := w.db.MarkRepoStale(rctx, repo.ID); err != nil {
		w.log.Warnw("cleanup: failed to mark rpm repository stale", "repository_id", repo.ID, "error", err)
	}
}

// refreshMaven regenerates the version list and the snapshot metadata of every
// artifact a run removed files of, so clients stop resolving removed versions
// and builds.
func (w *CleanupWorker) refreshMaven(repo *models.Repository, names []string) {
	if w.mavenNotifier == nil {
		return
	}

	for _, n := range names {
		group, artifact, ok := strings.Cut(n, ":")
		if !ok {
			continue
		}

		w.mavenNotifier.NotifyStale(repo.ID, group, artifact)
	}
}

// refreshNPM keeps the packuments of the packages a run removed versions of in
// step with their versions: each gets a new packument validator and a
// regeneration of the latest version. A package row that is gone (purged with
// its last version) has nothing to refresh. Versions a dist-tag points at are
// never removed, so no dist-tag is left dangling.
func (w *CleanupWorker) refreshNPM(ctx context.Context, repo *models.Repository, packageIDs []string) {
	for _, id := range packageIDs {
		var exists bool

		err := w.db.GetDB().QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM packages WHERE id = $1::uuid AND repository_id = $2::uuid)`, id, repo.ID).Scan(&exists)
		if err != nil {
			w.log.Warnw("cleanup: failed to resolve npm package", "repository_id", repo.ID, "package_id", id, "error", err)
			continue
		}

		if !exists {
			continue
		}

		if err := w.db.MarkPackumentStale(ctx, id); err != nil {
			w.log.Warnw("cleanup: failed to mark packument stale", "repository_id", repo.ID, "package_id", id, "error", err)
		}

		if w.npmNotifier != nil {
			w.npmNotifier.NotifyStale(id)
		}
	}
}

// collectDocker runs registry garbage collection after tags were deleted for
// good: the manifests only they tagged and the layers only those referenced are
// reclaimed (with the GC's own grace period). Tags moved to the trash change
// nothing for the GC until they are purged.
func (w *CleanupWorker) collectDocker(ctx context.Context, repo *models.Repository, run *cleanupRun) {
	if w.dockerGC == nil || run.hard == 0 {
		return
	}

	_, err := w.dockerGC.RunRepo(ctx, repo, DockerGCOptions{Grace: w.dockerGC.DefaultGrace()})

	switch {
	case err == nil:
	case errors.Is(err, ErrDockerGCBusy):
		w.log.Debugw("cleanup: docker garbage collection skipped", "repository_id", repo.ID, "reason", err)
	default:
		w.log.Warnw("cleanup: docker garbage collection failed", "repository_id", repo.ID, "error", fmt.Errorf("run gc: %w", err))
	}
}
