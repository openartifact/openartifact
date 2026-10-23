// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"time"

	sq "github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/logger"
)

// listOldMetadata returns path -> blob sha256 of the generated dists/ metadata rows.
func listOldMetadata(ctx context.Context, tx sq.Transaction, repoID string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT path, blob_sha256 FROM repository_metadata WHERE repository_id = $1 AND (path LIKE 'dists/%' OR path = 'public.key')`, repoID)
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
	signers   gpg.Resolver
	log       *logger.Logger
	q         *regenQueue[string]
	onStale   func(repoID string)
}

func NewDebMetadataWorker(db sq.Database, blobStore storage.BlobStore, signers gpg.Resolver, log *logger.Logger) *DebMetadataWorker {
	w := &DebMetadataWorker{db: db, blobStore: blobStore, signers: signers, log: log}
	w.q = newRegenQueue("deb", log, w.regenerate)

	return w
}

func (w *DebMetadataWorker) WithDebounce(d time.Duration) *DebMetadataWorker {
	w.q.setDebounce(d)
	return w
}

// WithMetrics records regeneration counts and durations in reg.
func (w *DebMetadataWorker) WithMetrics(reg *metrics.Registry) *DebMetadataWorker {
	w.q.setMetrics(reg)
	return w
}

// WithRegenLimiter shares a concurrency limiter with the other metadata workers.
func (w *DebMetadataWorker) WithRegenLimiter(l *RegenLimiter) *DebMetadataWorker {
	w.q.setLimiter(l)
	return w
}

// signerFor returns the signer of the repository's metadata, nil when it is
// unsigned. A resolution failure aborts the regeneration: replacing signed
// metadata with unsigned metadata because of a transient error would break
// every client that verifies it.
func (w *DebMetadataWorker) signerFor(ctx context.Context, repoID string) (gpg.Signer, error) {
	if w.signers == nil {
		return nil, nil //nolint:nilnil // nil signer means unsigned
	}

	signer, err := w.signers.SignerForRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("deb worker: resolving signing key: %w", err)
	}

	return signer, nil
}

// WithStaleHook calls fn with the repository id on every NotifyStale: the group
// repositories that list it as a member regenerate their merged metadata.
func (w *DebMetadataWorker) WithStaleHook(fn func(repoID string)) *DebMetadataWorker {
	w.onStale = fn
	return w
}

// NotifyStale schedules a coalesced regeneration of the repository's dists/ metadata.
func (w *DebMetadataWorker) NotifyStale(repoID string) {
	w.q.Notify(repoID)

	if w.onStale != nil {
		w.onStale(repoID)
	}
}

// blobs returns the shared blob bookkeeping (see metadataBlobs) for this worker.
func (w *DebMetadataWorker) blobs() metadataBlobs {
	return metadataBlobs{db: w.db, store: w.blobStore, log: w.log, who: "deb worker"}
}

// Stop drains pending regenerations and waits for running ones (bounded).
func (w *DebMetadataWorker) Stop() {
	w.q.Stop()
}

func (w *DebMetadataWorker) regenerate(ctx context.Context, repoID string) error {
	if sqlDB := w.db.GetDB(); sqlDB != nil {
		release, err := lockForRegeneration(ctx, sqlDB, "deb:"+repoID)
		if err != nil {
			return fmt.Errorf("deb worker: repository %s: %w", repoID, err)
		}

		defer release()
	}

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

	signer, err := w.signerFor(ctx, repoID)
	if err != nil {
		return err
	}

	blobs := make(map[string][]byte)

	for _, dist := range distributions {
		if err := w.generateDistribution(ctx, dist, byDist[dist.Name], signer, blobs); err != nil {
			return err
		}
	}

	// APT clients fetch the key the metadata is signed with from the same
	// place as RPM clients: HOST/repos/REPO/public.key.
	if signer != nil {
		publicKey, keyErr := signer.PublicKey()
		if keyErr != nil {
			return fmt.Errorf("deb worker: exporting public key: %w", keyErr)
		}

		blobs["public.key"] = publicKey
	}

	// Existing generated metadata, so rows/blobs for removed paths can be reclaimed.
	oldSHA, err := listOldMetadata(ctx, tx, repoID)
	if err != nil {
		return err
	}
	newSHA := make(map[string]string, len(blobs))
	changed := make(map[string][]byte, len(blobs))

	for path, data := range blobs {
		checksum := fmt.Sprintf("%x", sha256.Sum256(data))
		newSHA[path] = checksum

		// Identical to what the row already points at: nothing to store or
		// write, and the blob is protected by that row from reclaim.
		if oldSHA[path] == checksum {
			continue
		}

		changed[path] = data

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
	stalePaths, reclaim := supersededMetadata(oldSHA, newSHA)

	for _, path := range stalePaths {
		if _, err := tx.ExecContext(ctx, `DELETE FROM repository_metadata WHERE repository_id = $1 AND path = $2`, repoID, path); err != nil {
			return fmt.Errorf("deb worker: deleting stale repo_metadata %s: %w", path, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("deb worker: commit: %w", err)
	}

	// The new set is durable: make sure a concurrent reclaim of an identical
	// blob (another repo's superseded row) did not remove one of its objects,
	// then drop the blobs nothing references any more.
	w.blobs().ensure(ctx, repoID, changed, newSHA, func(path, sum string) map[string]string {
		return map[string]string{
			"path":    path,
			"format":  "deb",
			"repo_id": repoID,
			"sha256":  sum,
		}
	})
	w.blobs().reclaim(ctx, reclaim)

	w.log.Infow("deb metadata regeneration complete", "repo_id", repoID, "distributions", len(distributions))

	return nil
}

// generateDistribution renders Packages{,.gz,.xz}, Release, InRelease and
// Release.gpg for one distribution into blobs, keyed by full "dists/..." path.
// Every component x architecture pair gets an index (empty when no packages
// remain) so stale listings can never be served after deletions.
func (w *DebMetadataWorker) generateDistribution(ctx context.Context, dist models.DebianDistribution, entries []models.DebPackageEntry, signer gpg.Signer, blobs map[string][]byte) error {
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

	if signer != nil {
		signed, err := formatdeb.NewReleaseSigner(signer).SignRelease([]byte(releaseContent))
		if err != nil {
			w.log.Warnw("failed to sign release", "dist", dist.Name, "error", err)
		} else {
			blobs["dists/"+dist.Name+"/InRelease"] = signed.InRelease
			blobs["dists/"+dist.Name+"/Release.gpg"] = signed.ReleaseGPG
		}
	}

	return nil
}
