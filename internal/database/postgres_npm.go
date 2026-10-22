// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// fillNPMBlobSHA256 populates BlobSHA256 (the blob store key) and BlobSizeBytes
// on versions.
// npm_versions.blob_id is a blobs.id UUID, which is not a valid storage key.
func (p *PostgresDB) fillNPMBlobSHA256(ctx context.Context, packageID string, versions ...*models.NPMVersion) error {
	if len(versions) == 0 {
		return nil
	}

	// A single version needs only its own blob; the package-wide join is for
	// callers that resolve every version at once.
	query, arg := `SELECT v.id, b.sha256, b.size_bytes FROM npm_versions v JOIN blobs b ON b.id = v.blob_id WHERE v.package_id = $1 AND v.deleted_at IS NULL`, packageID
	if len(versions) == 1 {
		query, arg = `SELECT v.id, b.sha256, b.size_bytes FROM npm_versions v JOIN blobs b ON b.id = v.blob_id WHERE v.id = $1 AND v.deleted_at IS NULL`, versions[0].ID
	}

	rows, err := p.db.QueryContext(ctx, query, arg)
	if err != nil {
		return fmt.Errorf("failed to resolve npm blob sha256: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type blobRef struct {
		sha  string
		size int64
	}

	refByID := make(map[string]blobRef)

	for rows.Next() {
		var (
			id  string
			ref blobRef
		)

		if err := rows.Scan(&id, &ref.sha, &ref.size); err != nil {
			return fmt.Errorf("failed to scan npm blob sha256: %w", err)
		}

		refByID[id] = ref
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate npm blob sha256: %w", err)
	}

	for _, v := range versions {
		ref := refByID[v.ID]
		v.BlobSHA256, v.BlobSizeBytes = ref.sha, ref.size
	}

	return nil
}
