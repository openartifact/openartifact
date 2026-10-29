// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// ComposerStore owns the Composer packages.
//
// Packages live in the generic package tables (repository format "composer"; the
// mapping is the maintainers' reference, derived from migrations/001_baseline.sql):
//
//	packages          one per vendor/package: name = name_key = name as published, attrs {}
//	package_versions  one per published version: version = the display version
//	                  ("v1.2.3", "dev-main"), version_key = Composer's normalised
//	                  version (the live key and the cleanup tie-break text),
//	                  metadata {"composer_json": <the manifest as uploaded>}
//	package_files     one primary listed file per version: path
//	                  "<name>/<version_normalized>/<filename>", filename
//	                  "<vendor>-<package>-<version>.zip" (the listing expression),
//	                  attrs {"sha1": <40 hex>} (omitted when empty; sha1 is
//	                  not in checksums, the checksum search never looked at it)
//
// Composer is not federated: the file carries no unit_key.
type ComposerStore struct{ *core }

const (
	composerMediaType = "application/zip"
	composerFormat    = "composer"
)

// composerRow lists the sqlc row types composerFromRow converts; they share the columns of ListComposerPackageVersionsRow.
type composerRow interface {
	dbsqlc.ListComposerPackageVersionsRow | dbsqlc.GetComposerPackageRow | dbsqlc.GetComposerPackageByIDRow
}

func composerFromRow[R composerRow](in R) *models.ComposerPackage {
	row := dbsqlc.ListComposerPackageVersionsRow(in)

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

// CreateComposerPackage stores one package version. A live version with the
// same name and normalised version is ErrDuplicate.
func (p *ComposerStore) CreateComposerPackage(ctx context.Context, c *models.ComposerPackage) error {
	doc := c.ComposerJSON
	if len(doc) == 0 {
		doc = json.RawMessage(`{}`)
	}

	attrs := map[string]string{}
	if c.SHA1 != "" {
		attrs["sha1"] = c.SHA1
	}

	filename := strings.ReplaceAll(c.Name, "/", "-") + "-" + c.Version + ".zip"

	written, err := p.writePackage(ctx, &PackageWrite{
		RepoID:      c.RepositoryID,
		Format:      composerFormat,
		Name:        c.Name,
		NameKey:     c.Name,
		Version:     c.Version,
		VersionKey:  c.VersionNormalized,
		VersionMeta: map[string]json.RawMessage{"composer_json": doc},
		Exclusive:   true,
		CreatedAt:   c.CreatedAt,
		Files: []FileWrite{{
			Path:     c.Name + "/" + c.VersionNormalized + "/" + filename,
			Filename: filename,
			Blob:     BlobWrite{SHA256: c.SHA256, Key: c.BlobKey, MediaType: composerMediaType, Size: c.SizeBytes},
			Attrs:    attrs,
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to create composer package: %w", err)
	}

	c.ID = written.Files[0].ID
	c.CreatedAt = written.Files[0].CreatedAt
	c.UpdatedAt = written.Files[0].UpdatedAt

	return nil
}

// GetComposerPackage returns the live version of a package, or (nil, nil).
func (p *ComposerStore) GetComposerPackage(ctx context.Context, repoID, name, versionNormalized string) (*models.ComposerPackage, error) {
	row, err := p.q.GetComposerPackage(ctx, dbsqlc.GetComposerPackageParams{
		RepositoryID: repoID, Name: name, VersionNormalized: versionNormalized,
	})

	return mapRow(row, err, "get composer package", composerFromRow)
}

// GetComposerPackageByID returns the live package version of the repository
// with the ID, or (nil, nil).
func (p *ComposerStore) GetComposerPackageByID(ctx context.Context, repoID, id string) (*models.ComposerPackage, error) {
	row, err := p.q.GetComposerPackageByID(ctx, dbsqlc.GetComposerPackageByIDParams{RepositoryID: repoID, ID: id})

	return mapRow(row, err, "get composer package by id", composerFromRow)
}

// ListComposerPackageVersions returns every live version of one package, oldest upload first.
func (p *ComposerStore) ListComposerPackageVersions(ctx context.Context, repoID, name string) ([]*models.ComposerPackage, error) {
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

// ListComposerPackageNames returns every package of the repository in name
// order with the description of its newest live version.
func (p *ComposerStore) ListComposerPackageNames(ctx context.Context, repoID string) ([]*models.ComposerPackageName, error) {
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

// DeleteComposerPackage removes a package version file; the version and the
// package go with it when it was their last file.
func (p *ComposerStore) DeleteComposerPackage(ctx context.Context, repoID, id string) error {
	if _, err := p.deleteFiles(ctx, repoID, []string{id}); err != nil {
		return fmt.Errorf("failed to delete composer package: %w", err)
	}

	return nil
}
