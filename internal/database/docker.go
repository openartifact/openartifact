// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// Docker blob methods

// CreateDockerBlob upserts the blob's content into the global blobs table
// (docker_blobs.blob_id FKs into blobs, which holds the media type, size
// and sha256) and inserts the per-repo
// docker_blobs registry row referencing it.
func (p *PostgresDB) CreateDockerBlob(ctx context.Context, blob *models.DockerBlob) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, blob.BlobSHA256, blob.BlobSHA256, blob.MediaType, blob.Size)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for docker blob: %w", err)
	}

	row, err := p.q.CreateDockerBlob(ctx, dbsqlc.CreateDockerBlobParams{
		RepositoryID: blob.RepositoryID,
		Digest:       blob.Digest,
		BlobID:       blobID,
	})
	if err != nil {
		return fmt.Errorf("failed to create docker blob: %w", err)
	}

	blob.ID = row.ID
	blob.CreatedAt = row.CreatedAt

	return nil
}

// upsertBlobWithMediaType is like UpsertBlob but also sets media_type,
// needed by docker blobs (layers/configs have a real content type, unlike
// the generic default used elsewhere).
func (p *PostgresDB) upsertBlobWithMediaType(ctx context.Context, sha256, blobKey, mediaType string, sizeBytes int64) (string, error) {
	var blobID string

	err := p.db.QueryRowContext(ctx,
		`INSERT INTO blobs (sha256, size_bytes, media_type, blob_key)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (sha256) DO UPDATE SET media_type = EXCLUDED.media_type
		 RETURNING id`,
		sha256, sizeBytes, mediaType, blobKey,
	).Scan(&blobID)
	if err != nil {
		return "", fmt.Errorf("upsert blob: %w", err)
	}

	return blobID, nil
}

// EnsureBlob ensures the global blobs table has a row for sha256 (creating
// it, or updating its media type, if it already exists) and returns its
// blobs.id. Manifests are stored directly in the blob store without a
// per-repo registry row (unlike docker_blobs), so this is the manifest
// handlers' entry point into upsertBlobWithMediaType.
func (p *PostgresDB) EnsureBlob(ctx context.Context, sha256, mediaType string, size int64) (string, error) {
	blobID, err := p.upsertBlobWithMediaType(ctx, sha256, sha256, mediaType, size)
	if err != nil {
		return "", fmt.Errorf("failed to ensure blob: %w", err)
	}

	return blobID, nil
}

func (p *PostgresDB) GetDockerBlob(ctx context.Context, repoID, digest string) (*models.DockerBlob, error) {
	row, err := p.q.GetDockerBlob(ctx, dbsqlc.GetDockerBlobParams{
		RepositoryID: repoID,
		Digest:       digest,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get docker blob: %w", err)
	}

	return &models.DockerBlob{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Digest:       row.Digest,
		MediaType:    row.MediaType,
		Size:         row.Size,
		BlobSHA256:   row.BlobSha256,
		CreatedAt:    row.CreatedAt,
	}, nil
}

func (p *PostgresDB) DeleteDockerBlob(ctx context.Context, repoID, digest string) error {
	if err := p.q.DeleteDockerBlob(ctx, dbsqlc.DeleteDockerBlobParams{
		RepositoryID: repoID,
		Digest:       digest,
	}); err != nil {
		return fmt.Errorf("failed to delete docker blob: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListDockerBlobs(ctx context.Context, repoID string) ([]*models.DockerBlob, error) {
	rows, err := p.q.ListDockerBlobs(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list docker blobs: %w", err)
	}

	blobs := make([]*models.DockerBlob, len(rows))
	for i, row := range rows {
		blobs[i] = &models.DockerBlob{
			ID:           row.ID,
			RepositoryID: row.RepositoryID,
			Digest:       row.Digest,
			MediaType:    row.MediaType,
			Size:         row.Size,
			BlobSHA256:   row.BlobSha256,
			CreatedAt:    row.CreatedAt,
		}
	}

	return blobs, nil
}

// Docker upload methods

func (p *PostgresDB) CreateDockerUpload(ctx context.Context, upload *models.DockerUpload) error {
	row, err := p.q.CreateDockerUpload(ctx, dbsqlc.CreateDockerUploadParams{
		RepositoryID: upload.RepositoryID,
		UploadUuid:   upload.UploadUUID,
		Offset:       upload.Offset,
	})
	if err != nil {
		return fmt.Errorf("failed to create docker upload: %w", err)
	}

	upload.ID = row.ID
	upload.CreatedAt = row.CreatedAt
	upload.UpdatedAt = row.UpdatedAt

	return nil
}

func (p *PostgresDB) GetDockerUpload(ctx context.Context, repositoryID, uploadUUID string) (*models.DockerUpload, error) {
	row, err := p.q.GetDockerUpload(ctx, dbsqlc.GetDockerUploadParams{
		RepositoryID: repositoryID,
		UploadUuid:   uploadUUID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get docker upload: %w", err)
	}

	return &models.DockerUpload{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		UploadUUID:   row.UploadUuid,
		Offset:       row.Offset,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

func (p *PostgresDB) UpdateDockerUploadOffset(ctx context.Context, repositoryID, uploadUUID string, offset int64) error {
	if err := p.q.UpdateDockerUploadOffset(ctx, dbsqlc.UpdateDockerUploadOffsetParams{
		RepositoryID: repositoryID,
		UploadUuid:   uploadUUID,
		Offset:       offset,
	}); err != nil {
		return fmt.Errorf("failed to update docker upload offset: %w", err)
	}

	return nil
}

func (p *PostgresDB) DeleteDockerUpload(ctx context.Context, repositoryID, uploadUUID string) error {
	if err := p.q.DeleteDockerUpload(ctx, dbsqlc.DeleteDockerUploadParams{
		RepositoryID: repositoryID,
		UploadUuid:   uploadUUID,
	}); err != nil {
		return fmt.Errorf("failed to delete docker upload: %w", err)
	}

	return nil
}

// Docker manifest methods

// CreateDockerManifest upserts the manifest row. When m.Refs is set, the
// references of the manifest body are recorded in the same transaction, under
// the shared per-repository docker lock that registry GC takes exclusively
// before it deletes anything: GC then sees either none of this push or all of
// it, never a manifest whose references it cannot see yet.
func (p *PostgresDB) CreateDockerManifest(ctx context.Context, m *models.DockerManifest) error {
	params := dbsqlc.CreateDockerManifestParams{
		RepositoryID: m.RepositoryID,
		Image:        m.Image,
		Digest:       m.Digest,
		MediaType:    m.MediaType,
		BlobID:       m.BlobID,
		Size:         m.Size,
		Column7:      m.SubjectDigest,
	}

	if m.Refs == nil {
		row, err := p.q.CreateDockerManifest(ctx, params)
		if err != nil {
			return fmt.Errorf("failed to create docker manifest: %w", err)
		}

		m.ID = row.ID
		m.CreatedAt = row.CreatedAt
		m.UpdatedAt = row.UpdatedAt

		return nil
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin docker manifest tx: %w", err)
	}

	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1, 0))`, dockerRepoLockKey(m.RepositoryID)); err != nil {
		return fmt.Errorf("lock docker repository: %w", err)
	}

	q := p.q.WithTx(tx)

	row, err := q.CreateDockerManifest(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to create docker manifest: %w", err)
	}

	if err := insertDockerManifestRefs(ctx, q, row.ID, m.RepositoryID, m.Image, m.Refs); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit docker manifest: %w", err)
	}

	m.ID = row.ID
	m.CreatedAt = row.CreatedAt
	m.UpdatedAt = row.UpdatedAt

	return nil
}

// TouchDockerBlob records that a client has just relied on the blob (HEAD
// probe, re-push, mount), so registry GC keeps it for its grace period. It
// reports false when the repository no longer has the blob.
func (p *PostgresDB) TouchDockerBlob(ctx context.Context, repoID, digest string) (bool, error) {
	_, err := p.q.TouchDockerBlob(ctx, dbsqlc.TouchDockerBlobParams{RepositoryID: repoID, Digest: digest})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("failed to touch docker blob: %w", err)
	}

	return true, nil
}

func (p *PostgresDB) GetDockerManifest(ctx context.Context, repoID, image, digest string) (*models.DockerManifest, error) {
	row, err := p.q.GetDockerManifest(ctx, dbsqlc.GetDockerManifestParams{
		RepositoryID: repoID,
		Image:        image,
		Digest:       digest,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get docker manifest: %w", err)
	}

	return &models.DockerManifest{
		ID:            row.ID,
		RepositoryID:  row.RepositoryID,
		Image:         row.Image,
		Digest:        row.Digest,
		MediaType:     row.MediaType,
		BlobID:        row.BlobID,
		Size:          row.Size,
		SubjectDigest: row.SubjectDigest,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}, nil
}

// ListDockerManifestsByRepo lists every manifest for a repository, with its
// blob's sha256 resolved via a JOIN (unlike BlobID above, which is the raw,
// restore-incompatible blobs.id UUID). Used only by backup export.
func (p *PostgresDB) ListDockerManifestsByRepo(ctx context.Context, repoID string) ([]*models.DockerManifest, error) {
	rows, err := p.q.ListDockerManifestsByRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list docker manifests by repo: %w", err)
	}

	manifests := make([]*models.DockerManifest, len(rows))
	for i, row := range rows {
		manifests[i] = &models.DockerManifest{
			ID:            row.ID,
			RepositoryID:  row.RepositoryID,
			Image:         row.Image,
			Digest:        row.Digest,
			MediaType:     row.MediaType,
			Size:          row.Size,
			SubjectDigest: row.SubjectDigest,
			BlobSHA256:    row.BlobSha256,
			CreatedAt:     row.CreatedAt,
			UpdatedAt:     row.UpdatedAt,
		}
	}

	return manifests, nil
}

// ListDockerManifestsBySubject returns manifests in repoID whose OCI
// `subject` field points at subjectDigest, for the Referrers API
// (GET /v2/<name>/referrers/<digest>). No sqlc query exists for this
// lookup, so it runs directly against p.db.
func (p *PostgresDB) ListDockerManifestsBySubject(ctx context.Context, repoID, image, subjectDigest string) ([]*models.DockerManifest, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, repository_id, image, digest, media_type, blob_id, size,
		        COALESCE(subject_digest, ''), created_at, updated_at
		 FROM docker_manifests
		 WHERE repository_id = $1 AND image = $2 AND subject_digest = $3
		 ORDER BY created_at`,
		repoID, image, subjectDigest,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list docker manifests by subject: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var manifests []*models.DockerManifest

	for rows.Next() {
		m := &models.DockerManifest{}
		if err := rows.Scan(&m.ID, &m.RepositoryID, &m.Image, &m.Digest, &m.MediaType, &m.BlobID, &m.Size,
			&m.SubjectDigest, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan docker manifest: %w", err)
		}

		manifests = append(manifests, m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list docker manifests by subject: %w", err)
	}

	return manifests, nil
}

func (p *PostgresDB) DeleteDockerManifest(ctx context.Context, repoID, image, digest string) error {
	if err := p.q.DeleteDockerManifest(ctx, dbsqlc.DeleteDockerManifestParams{
		RepositoryID: repoID,
		Image:        image,
		Digest:       digest,
	}); err != nil {
		return fmt.Errorf("failed to delete docker manifest: %w", err)
	}

	return nil
}

// Docker tag methods

func (p *PostgresDB) UpsertDockerTag(ctx context.Context, tag *models.DockerTag) error {
	row, err := p.q.UpsertDockerTag(ctx, dbsqlc.UpsertDockerTagParams{
		RepositoryID:   tag.RepositoryID,
		Image:          tag.Image,
		Name:           tag.Name,
		ManifestDigest: tag.ManifestDigest,
	})
	if err != nil {
		return fmt.Errorf("failed to upsert docker tag: %w", err)
	}

	tag.ID = row.ID
	tag.CreatedAt = row.CreatedAt
	tag.UpdatedAt = row.UpdatedAt

	return nil
}

func (p *PostgresDB) GetDockerTag(ctx context.Context, repoID, image, name string) (*models.DockerTag, error) {
	row, err := p.q.GetDockerTag(ctx, dbsqlc.GetDockerTagParams{
		RepositoryID: repoID,
		Image:        image,
		Name:         name,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get docker tag: %w", err)
	}

	return &models.DockerTag{
		ID:             row.ID,
		RepositoryID:   row.RepositoryID,
		Image:          row.Image,
		Name:           row.Name,
		ManifestDigest: row.ManifestDigest,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}

func (p *PostgresDB) DeleteDockerTag(ctx context.Context, repoID, image, name string) error {
	if err := p.q.DeleteDockerTag(ctx, dbsqlc.DeleteDockerTagParams{
		RepositoryID: repoID,
		Image:        image,
		Name:         name,
	}); err != nil {
		return fmt.Errorf("failed to delete docker tag: %w", err)
	}

	return nil
}

func (p *PostgresDB) DeleteDockerTagsByDigest(ctx context.Context, repoID, image, digest string) error {
	if err := p.q.DeleteDockerTagsByDigest(ctx, dbsqlc.DeleteDockerTagsByDigestParams{
		RepositoryID:   repoID,
		Image:          image,
		ManifestDigest: digest,
	}); err != nil {
		return fmt.Errorf("failed to delete docker tags by digest: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListDockerTags(ctx context.Context, repoID, image, last string, n int) ([]*models.DockerTag, error) {
	if n <= 0 {
		n = 100
	}

	var dbTags []dbsqlc.DockerTag

	if last != "" {
		rows, err := p.q.ListDockerTagsAfter(ctx, dbsqlc.ListDockerTagsAfterParams{
			RepositoryID: repoID,
			Image:        image,
			Name:         last,
			Limit:        toInt32(n),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list docker tags: %w", err)
		}

		dbTags = rows
	} else {
		rows, err := p.q.ListDockerTags(ctx, dbsqlc.ListDockerTagsParams{
			RepositoryID: repoID,
			Image:        image,
			Limit:        toInt32(n),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list docker tags: %w", err)
		}

		dbTags = rows
	}

	return dockerTagsFromRows(dbTags), nil
}

// ListDockerTagsByRepo lists tags across every image of a repository, for
// backup export.
func (p *PostgresDB) ListDockerTagsByRepo(ctx context.Context, repoID string, limit int) ([]*models.DockerTag, error) {
	rows, err := p.q.ListDockerTagsByRepo(ctx, dbsqlc.ListDockerTagsByRepoParams{
		RepositoryID: repoID,
		Limit:        toInt32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list docker tags by repo: %w", err)
	}

	return dockerTagsFromRows(rows), nil
}

func dockerTagsFromRows(rows []dbsqlc.DockerTag) []*models.DockerTag {
	tags := make([]*models.DockerTag, len(rows))
	for i, row := range rows {
		tags[i] = &models.DockerTag{
			ID:             row.ID,
			RepositoryID:   row.RepositoryID,
			Image:          row.Image,
			Name:           row.Name,
			ManifestDigest: row.ManifestDigest,
			CreatedAt:      row.CreatedAt,
			UpdatedAt:      row.UpdatedAt,
		}
	}

	return tags
}
