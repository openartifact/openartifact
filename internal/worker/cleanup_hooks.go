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

// refreshFormatMetadata is the per-format follow-up of a run that removed rows
// (moved to the trash or deleted). Formats not handled here compute their
// indexes live from the tables (pypi simple, helm index.yaml, cargo index,
// rubygems specs, conda repodata, conan, nuget, go, ivy, sbt, raw).
func (w *CleanupWorker) refreshFormatMetadata(ctx context.Context, repo *models.Repository, run *cleanupRun) {
	switch repo.Format {
	case "maven":
		w.refreshMaven(repo, run.names())
	case "npm":
		w.refreshNPM(ctx, repo, run.names())
	case "docker":
		w.collectDocker(ctx, repo, run)
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
// step with their versions: a package that lost its last version row is
// dropped, the others get a new packument validator and a regeneration of the
// latest version. Versions a dist-tag points at are never removed, so no
// dist-tag is left dangling.
func (w *CleanupWorker) refreshNPM(ctx context.Context, repo *models.Repository, names []string) {
	for _, n := range names {
		var pkgID string

		err := w.db.GetDB().QueryRowContext(ctx,
			`SELECT id::text FROM npm_packages WHERE repository_id = $1 AND full_name = $2`, repo.ID, n).Scan(&pkgID)
		if err != nil {
			w.log.Warnw("cleanup: failed to resolve npm package", "repository_id", repo.ID, "package", n, "error", err)
			continue
		}

		res, err := w.db.GetDB().ExecContext(ctx,
			`DELETE FROM npm_packages p WHERE p.id = $1::uuid AND NOT EXISTS (SELECT 1 FROM npm_versions v WHERE v.package_id = p.id)`, pkgID)
		if err == nil {
			if rows, _ := res.RowsAffected(); rows > 0 {
				continue
			}
		}

		if err := w.db.MarkPackumentStale(ctx, pkgID); err != nil {
			w.log.Warnw("cleanup: failed to mark packument stale", "repository_id", repo.ID, "package", n, "error", err)
		}

		if w.npmNotifier != nil {
			w.npmNotifier.NotifyStale(pkgID)
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
	case errors.Is(err, ErrDockerGCBusy), errors.Is(err, ErrDockerGCUnsupported):
		w.log.Debugw("cleanup: docker garbage collection skipped", "repository_id", repo.ID, "reason", err)
	default:
		w.log.Warnw("cleanup: docker garbage collection failed", "repository_id", repo.ID, "error", fmt.Errorf("run gc: %w", err))
	}
}
