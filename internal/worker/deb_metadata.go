// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	sq "github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/logger"
)

var errDebLockBusy = errors.New("could not acquire advisory lock")

// listOldMetadata returns path -> blob sha256 of the generated dists/ metadata rows.
func listOldMetadata(ctx context.Context, tx sq.Transaction, repoID string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT path, blob_sha256 FROM repository_metadata WHERE repository_id = $1 AND path LIKE 'dists/%'`, repoID)
	if err != nil {
		return nil, fmt.Errorf("deb worker: listing repo_metadata: %w", err)
	}
	defer func() { _ = rows.Close() }()

	old := make(map[string]string)

	for rows.Next() {
		var path, sum string
		if err := rows.Scan(&path, &sum); err != nil {
			return nil, fmt.Errorf("deb worker: scanning repo_metadata: %w", err)
		}

		old[path] = sum
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("deb worker: listing repo_metadata: %w", err)
	}

	return old, nil
}

type DebMetadataWorker struct {
	db        sq.Database
	blobStore storage.BlobStore
	signer    gpg.Signer
	log       *logger.Logger

	mu       sync.Mutex
	timers   map[string]*time.Timer
	debounce time.Duration
}

func NewDebMetadataWorker(db sq.Database, blobStore storage.BlobStore, signer gpg.Signer, log *logger.Logger) *DebMetadataWorker {
	return &DebMetadataWorker{
		db:        db,
		blobStore: blobStore,
		signer:    signer,
		log:       log,
		timers:    make(map[string]*time.Timer),
		debounce:  500 * time.Millisecond,
	}
}

func (w *DebMetadataWorker) NotifyStale(repoID string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if t, ok := w.timers[repoID]; ok {
		t.Stop()
	}

	var t *time.Timer

	t = time.AfterFunc(w.debounce, func() {
		defer func() {
			if r := recover(); r != nil {
				w.log.Errorw("panic in goroutine", "panic", r)
			}
		}()
		w.log.Infow("deb metadata regeneration triggered", "repo_id", repoID)

		if err := w.regenerate(context.Background(), repoID); err != nil {
			w.log.Errorw("deb metadata regeneration failed", "repo_id", repoID, "error", err)
		}

		w.mu.Lock()
		if w.timers[repoID] == t {
			delete(w.timers, repoID)
		}
		w.mu.Unlock()
	})
	w.timers[repoID] = t
}

func (w *DebMetadataWorker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()

	for id, t := range w.timers {
		t.Stop()
		delete(w.timers, id)
	}
}

//nolint:gocyclo,cyclop // protocol handler: branching mirrors spec state machine
func (w *DebMetadataWorker) regenerate(ctx context.Context, repoID string) error {
	release, locked, err := tryAdvisoryLock(ctx, w.db.GetDB(), "deb:"+repoID)
	if err != nil {
		return fmt.Errorf("deb worker: could not acquire advisory lock for %s: %w", repoID, err)
	}

	if !locked {
		return fmt.Errorf("%w: deb worker repository %s", errDebLockBusy, repoID)
	}

	defer release()

	tx, err := w.db.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("deb worker: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	distributions, err := w.db.GetDebianDistributionsByRepo(ctx, repoID)
	if err != nil {
		return fmt.Errorf("deb worker: querying distributions: %w", err)
	}

	// One query for every package in the repository; grouped in memory below.
	entries, err := w.db.ListDebPackageEntries(ctx, repoID, "", "", "")
	if err != nil {
		return fmt.Errorf("deb worker: querying packages: %w", err)
	}

	byDist := make(map[string][]models.DebPackageEntry)
	for _, e := range entries {
		byDist[e.Distribution] = append(byDist[e.Distribution], e)
	}

	blobs := make(map[string][]byte)

	for _, dist := range distributions {
		if err := w.generateDistribution(ctx, dist, byDist[dist.Name], blobs); err != nil {
			return err
		}
	}

	// Existing generated metadata, so rows/blobs for removed paths can be reclaimed.
	oldSHA, err := listOldMetadata(ctx, tx, repoID)
	if err != nil {
		return err
	}
	newSHA := make(map[string]string, len(blobs))

	for path, data := range blobs {
		checksum := fmt.Sprintf("%x", sha256.Sum256(data))
		newSHA[path] = checksum

		_, err = w.blobStore.Put(ctx, bytes.NewReader(data), map[string]string{
			"path":    path,
			"format":  "deb",
			"repo_id": repoID,
			"sha256":  checksum,
		})
		if err != nil {
			return fmt.Errorf("deb worker: storing blob %s: %w", path, err)
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO repository_metadata (id, repository_id, path, blob_sha256, blob_size, last_generated)
			VALUES (gen_random_uuid(), $1, $2, $3, $4, now())
			ON CONFLICT (repository_id, path) DO UPDATE
			SET blob_sha256 = $3, blob_size = $4, last_generated = now()
		`, repoID, path, checksum, int64(len(data)))
		if err != nil {
			return fmt.Errorf("deb worker: updating repo_metadata for %s: %w", path, err)
		}
	}

	// Drop rows for paths that are no longer generated (removed dists/comps/archs).
	for path := range oldSHA {
		if _, ok := newSHA[path]; ok {
			continue
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM repository_metadata WHERE repository_id = $1 AND path = $2`, repoID, path); err != nil {
			return fmt.Errorf("deb worker: deleting stale repo_metadata %s: %w", path, err)
		}
	}

	// Superseded blobs: old checksums no longer referenced by any metadata row.
	var reclaim []string

	seen := make(map[string]bool)

	for path, old := range oldSHA {
		if old == "" || seen[old] || newSHA[path] == old {
			continue
		}

		seen[old] = true

		var refs int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM repository_metadata WHERE blob_sha256 = $1`, old).Scan(&refs); err != nil {
			return fmt.Errorf("deb worker: counting metadata refs: %w", err)
		}

		if refs == 0 {
			reclaim = append(reclaim, old)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("deb worker: commit: %w", err)
	}

	for _, sum := range reclaim {
		if err := w.blobStore.Delete(ctx, sum); err != nil {
			w.log.Warnw("deb worker: failed to delete superseded metadata blob", "sha256", sum, "error", err)
		}
	}

	w.log.Infow("deb metadata regeneration complete", "repo_id", repoID, "distributions", len(distributions))

	return nil
}

// generateDistribution renders Packages{,.gz,.xz}, Release, InRelease and
// Release.gpg for one distribution into blobs, keyed by full "dists/..." path.
// Every component x architecture pair gets an index (empty when no packages
// remain) so stale listings can never be served after deletions.
func (w *DebMetadataWorker) generateDistribution(ctx context.Context, dist models.DebianDistribution, entries []models.DebPackageEntry, blobs map[string][]byte) error {
	components, err := w.db.GetDebianComponentsByDist(ctx, dist.ID)
	if err != nil {
		return fmt.Errorf("deb worker: querying components for dist %s: %w", dist.Name, err)
	}

	architectures, err := w.db.GetDebianArchitecturesByDist(ctx, dist.ID)
	if err != nil {
		return fmt.Errorf("deb worker: querying architectures for dist %s: %w", dist.Name, err)
	}

	compNames := make([]string, 0, len(components))
	for _, c := range components {
		compNames = append(compNames, c.Name)
	}

	sort.Strings(compNames)

	// Architecture "all" packages are listed in every real architecture's
	// index; a dist that only has "all" keeps a binary-all index.
	var archNames []string

	for _, a := range architectures {
		if a.Name != "all" {
			archNames = append(archNames, a.Name)
		}
	}

	if len(archNames) == 0 && len(architectures) > 0 {
		archNames = []string{"all"}
	}

	sort.Strings(archNames)

	releaseGen := formatdeb.NewReleaseFileGenerator("openartifact", "openartifact", dist.Name, dist.Name)
	for _, c := range compNames {
		releaseGen.AddComponent(c)
	}

	for _, a := range archNames {
		releaseGen.AddArchitecture(a)
	}

	fileInfos := make([]formatdeb.FileInfo, 0, len(compNames)*len(archNames)*3)

	addFile := func(rel string, data []byte) {
		blobs["dists/"+dist.Name+"/"+rel] = data

		md5sum, sha1sum, sha256sum := formatdeb.CalculateChecksums(data)
		fileInfos = append(fileInfos, formatdeb.FileInfo{
			Filename: rel,
			Size:     int64(len(data)),
			MD5sum:   md5sum,
			SHA1:     sha1sum,
			SHA256:   sha256sum,
		})
	}

	for _, comp := range compNames {
		for _, arch := range archNames {
			gen := formatdeb.NewPackagesFileGenerator()

			for _, e := range entries {
				if e.ComponentName != comp || (e.Arch != arch && e.Arch != "all") {
					continue
				}

				gen.AddEntry(formatdeb.FormatPackagesEntry(e))
			}

			base := fmt.Sprintf("%s/binary-%s/Packages", comp, arch)

			addFile(base, []byte(gen.Generate()))

			gzipped, err := gen.GenerateGzipped()
			if err != nil {
				return fmt.Errorf("deb worker: compressing packages for %s/%s/%s: %w", dist.Name, comp, arch, err)
			}

			addFile(base+".gz", gzipped)

			xzipped, err := gen.GenerateXZipped()
			if err != nil {
				return fmt.Errorf("deb worker: xz compressing packages for %s/%s/%s: %w", dist.Name, comp, arch, err)
			}

			addFile(base+".xz", xzipped)
		}
	}

	releaseContent := releaseGen.GenerateRelease(fileInfos)
	blobs["dists/"+dist.Name+"/Release"] = []byte(releaseContent)

	if w.signer != nil {
		signed, err := formatdeb.NewReleaseSigner(w.signer).SignRelease([]byte(releaseContent))
		if err != nil {
			w.log.Warnw("failed to sign release", "dist", dist.Name, "error", err)
		} else {
			blobs["dists/"+dist.Name+"/InRelease"] = signed.InRelease
			blobs["dists/"+dist.Name+"/Release.gpg"] = signed.ReleaseGPG
		}
	}

	return nil
}
