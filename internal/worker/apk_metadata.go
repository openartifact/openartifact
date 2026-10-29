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
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	formatapk "github.com/openartifact/openartifact/internal/format/apk"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/workq"
	"github.com/openartifact/openartifact/pkg/apk"
	"github.com/openartifact/openartifact/pkg/logger"
)

// ApkSignerResolver finds the signer of a repository's index. A nil signer
// with no error means the index is served unsigned.
type ApkSignerResolver interface {
	SignerForRepo(ctx context.Context, repo *models.Repository) (*apk.Signer, error)
}

// isApkIndexPath reports whether a repository_metadata path is the index of a
// branch/repo/arch directory.
func isApkIndexPath(path string) bool {
	parts := strings.Split(path, "/")

	return len(parts) == 4 && parts[3] == formatapk.IndexName
}

// ApkMetadataStore is the part of the database ApkMetadataWorker needs.
type ApkMetadataStore interface {
	metadataBlobsStore
	BeginTx(ctx context.Context) (database.Transaction, error)
	GetRepositoryByID(ctx context.Context, id string) (*models.Repository, error)
	ListApkPackages(ctx context.Context, repoID string) ([]*models.ApkPackage, error)
}

// ApkMetadataWorker regenerates the signed APKINDEX.tar.gz of every
// branch/repo/arch directory of hosted apk repositories, through the shared
// coalescing RegenQueue (one running and one pending regeneration per
// repository).
type ApkMetadataWorker struct {
	db        ApkMetadataStore
	blobStore storage.BlobStore
	signers   ApkSignerResolver
	log       *logger.Logger
	q         *regenQueue[string]
	onStale   func(repoID string)
}

// NewApkMetadataWorker builds the worker. signers may be nil (unsigned indexes).
func NewApkMetadataWorker(
	db ApkMetadataStore,
	blobStore storage.BlobStore,
	signers ApkSignerResolver,
	log *logger.Logger,
) *ApkMetadataWorker {
	w := &ApkMetadataWorker{db: db, blobStore: blobStore, signers: signers, log: log}
	w.q = newRegenQueue("apk", log, w.regenerate)
	w.q.setCodec(stringRegenCodec())

	return w
}

// ForwardTo makes NotifyStale write a worker request to s instead of scheduling
// the regeneration here (server.role=web); see RegenRemote.
func (w *ApkMetadataWorker) ForwardTo(s *workq.Store) { w.q.forwardTo(s) }

// ServeFrom makes the worker regenerate what the requests claimed by c ask for.
func (w *ApkMetadataWorker) ServeFrom(c *workq.Consumer) { w.q.serveFrom(c, w.NotifyStale) }

// WithDebounce sets the quiet period a notification waits for.
func (w *ApkMetadataWorker) WithDebounce(d time.Duration) *ApkMetadataWorker {
	w.q.setDebounce(d)
	return w
}

// WithMetrics records regeneration counts and durations in reg.
func (w *ApkMetadataWorker) WithMetrics(reg *metrics.Registry) *ApkMetadataWorker {
	w.q.setMetrics(reg)
	return w
}

// WithDrainTimeout sets how long Stop waits for pending regenerations.
func (w *ApkMetadataWorker) WithDrainTimeout(d time.Duration) *ApkMetadataWorker {
	w.q.setDrainTimeout(d)
	return w
}

// WithRegenLimiter shares a concurrency limiter with the other metadata workers.
func (w *ApkMetadataWorker) WithRegenLimiter(l *RegenLimiter) *ApkMetadataWorker {
	w.q.setLimiter(l)
	return w
}

// WithStaleHook calls fn with the repository id on every NotifyStale: the group
// repositories that list it as a member regenerate their merged index.
func (w *ApkMetadataWorker) WithStaleHook(fn func(repoID string)) *ApkMetadataWorker {
	w.onStale = fn
	return w
}

// NotifyStale schedules a coalesced regeneration of the repository's indexes.
func (w *ApkMetadataWorker) NotifyStale(repoID string) {
	w.q.Notify(repoID)

	if w.onStale != nil {
		w.onStale(repoID)
	}
}

// Stop drains pending regenerations and waits for running ones (bounded).
func (w *ApkMetadataWorker) Stop() { w.q.Stop() }

func (w *ApkMetadataWorker) blobs() metadataBlobs {
	return metadataBlobs{db: w.db, store: w.blobStore, log: w.log, who: "apk worker"}
}

// signerFor resolves the signer of the repository's index. A resolution
// failure aborts the regeneration: replacing a signed index by an unsigned one
// because of a transient error would make every apk client reject it.
func (w *ApkMetadataWorker) signerFor(ctx context.Context, repo *models.Repository) (*apk.Signer, error) {
	if w.signers == nil {
		return nil, nil //nolint:nilnil // nil signer means unsigned
	}

	signer, err := w.signers.SignerForRepo(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("apk worker: resolving signing key: %w", err)
	}

	return signer, nil
}

func (w *ApkMetadataWorker) regenerate(ctx context.Context, repoID string) error {
	if sqlDB := w.db.GetDB(); sqlDB != nil {
		release, err := lockForRegeneration(ctx, sqlDB, "apk:"+repoID)
		if err != nil {
			return fmt.Errorf("apk worker: repository %s: %w", repoID, err)
		}

		defer release()
	}

	repo, err := w.db.GetRepositoryByID(ctx, repoID)
	if err != nil {
		return fmt.Errorf("apk worker: loading repository %s: %w", repoID, err)
	}

	if repo == nil || repo.Format != "apk" {
		return nil
	}

	rows, err := w.db.ListApkPackages(ctx, repoID)
	if err != nil {
		return fmt.Errorf("apk worker: listing packages: %w", err)
	}

	signer, err := w.signerFor(ctx, repo)
	if err != nil {
		return err
	}

	blobs, err := renderApkIndexes(repo.Name, ApkPackageDirs(rows), signer)
	if err != nil {
		return err
	}

	return w.commit(ctx, repoID, blobs, len(rows))
}

// ApkPackageDirs groups the live package rows by directory.
func ApkPackageDirs(rows []*models.ApkPackage) map[string][]apk.Package {
	byDir := make(map[string][]apk.Package)

	for _, row := range rows {
		p := ApkRowPackage(row)
		path := formatapk.IndexPath(row.Branch, row.Repo, row.Arch)
		byDir[path] = append(byDir[path], p)
	}

	return byDir
}

// ApkRowPackage is the index entry of a stored package. Its architecture is the
// one of the directory (what Alpine's `apk index --rewrite-arch` writes): a
// noarch package lives in each arch directory, and apk fetches a package from
// <repo>/<arch of its index entry>/, so an A:noarch entry would send apk to a
// noarch directory that does not exist. The package file itself stays noarch.
func ApkRowPackage(row *models.ApkPackage) apk.Package {
	return apk.Package{
		Name:             row.Name,
		Version:          row.Version,
		Arch:             row.Arch,
		Description:      row.Description,
		URL:              row.URL,
		License:          row.License,
		Origin:           row.Origin,
		Maintainer:       row.Maintainer,
		Commit:           row.Commit,
		BuildTime:        row.BuildTime,
		InstalledSize:    row.InstalledSize,
		Depends:          row.Depends,
		Provides:         row.Provides,
		InstallIf:        row.InstallIf,
		ProviderPriority: row.ProviderPriority,
		Checksum:         row.Checksum,
		Size:             row.SizeBytes,
	}
}

// renderApkIndexes builds the signed index of every directory in byDir, keyed
// by repository_metadata path.
func renderApkIndexes(repoName string, byDir map[string][]apk.Package, signer *apk.Signer) (map[string][]byte, error) {
	blobs := make(map[string][]byte, len(byDir))
	description := "OpenArtifact apk repository " + repoName

	for path, pkgs := range byDir {
		sort.Slice(pkgs, func(i, j int) bool {
			if pkgs[i].Name != pkgs[j].Name {
				return pkgs[i].Name < pkgs[j].Name
			}

			return pkgs[i].Version < pkgs[j].Version
		})

		index, err := apk.BuildIndex(pkgs, description, signer)
		if err != nil {
			return nil, fmt.Errorf("apk worker: building index %s: %w", path, err)
		}

		blobs[path] = index
	}

	return blobs, nil
}

// commit stores blobs as the repository's generated metadata: unchanged
// paths are left alone, paths no package backs any more are re-rendered as
// empty indexes (a client that knew the directory keeps getting a valid,
// empty index instead of a 404).
func (w *ApkMetadataWorker) commit(ctx context.Context, repoID string, blobs map[string][]byte, packages int) error {
	tx, err := w.db.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("apk worker: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	oldSHA, err := listApkMetadata(ctx, tx, repoID)
	if err != nil {
		return err
	}

	if err := w.addEmptyIndexes(ctx, repoID, oldSHA, blobs); err != nil {
		return err
	}

	newSHA := make(map[string]string, len(blobs))
	changed := make(map[string][]byte, len(blobs))

	for path, data := range blobs {
		checksum := fmt.Sprintf("%x", sha256.Sum256(data))
		newSHA[path] = checksum

		if oldSHA[path] == checksum {
			continue
		}

		changed[path] = data

		if _, err := w.blobStore.Put(ctx, bytes.NewReader(data), apkBlobAttrs(repoID, path, checksum)); err != nil {
			return fmt.Errorf("apk worker: storing blob %s: %w", path, err)
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO repository_metadata (id, repository_id, path, blob_sha256, blob_size, last_generated)
			VALUES (gen_random_uuid(), $1, $2, $3, $4, now())
			ON CONFLICT (repository_id, path) DO UPDATE
			SET blob_sha256 = $3, blob_size = $4, last_generated = now()
		`, repoID, path, checksum, int64(len(data)))
		if err != nil {
			return fmt.Errorf("apk worker: updating repo_metadata for %s: %w", path, err)
		}
	}

	stalePaths, reclaim := supersededMetadata(oldSHA, newSHA)

	for _, path := range stalePaths {
		if _, err := tx.ExecContext(
			ctx,
			`DELETE FROM repository_metadata WHERE repository_id = $1 AND path = $2`,
			repoID,
			path,
		); err != nil {
			return fmt.Errorf("apk worker: deleting stale repo_metadata %s: %w", path, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("apk worker: commit: %w", err)
	}

	w.blobs().ensure(ctx, repoID, changed, newSHA, func(path, sum string) map[string]string { return apkBlobAttrs(repoID, path, sum) })
	w.blobs().reclaim(ctx, reclaim)

	w.log.Infow("apk metadata regeneration complete", "repo_id", repoID, "indexes", len(blobs), "packages", packages)

	return nil
}

// addEmptyIndexes adds an empty index to blobs for every directory that had
// one but has no packages left.
func (w *ApkMetadataWorker) addEmptyIndexes(ctx context.Context, repoID string, oldSHA map[string]string, blobs map[string][]byte) error {
	var (
		repo   *models.Repository
		signer *apk.Signer
		loaded bool
	)

	for path := range oldSHA {
		if _, ok := blobs[path]; ok {
			continue
		}

		if !isApkIndexPath(path) {
			continue
		}

		if !loaded {
			var err error

			if repo, err = w.db.GetRepositoryByID(ctx, repoID); err != nil || repo == nil {
				return fmt.Errorf("apk worker: loading repository %s: %w", repoID, err)
			}

			if signer, err = w.signerFor(ctx, repo); err != nil {
				return err
			}

			loaded = true
		}

		index, err := apk.BuildIndex(nil, "OpenArtifact apk repository "+repo.Name, signer)
		if err != nil {
			return fmt.Errorf("apk worker: building empty index %s: %w", path, err)
		}

		blobs[path] = index
	}

	return nil
}

func apkBlobAttrs(repoID, path, sum string) map[string]string {
	return map[string]string{"path": path, "format": "apk", "repo_id": repoID, "sha256": sum}
}

// listApkMetadata returns path -> blob sha256 of the generated index rows.
func listApkMetadata(ctx context.Context, tx database.Transaction, repoID string) (map[string]string, error) {
	return queryMetadataSums(ctx, tx, "apk worker", "repo_metadata",
		`SELECT path, blob_sha256 FROM repository_metadata WHERE repository_id = $1`, repoID)
}
