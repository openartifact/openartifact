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

const composerMediaType = "application/zip"

func composerFromRow(row dbsqlc.ListComposerPackageVersionsRow) *models.ComposerPackage {
	return &models.ComposerPackage{
		ID:                row.ID,
		RepositoryID:      row.RepositoryID,
		Name:              row.Name,
		Version:           row.Version,
		VersionNormalized: row.VersionNormalized,
		BlobKey:           row.BlobKey,
		SizeBytes:         row.SizeBytes,
		SHA256:            row.Sha256,
		SHA1:              row.Sha1,
		ComposerJSON:      row.ComposerJson,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}

// CreateComposerPackage stores one package version. A live row with the same
// name and normalised version is a unique violation.
func (p *PostgresDB) CreateComposerPackage(ctx context.Context, c *models.ComposerPackage) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, c.SHA256, c.BlobKey, composerMediaType, c.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for composer package: %w", err)
	}

	doc := c.ComposerJSON
	if len(doc) == 0 {
		doc = json.RawMessage(`{}`)
	}

	row, err := p.q.CreateComposerPackage(ctx, dbsqlc.CreateComposerPackageParams{
		RepositoryID:      c.RepositoryID,
		Name:              c.Name,
		Version:           c.Version,
		VersionNormalized: c.VersionNormalized,
		BlobID:            blobID,
		Sha1:              c.SHA1,
		ComposerJson:      doc,
		CreatedAt:         createdAtOrNil(c.CreatedAt),
	})
	if err != nil {
		return fmt.Errorf("failed to create composer package: %w", err)
	}

	c.ID = row.ID
	c.CreatedAt = row.CreatedAt
	c.UpdatedAt = row.UpdatedAt

	return nil
}

// GetComposerPackage returns the live version of a package, or (nil, nil).
func (p *PostgresDB) GetComposerPackage(ctx context.Context, repoID, name, versionNormalized string) (*models.ComposerPackage, error) {
	row, err := p.q.GetComposerPackage(ctx, dbsqlc.GetComposerPackageParams{
		RepositoryID: repoID, Name: name, VersionNormalized: versionNormalized,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get composer package: %w", err)
	}

	return composerFromRow(dbsqlc.ListComposerPackageVersionsRow(row)), nil
}

// GetComposerPackageByID returns the live package version of the repository
// with the ID, or (nil, nil).
func (p *PostgresDB) GetComposerPackageByID(ctx context.Context, repoID, id string) (*models.ComposerPackage, error) {
	row, err := p.q.GetComposerPackageByID(ctx, dbsqlc.GetComposerPackageByIDParams{RepositoryID: repoID, ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get composer package by id: %w", err)
	}

	return composerFromRow(dbsqlc.ListComposerPackageVersionsRow(row)), nil
}

// ListComposerPackageVersions returns every live version of one package, oldest upload first.
func (p *PostgresDB) ListComposerPackageVersions(ctx context.Context, repoID, name string) ([]*models.ComposerPackage, error) {
	rows, err := p.q.ListComposerPackageVersions(ctx, dbsqlc.ListComposerPackageVersionsParams{RepositoryID: repoID, Name: name})
	if err != nil {
		return nil, fmt.Errorf("failed to list composer package versions: %w", err)
	}

	out := make([]*models.ComposerPackage, len(rows))
	for i, row := range rows {
		out[i] = composerFromRow(row)
	}

	return out, nil
}

// ListComposerPackages returns every live version of every package of the repository.
func (p *PostgresDB) ListComposerPackages(ctx context.Context, repoID string) ([]*models.ComposerPackage, error) {
	rows, err := p.q.ListComposerPackages(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list composer packages: %w", err)
	}

	out := make([]*models.ComposerPackage, len(rows))
	for i, row := range rows {
		out[i] = composerFromRow(dbsqlc.ListComposerPackageVersionsRow(row))
	}

	return out, nil
}

// ListComposerPackageNames returns every package of the repository in name
// order with the description of its newest live version.
func (p *PostgresDB) ListComposerPackageNames(ctx context.Context, repoID string) ([]*models.ComposerPackageName, error) {
	rows, err := p.q.ListComposerPackageNames(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list composer package names: %w", err)
	}

	out := make([]*models.ComposerPackageName, len(rows))
	for i, row := range rows {
		out[i] = &models.ComposerPackageName{Name: row.Name, Description: row.Description, UpdatedAt: row.UpdatedAt}
	}

	return out, nil
}

// DeleteComposerPackage removes a live package version row.
func (p *PostgresDB) DeleteComposerPackage(ctx context.Context, repoID, id string) error {
	if err := p.q.DeleteComposerPackage(ctx, dbsqlc.DeleteComposerPackageParams{ID: id, RepositoryID: repoID}); err != nil {
		return fmt.Errorf("failed to delete composer package: %w", err)
	}

	return nil
}
