// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"fmt"

	formatstorage "github.com/openartifact/openartifact/internal/format/storage"
)

// unitRowIDs are the row ids of the live rows a unit is made of on this
// instance. The package listing shows them and the UI, the license policies
// and the OSV worker record a scan under them, so they decide whether a unit is
// quarantined next to the path spellings of quarantineKeys, which do not carry
// them. A unit with no live row has none.
func unitRowIDs(ctx context.Context, q Querier, repo *RepoInfo, key string) ([]string, error) {
	query, args := unitRowIDsQuery(repo, key)

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list unit rows: %w", err)
	}
	defer rows.Close()

	var ids []string

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan unit row id: %w", err)
		}

		ids = append(ids, id)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list unit rows: %w", err)
	}

	return ids, nil
}

const (
	// A docker manifest unit: the manifest and the tags that point at it.
	dockerManifestRowsSQL = `SELECT id::text FROM docker_manifests
	  WHERE repository_id = $1::uuid AND image = $2 AND digest = $3 AND deleted_at IS NULL
	 UNION ALL
	 SELECT id::text FROM docker_tags
	  WHERE repository_id = $1::uuid AND image = $2 AND manifest_digest = $3 AND deleted_at IS NULL`
	dockerTagRowsSQL = `SELECT id::text FROM docker_tags
	  WHERE repository_id = $1::uuid AND image = $2 AND name = $3 AND deleted_at IS NULL`
	// A one-file unit: the file keyed by the unit key.
	fileUnitRowsSQL = `SELECT id::text FROM package_files
	  WHERE repository_id = $1::uuid AND unit_key = $2 AND deleted_at IS NULL`
	// A version unit: every live file of the version the keyed file belongs to.
	versionUnitRowsSQL = `SELECT f.id::text FROM package_files f
	  WHERE f.repository_id = $1::uuid AND f.deleted_at IS NULL
	    AND f.version_id = (SELECT k.version_id FROM package_files k
	                         WHERE k.repository_id = $1::uuid AND k.unit_key = $2 AND k.deleted_at IS NULL LIMIT 1)`
)

func unitRowIDsQuery(repo *RepoInfo, key string) (string, []any) {
	if repo.Format == "docker" {
		if image, digest, ok := parseDockerManifestKey(key); ok {
			return dockerManifestRowsSQL, []any{repo.ID, image, digest}
		}

		if image, name, ok := parseDockerTagKey(key); ok {
			return dockerTagRowsSQL, []any{repo.ID, image, name}
		}

		return fileUnitRowsSQL, []any{repo.ID, key} // no such unit: no row either
	}

	if d, ok := formatstorage.Lookup(repo.Format); ok && d.Federation != nil && d.Federation.Unit == formatstorage.UnitFile {
		return fileUnitRowsSQL, []any{repo.ID, key}
	}

	return versionUnitRowsSQL, []any{repo.ID, key}
}
