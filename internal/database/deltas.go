// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// DeltaStore owns package deltas.
type DeltaStore struct{ *core }

func (p *DeltaStore) CreatePackageDelta(ctx context.Context, d *models.PackageDelta) error {
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO package_deltas
			(id, repository_id, from_sha256, from_filename, to_sha256, to_filename, delta_sha256, delta_size, format)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (repository_id, from_sha256, to_sha256) DO NOTHING`,
		d.RepositoryID, d.FromSHA256, d.FromFilename, d.ToSHA256, d.ToFilename,
		d.DeltaSHA256, d.DeltaSize, d.Format,
	)
	if err != nil {
		return fmt.Errorf("create package delta: %w", err)
	}

	return nil
}

func (p *DeltaStore) GetPackageDelta(ctx context.Context, repoID, fromSHA256, toSHA256 string) (*models.PackageDelta, error) {
	var d models.PackageDelta

	err := p.db.QueryRowContext(ctx, `
		SELECT id, repository_id, from_sha256, from_filename, to_sha256, to_filename,
		       delta_sha256, delta_size, format, created_at
		FROM package_deltas
		WHERE repository_id = $1 AND from_sha256 = $2 AND to_sha256 = $3`,
		repoID, fromSHA256, toSHA256,
	).Scan(&d.ID, &d.RepositoryID, &d.FromSHA256, &d.FromFilename, &d.ToSHA256, &d.ToFilename,
		&d.DeltaSHA256, &d.DeltaSize, &d.Format, &d.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
		}

		return nil, fmt.Errorf("get package delta: %w", err)
	}

	return &d, nil
}

// ListPackageDeltas returns the deltas that target toSHA256. A delta whose
// source package is in the trash is hidden (it would reveal the trashed file's
// sha256 and name) and reappears when the source is restored; a source that
// still has a live row (same blob in another row) keeps its deltas visible.
func (p *DeltaStore) ListPackageDeltas(ctx context.Context, repoID, toSHA256 string) ([]*models.PackageDelta, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT d.id, d.repository_id, d.from_sha256, d.from_filename, d.to_sha256, d.to_filename,
		       d.delta_sha256, d.delta_size, d.format, d.created_at
		FROM package_deltas d
		WHERE d.repository_id = $1 AND d.to_sha256 = $2
		  AND (
		    EXISTS (SELECT 1 FROM rpm_packages s JOIN blobs b ON b.id = s.blob_id
		            WHERE s.repository_id = d.repository_id AND b.sha256 = d.from_sha256 AND s.deleted_at IS NULL)
		    OR EXISTS (SELECT 1 FROM deb_packages s JOIN blobs b ON b.id = s.blob_id
		               WHERE s.repository_id = d.repository_id AND b.sha256 = d.from_sha256 AND s.deleted_at IS NULL)
		    OR NOT (
		      EXISTS (SELECT 1 FROM rpm_packages s JOIN blobs b ON b.id = s.blob_id
		              WHERE s.repository_id = d.repository_id AND b.sha256 = d.from_sha256 AND s.deleted_at IS NOT NULL)
		      OR EXISTS (SELECT 1 FROM deb_packages s JOIN blobs b ON b.id = s.blob_id
		                 WHERE s.repository_id = d.repository_id AND b.sha256 = d.from_sha256 AND s.deleted_at IS NOT NULL)
		    )
		  )
		ORDER BY d.created_at DESC`,
		repoID, toSHA256,
	)
	if err != nil {
		return nil, fmt.Errorf("list package deltas: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var deltas []*models.PackageDelta

	for rows.Next() {
		var d models.PackageDelta
		if err := rows.Scan(&d.ID, &d.RepositoryID, &d.FromSHA256, &d.FromFilename, &d.ToSHA256, &d.ToFilename,
			&d.DeltaSHA256, &d.DeltaSize, &d.Format, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("list package deltas: scan: %w", err)
		}

		deltas = append(deltas, &d)
	}

	return deltas, rows.Err()
}

// PruneRepoDeltas deletes the oldest package_deltas rows of a repository until
// at most maxCount rows totalling at most maxBytes remain, and returns the
// delta blob sha256s of the deleted rows (callers release them through
// storage.SafeDeleteBlob once the rows are gone).
func (p *DeltaStore) PruneRepoDeltas(ctx context.Context, repoID string, maxCount int, maxBytes int64) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `
		WITH ranked AS (
			SELECT id,
			       row_number() OVER w AS rn,
			       sum(delta_size) OVER w AS cum
			FROM package_deltas
			WHERE repository_id = $1
			WINDOW w AS (ORDER BY created_at DESC, id)
		)
		DELETE FROM package_deltas d
		USING ranked r
		WHERE d.id = r.id AND (r.rn > $2 OR r.cum > $3)
		RETURNING d.delta_sha256`,
		repoID, maxCount, maxBytes,
	)
	if err != nil {
		return nil, fmt.Errorf("prune package deltas: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var shas []string

	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, fmt.Errorf("prune package deltas: scan: %w", err)
		}

		shas = append(shas, sha)
	}

	return shas, rows.Err()
}
