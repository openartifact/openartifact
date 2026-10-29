// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

const ansibleMediaType = "application/gzip"

func ansibleFromRow(row dbsqlc.ListAnsibleCollectionVersionsRow) (*models.AnsibleCollection, error) {
	deps := map[string]string{}

	if len(row.Dependencies) > 0 {
		if err := json.Unmarshal(row.Dependencies, &deps); err != nil {
			return nil, fmt.Errorf("decode ansible collection dependencies: %w", err)
		}
	}

	return &models.AnsibleCollection{
		ID:              row.ID,
		RepositoryID:    row.RepositoryID,
		Namespace:       row.Namespace,
		Name:            row.Name,
		Version:         row.Version,
		Filename:        row.Filename,
		BlobKey:         row.BlobKey,
		SizeBytes:       row.SizeBytes,
		SHA256:          row.Sha256,
		Dependencies:    deps,
		Metadata:        row.Metadata,
		RequiresAnsible: row.RequiresAnsible,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}, nil
}

// CreateAnsibleCollection stores one collection version. A live row with the
// same namespace, name and version is a unique violation.
func (p *PostgresDB) CreateAnsibleCollection(ctx context.Context, c *models.AnsibleCollection) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, c.SHA256, c.BlobKey, ansibleMediaType, c.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for ansible collection: %w", err)
	}

	deps, err := json.Marshal(nonNilMap(c.Dependencies))
	if err != nil {
		return fmt.Errorf("encode ansible collection dependencies: %w", err)
	}

	meta := c.Metadata
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}

	row, err := p.q.CreateAnsibleCollection(ctx, dbsqlc.CreateAnsibleCollectionParams{
		RepositoryID:    c.RepositoryID,
		Namespace:       c.Namespace,
		Name:            c.Name,
		Version:         c.Version,
		Filename:        c.Filename,
		BlobID:          blobID,
		Dependencies:    deps,
		Metadata:        meta,
		RequiresAnsible: c.RequiresAnsible,
		CreatedAt:       createdAtOrNil(c.CreatedAt),
	})

	return finishCreate(insertedRow(row), err, "create ansible collection", &c.ID, &c.CreatedAt, &c.UpdatedAt)
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}

	return m
}

// GetAnsibleCollection returns the live version of a collection, or (nil, nil).
func (p *PostgresDB) GetAnsibleCollection(ctx context.Context, repoID, namespace, name, version string) (*models.AnsibleCollection, error) {
	row, err := p.q.GetAnsibleCollection(ctx, dbsqlc.GetAnsibleCollectionParams{
		RepositoryID: repoID, Namespace: namespace, Name: name, Version: version,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get ansible collection: %w", err)
	}

	return ansibleFromRow(dbsqlc.ListAnsibleCollectionVersionsRow(row))
}

// GetAnsibleCollectionByID returns the live collection version of the
// repository with the ID, or (nil, nil).
func (p *PostgresDB) GetAnsibleCollectionByID(ctx context.Context, repoID, id string) (*models.AnsibleCollection, error) {
	row, err := p.q.GetAnsibleCollectionByID(ctx, dbsqlc.GetAnsibleCollectionByIDParams{RepositoryID: repoID, ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get ansible collection by id: %w", err)
	}

	return ansibleFromRow(dbsqlc.ListAnsibleCollectionVersionsRow(row))
}

// ListAnsibleCollectionVersions returns every live version of one collection.
func (p *PostgresDB) ListAnsibleCollectionVersions(
	ctx context.Context,
	repoID, namespace, name string,
) ([]*models.AnsibleCollection, error) {
	rows, err := p.q.ListAnsibleCollectionVersions(ctx, dbsqlc.ListAnsibleCollectionVersionsParams{
		RepositoryID: repoID, Namespace: namespace, Name: name,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list ansible collection versions: %w", err)
	}

	out := make([]*models.AnsibleCollection, len(rows))

	for i, row := range rows {
		if out[i], err = ansibleFromRow(row); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// ListAnsibleCollections returns every live version of every collection of the repository.
func (p *PostgresDB) ListAnsibleCollections(ctx context.Context, repoID string) ([]*models.AnsibleCollection, error) {
	rows, err := p.q.ListAnsibleCollections(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list ansible collections: %w", err)
	}

	out := make([]*models.AnsibleCollection, len(rows))

	for i, row := range rows {
		if out[i], err = ansibleFromRow(dbsqlc.ListAnsibleCollectionVersionsRow(row)); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// ListAnsibleCollectionNames returns a page of the collections of the
// repository in name order, and how many collections it has.
func (p *PostgresDB) ListAnsibleCollectionNames(
	ctx context.Context, repoID string, limit, offset int,
) ([]*models.AnsibleCollectionName, int, error) {
	total, err := p.q.CountAnsibleCollectionNames(ctx, repoID)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count ansible collections: %w", err)
	}

	rows, err := p.q.ListAnsibleCollectionNames(ctx, dbsqlc.ListAnsibleCollectionNamesParams{
		RepositoryID: repoID, Limit: int32(limit), Offset: int32(offset), //nolint:gosec // bounded by the caller's page size
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list ansible collections: %w", err)
	}

	out := make([]*models.AnsibleCollectionName, len(rows))
	for i, row := range rows {
		out[i] = &models.AnsibleCollectionName{
			Namespace: row.Namespace, Name: row.Name, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}
	}

	return out, int(total), nil
}

// DeleteAnsibleCollection removes a live collection version row.
func (p *PostgresDB) DeleteAnsibleCollection(ctx context.Context, repoID, id string) error {
	if err := p.q.DeleteAnsibleCollection(ctx, dbsqlc.DeleteAnsibleCollectionParams{ID: id, RepositoryID: repoID}); err != nil {
		return fmt.Errorf("failed to delete ansible collection: %w", err)
	}

	return nil
}
