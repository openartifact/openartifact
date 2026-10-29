// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
)

// PropertyStore owns artifact properties.
type PropertyStore struct{ *core }

// DeleteArtifactProperty deletes an artifact property.
func (p *PropertyStore) DeleteArtifactProperty(ctx context.Context, repoID, artifactID, key string) error {
	err := p.q.DeleteArtifactProperty(ctx, dbsqlc.DeleteArtifactPropertyParams{
		RepositoryID: repoID,
		ArtifactPath: artifactID,
		Key:          key,
	})
	if err != nil {
		return fmt.Errorf("failed to delete artifact property: %w", err)
	}

	return nil
}

// ListArtifactProperties lists all properties for an artifact.
func (p *PropertyStore) ListArtifactProperties(ctx context.Context, repoID, artifactID string) (map[string]string, error) {
	rows, err := p.q.ListArtifactProperties(ctx, dbsqlc.ListArtifactPropertiesParams{
		RepositoryID: repoID,
		ArtifactPath: artifactID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list artifact properties: %w", err)
	}

	result := make(map[string]string, len(rows))

	for _, row := range rows {
		result[row.Key] = row.Value
	}

	return result, nil
}

// SearchByProperty searches for artifacts by property key and value.
func (p *PropertyStore) SearchByProperty(ctx context.Context, repoID, key, value string) ([]string, error) {
	result, err := p.q.SearchByProperty(ctx, dbsqlc.SearchByPropertyParams{
		RepositoryID: repoID,
		Key:          key,
		Value:        value,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search by property: %w", err)
	}

	return result, nil
}

const upsertArtifactPropertySQL = `
	WITH ref AS (
		INSERT INTO artifact_refs (repository_id, artifact_path, format)
		VALUES ($1, $2, COALESCE((SELECT format FROM repositories WHERE id = $1), 'unknown'))
		ON CONFLICT (repository_id, artifact_path) DO UPDATE SET artifact_path = EXCLUDED.artifact_path
		RETURNING id
	), upd AS (
		UPDATE artifact_properties ap
		SET value = $4, updated_at = now()
		FROM ref
		WHERE ap.repository_id = $1 AND ap.artifact_ref_id = ref.id AND ap.key = $3
		RETURNING ap.id
	)
	INSERT INTO artifact_properties (repository_id, artifact_ref_id, key, value)
	SELECT $1, ref.id, $3, $4 FROM ref
	WHERE NOT EXISTS (SELECT 1 FROM upd)`

// SetArtifactProperties upserts all props for an artifact in a single
// transaction. When replace is true the artifact's existing properties are
// deleted first, in the same transaction.
func (p *PropertyStore) SetArtifactProperties(ctx context.Context, repoID, artifactID string, props map[string]string, replace bool) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if replace {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM artifact_properties ap
			USING artifact_refs ar
			WHERE ap.artifact_ref_id = ar.id AND ap.repository_id = $1 AND ar.artifact_path = $2`,
			repoID, artifactID); err != nil {
			return fmt.Errorf("failed to clear artifact properties: %w", err)
		}
	}

	for key, value := range props {
		if _, err := tx.ExecContext(ctx, upsertArtifactPropertySQL, repoID, artifactID, key, value); err != nil {
			return fmt.Errorf("failed to set artifact property %q: %w", key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit artifact properties: %w", err)
	}

	return nil
}
