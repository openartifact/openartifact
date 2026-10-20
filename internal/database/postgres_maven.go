// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// storeMavenChecksums persists the md5/sha1/sha512 digests of an artifact
// (sha256 lives on the joined blob row).
func (p *PostgresDB) storeMavenChecksums(ctx context.Context, a *models.MavenArtifact) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE maven_artifacts SET md5 = $2, sha1 = $3, sha512 = $4 WHERE id = $1`,
		a.ID, a.MD5, a.SHA1, a.SHA512)
	if err != nil {
		return fmt.Errorf("failed to store maven checksums: %w", err)
	}

	return nil
}

// fillMavenChecksums populates SHA256 (from the joined blob) and MD5/SHA1/
// SHA512 for the given artifacts in a single query.
func (p *PostgresDB) fillMavenChecksums(ctx context.Context, arts ...*models.MavenArtifact) error {
	if len(arts) == 0 {
		return nil
	}

	byID := make(map[string]*models.MavenArtifact, len(arts))
	ids := make([]string, 0, len(arts))

	for _, a := range arts {
		byID[a.ID] = a

		ids = append(ids, a.ID)
	}

	rows, err := p.pool.Query(ctx,
		`SELECT ma.id::text, b.sha256, ma.md5, ma.sha1, ma.sha512
		   FROM maven_artifacts ma JOIN blobs b ON b.id = ma.blob_id
		  WHERE ma.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return fmt.Errorf("failed to load maven checksums: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, sha256, md5, sha1, sha512 string
		if err := rows.Scan(&id, &sha256, &md5, &sha1, &sha512); err != nil {
			return fmt.Errorf("failed to scan maven checksums: %w", err)
		}

		if a := byID[id]; a != nil {
			a.SHA256, a.MD5, a.SHA1, a.SHA512 = sha256, md5, sha1, sha512
		}
	}

	return rows.Err()
}
