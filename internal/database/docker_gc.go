// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// Reference kinds recorded in docker_manifest_refs.
const (
	dockerRefBlob     = "blob"
	dockerRefManifest = "manifest"
	// dockerRefIndexed marks a manifest whose references are fully recorded.
	dockerRefIndexed = "indexed"
)

// DockerGCStore is the storage side of Docker registry garbage collection
// (internal/worker.DockerGCWorker). PostgresDB implements it; the worker
// reports GC as unsupported for databases that do not.
//
// Mutations take the repository's exclusive docker lock for the length of one
// transaction. CreateDockerManifest takes the same lock shared, so a push
// either completes before a GC transaction starts (and its references are then
// visible to it) or starts after it ends.
type DockerGCStore interface {
	// ListUnindexedDockerManifests returns up to limit manifests of the
	// repository whose references have not been recorded yet.
	ListUnindexedDockerManifests(ctx context.Context, repoID string, limit int) ([]*models.DockerGCManifest, error)

	// SetDockerManifestRefs records the references of a manifest body and marks
	// the manifest indexed. A manifest deleted meanwhile is skipped silently.
	SetDockerManifestRefs(ctx context.Context, repoID string, m *models.DockerGCManifest, refs models.DockerManifestRefs) error

	// ListDockerGCManifests returns every manifest of the repository with its
	// tagged/young flags (young: pushed within grace) and index children.
	ListDockerGCManifests(ctx context.Context, repoID string, grace time.Duration) ([]*models.DockerGCManifest, error)

	// DeleteDockerManifestsForGC deletes the given manifests (by row id) that
	// are still older than grace, untagged and not adjacent to a manifest
	// outside ids, and returns the ones actually deleted.
	DeleteDockerManifestsForGC(ctx context.Context, repoID string, ids []string, grace time.Duration) ([]*models.DockerGCManifest, error)

	// ListDockerGCBlobs returns up to limit blob records of the repository that
	// no manifest other than excludeManifestIDs references and that have not
	// been used within grace.
	ListDockerGCBlobs(ctx context.Context, repoID string, grace time.Duration, excludeManifestIDs []string, limit int) ([]*models.DockerGCBlob, error)

	// DeleteDockerBlobsForGC deletes the given blob records (by digest) that are
	// still unreferenced and unused within grace, and returns the ones actually
	// deleted. The caller removes the underlying blobs with storage.SafeDeleteBlob.
	DeleteDockerBlobsForGC(ctx context.Context, repoID string, digests []string, grace time.Duration) ([]*models.DockerGCBlob, error)

	// BlobRowExists reports whether the global blobs table still has sha256.
	BlobRowExists(ctx context.Context, sha256 string) (bool, error)
}

var _ DockerGCStore = (*PostgresDB)(nil)

// dockerRepoLockKey is the advisory-lock key serialising registry GC
// mutations of one repository against manifest pushes.
func dockerRepoLockKey(repoID string) string { return "docker-repo:" + repoID }

// beginDockerGCTx starts a transaction holding the repository's exclusive
// docker lock until it ends.
func (p *PostgresDB) beginDockerGCTx(ctx context.Context, repoID string) (pgx.Tx, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin docker gc tx: %w", err)
	}

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, dockerRepoLockKey(repoID)); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))

		return nil, fmt.Errorf("lock docker repository: %w", err)
	}

	return tx, nil
}

func insertDockerManifestRefs(ctx context.Context, q *dbsqlc.Queries, manifestID, repoID, image string, refs *models.DockerManifestRefs) error {
	for _, set := range []struct {
		kind    string
		digests []string
	}{
		{dockerRefBlob, refs.Blobs},
		{dockerRefManifest, refs.Manifests},
		{dockerRefIndexed, []string{""}},
	} {
		if len(set.digests) == 0 {
			continue
		}

		if err := q.InsertDockerManifestRefs(ctx, dbsqlc.InsertDockerManifestRefsParams{
			ManifestID:   manifestID,
			RepositoryID: repoID,
			Image:        image,
			Kind:         set.kind,
			Digests:      set.digests,
		}); err != nil {
			return fmt.Errorf("record docker manifest %s refs: %w", set.kind, err)
		}
	}

	return nil
}

func (p *PostgresDB) ListUnindexedDockerManifests(ctx context.Context, repoID string, limit int) ([]*models.DockerGCManifest, error) {
	rows, err := p.q.ListUnindexedDockerManifests(ctx, dbsqlc.ListUnindexedDockerManifestsParams{
		RepositoryID: repoID,
		Limit:        toInt32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list unindexed docker manifests: %w", err)
	}

	out := make([]*models.DockerGCManifest, len(rows))
	for i, r := range rows {
		out[i] = &models.DockerGCManifest{ID: r.ID, Image: r.Image, Digest: r.Digest, BlobSHA256: r.BlobSha256}
	}

	return out, nil
}

func (p *PostgresDB) SetDockerManifestRefs(ctx context.Context, repoID string, m *models.DockerGCManifest, refs models.DockerManifestRefs) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin docker refs tx: %w", err)
	}

	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := insertDockerManifestRefs(ctx, p.q.WithTx(tx), m.ID, repoID, m.Image, &refs); err != nil {
		if IsForeignKeyViolation(err) {
			return nil // manifest deleted since it was listed
		}

		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit docker refs: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListDockerGCManifests(ctx context.Context, repoID string, grace time.Duration) ([]*models.DockerGCManifest, error) {
	rows, err := p.q.ListDockerGCManifests(ctx, dbsqlc.ListDockerGCManifestsParams{
		RepositoryID: repoID,
		GraceSeconds: grace.Seconds(),
	})
	if err != nil {
		return nil, fmt.Errorf("list docker gc manifests: %w", err)
	}

	out := make([]*models.DockerGCManifest, len(rows))
	for i, r := range rows {
		m := &models.DockerGCManifest{
			ID:            r.ID,
			Image:         r.Image,
			Digest:        r.Digest,
			SubjectDigest: r.SubjectDigest,
			Size:          r.Size,
			BlobSHA256:    r.BlobSha256,
			Tagged:        r.Tagged,
			Young:         r.Young,
		}
		if r.Children != "" {
			m.Children = strings.Split(r.Children, ",")
		}

		out[i] = m
	}

	return out, nil
}

func (p *PostgresDB) DeleteDockerManifestsForGC(ctx context.Context, repoID string, ids []string, grace time.Duration) ([]*models.DockerGCManifest, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	tx, err := p.beginDockerGCTx(ctx, repoID)
	if err != nil {
		return nil, err
	}

	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	rows, err := p.q.WithTx(tx).DeleteDockerManifestsForGC(ctx, dbsqlc.DeleteDockerManifestsForGCParams{
		RepositoryID: repoID,
		Ids:          ids,
		GraceSeconds: grace.Seconds(),
	})
	if err != nil {
		return nil, fmt.Errorf("delete docker manifests for gc: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit docker manifest gc: %w", err)
	}

	out := make([]*models.DockerGCManifest, len(rows))
	for i, r := range rows {
		out[i] = &models.DockerGCManifest{ID: r.ID, Image: r.Image, Digest: r.Digest, Size: r.Size, BlobSHA256: r.BlobSha256}
	}

	return out, nil
}

func (p *PostgresDB) ListDockerGCBlobs(ctx context.Context, repoID string, grace time.Duration, excludeManifestIDs []string, limit int) ([]*models.DockerGCBlob, error) {
	if excludeManifestIDs == nil {
		excludeManifestIDs = []string{}
	}

	rows, err := p.q.ListDockerGCBlobs(ctx, dbsqlc.ListDockerGCBlobsParams{
		RepositoryID:       repoID,
		GraceSeconds:       grace.Seconds(),
		ExcludeManifestIds: excludeManifestIDs,
		RowLimit:           toInt32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list docker gc blobs: %w", err)
	}

	out := make([]*models.DockerGCBlob, len(rows))
	for i, r := range rows {
		out[i] = &models.DockerGCBlob{Digest: r.Digest, BlobSHA256: r.BlobSha256, Size: r.Size}
	}

	return out, nil
}

func (p *PostgresDB) DeleteDockerBlobsForGC(ctx context.Context, repoID string, digests []string, grace time.Duration) ([]*models.DockerGCBlob, error) {
	if len(digests) == 0 {
		return nil, nil
	}

	tx, err := p.beginDockerGCTx(ctx, repoID)
	if err != nil {
		return nil, err
	}

	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	rows, err := p.q.WithTx(tx).DeleteDockerBlobsForGC(ctx, dbsqlc.DeleteDockerBlobsForGCParams{
		RepositoryID: repoID,
		Digests:      digests,
		GraceSeconds: grace.Seconds(),
	})
	if err != nil {
		return nil, fmt.Errorf("delete docker blobs for gc: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit docker blob gc: %w", err)
	}

	out := make([]*models.DockerGCBlob, len(rows))
	for i, r := range rows {
		out[i] = &models.DockerGCBlob{Digest: r.Digest, BlobSHA256: r.BlobSha256, Size: r.Size}
	}

	return out, nil
}

func (p *PostgresDB) BlobRowExists(ctx context.Context, sha256 string) (bool, error) {
	exists, err := p.q.BlobRowExists(ctx, sha256)
	if err != nil {
		return false, fmt.Errorf("check blob row: %w", err)
	}

	return exists, nil
}

// DockerBlobToucher is implemented by databases that track when a registry
// blob was last used, so registry GC keeps recently used blobs.
type DockerBlobToucher interface {
	// TouchDockerBlob records a use of the blob and reports false when the
	// repository no longer has it.
	TouchDockerBlob(ctx context.Context, repoID, digest string) (bool, error)
}

var _ DockerBlobToucher = (*PostgresDB)(nil)
