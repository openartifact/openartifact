// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// fillNPMBlobSHA256 populates BlobSHA256 (the blob store key) on versions.
// npm_versions.blob_id is a blobs.id UUID, which is not a valid storage key.
func (p *PostgresDB) fillNPMBlobSHA256(ctx context.Context, packageID string, versions ...*models.NPMVersion) error {
	if len(versions) == 0 {
		return nil
	}

	// A single version needs only its own blob; the package-wide join is for
	// callers that resolve every version at once.
	query, arg := `SELECT v.id, b.sha256 FROM npm_versions v JOIN blobs b ON b.id = v.blob_id WHERE v.package_id = $1 AND v.deleted_at IS NULL`, packageID
	if len(versions) == 1 {
		query, arg = `SELECT v.id, b.sha256 FROM npm_versions v JOIN blobs b ON b.id = v.blob_id WHERE v.id = $1 AND v.deleted_at IS NULL`, versions[0].ID
	}

	rows, err := p.db.QueryContext(ctx, query, arg)
	if err != nil {
		return fmt.Errorf("failed to resolve npm blob sha256: %w", err)
	}
	defer func() { _ = rows.Close() }()

	shaByID := make(map[string]string)

	for rows.Next() {
		var id, sha string
		if err := rows.Scan(&id, &sha); err != nil {
			return fmt.Errorf("failed to scan npm blob sha256: %w", err)
		}

		shaByID[id] = sha
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate npm blob sha256: %w", err)
	}

	for _, v := range versions {
		v.BlobSHA256 = shaByID[v.ID]
	}

	return nil
}
