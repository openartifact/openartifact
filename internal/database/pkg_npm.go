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

// NPMStore owns the npm packages, versions and dist-tags.
type NPMStore struct{ *core }

func (p *NPMStore) CreateNPMPackage(ctx context.Context, pkg *models.NPMPackage) (*models.NPMPackage, error) {
	row, err := p.q.CreateNPMPackage(ctx, dbsqlc.CreateNPMPackageParams{
		RepositoryID:   pkg.RepositoryID,
		Scope:          pkg.Scope,
		Name:           pkg.Name,
		FullName:       pkg.FullName,
		LatestVersion:  pkg.LatestVersion,
		PackumentStale: pkg.PackumentStale,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create npm package: %w", err)
	}

	pkg.ID = row.ID
	pkg.CreatedAt = row.CreatedAt
	pkg.UpdatedAt = row.UpdatedAt

	return pkg, nil
}

func (p *NPMStore) GetNPMPackageByName(ctx context.Context, repoID, fullName string) (*models.NPMPackage, error) {
	row, err := p.q.GetNPMPackageByName(ctx, dbsqlc.GetNPMPackageByNameParams{
		RepositoryID: repoID,
		FullName:     fullName,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get npm package by name: %w", err)
	}

	return &models.NPMPackage{
		ID:             row.ID,
		RepositoryID:   row.RepositoryID,
		Scope:          row.Scope,
		Name:           row.Name,
		FullName:       row.FullName,
		LatestVersion:  row.LatestVersion,
		PackumentStale: row.PackumentStale,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}

func (p *NPMStore) DeleteNPMPackage(ctx context.Context, packageID string) error {
	if err := p.q.DeleteNPMPackage(ctx, packageID); err != nil {
		return fmt.Errorf("failed to delete npm package: %w", err)
	}

	return nil
}

func (p *NPMStore) MarkPackumentStale(ctx context.Context, packageID string) error {
	if err := p.q.MarkPackumentStale(ctx, packageID); err != nil {
		return fmt.Errorf("failed to mark packument stale: %w", err)
	}

	return nil
}

func (p *NPMStore) CreateNPMVersion(ctx context.Context, v *models.NPMVersion) (*models.NPMVersion, error) {
	row, err := p.q.CreateNPMVersion(ctx, dbsqlc.CreateNPMVersionParams{
		CreatedAt:       createdAtOrNil(v.CreatedAt),
		PackageID:       v.PackageID,
		Version:         v.Version,
		Column3:         v.ManifestJSON,
		Integrity:       v.Integrity,
		Shasum:          v.Shasum,
		BlobID:          v.BlobID,
		TarballFilename: v.TarballFilename,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create npm version: %w", err)
	}

	v.ID = row.ID
	v.CreatedAt = row.CreatedAt

	return v, nil
}

func (p *NPMStore) GetNPMVersion(ctx context.Context, packageID, version string) (*models.NPMVersion, error) {
	row, err := p.q.GetNPMVersion(ctx, dbsqlc.GetNPMVersionParams{
		PackageID: packageID,
		Version:   version,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get npm version: %w", err)
	}

	nv := &models.NPMVersion{
		ID:              row.ID,
		PackageID:       row.PackageID,
		Version:         row.Version,
		ManifestJSON:    row.ManifestJson,
		Integrity:       row.Integrity,
		Shasum:          row.Shasum,
		BlobID:          row.BlobID,
		TarballFilename: row.TarballFilename,
		CreatedAt:       row.CreatedAt,
	}

	if err := p.fillNPMBlobSHA256(ctx, packageID, nv); err != nil {
		return nil, err
	}

	return nv, nil
}

func (p *NPMStore) GetNPMVersionsByPackage(ctx context.Context, packageID string) ([]*models.NPMVersion, error) {
	rows, err := p.q.GetNPMVersionsByPackage(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("failed to query npm versions: %w", err)
	}

	var versions []*models.NPMVersion

	for _, row := range rows {
		versions = append(versions, &models.NPMVersion{
			ID:              row.ID,
			PackageID:       row.PackageID,
			Version:         row.Version,
			ManifestJSON:    row.ManifestJson,
			Integrity:       row.Integrity,
			Shasum:          row.Shasum,
			BlobID:          row.BlobID,
			TarballFilename: row.TarballFilename,
			CreatedAt:       row.CreatedAt,
		})
	}

	if err := p.fillNPMBlobSHA256(ctx, packageID, versions...); err != nil {
		return nil, err
	}

	return versions, nil
}

// GetNPMVersionStates returns number and deprecation of every live version
// of the package, without the manifests.
func (p *NPMStore) GetNPMVersionStates(ctx context.Context, packageID string) ([]models.NPMVersionState, error) {
	rows, err := p.q.GetNPMVersionStates(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("failed to query npm version states: %w", err)
	}

	states := make([]models.NPMVersionState, len(rows))
	for i, row := range rows {
		states[i] = models.NPMVersionState{Version: row.Version, Deprecated: row.Deprecated}
	}

	return states, nil
}

func (p *NPMStore) UpdateNPMPackageLatestVersion(ctx context.Context, packageID, latestVersion string) error {
	if err := p.q.UpdateNPMPackageLatestVersion(ctx, dbsqlc.UpdateNPMPackageLatestVersionParams{
		ID:            packageID,
		LatestVersion: latestVersion,
	}); err != nil {
		return fmt.Errorf("failed to update npm package latest version: %w", err)
	}

	return nil
}

// UpdateNPMVersionManifest replaces the stored manifest of a live version
// (used for `npm deprecate`).
func (p *NPMStore) UpdateNPMVersionManifest(ctx context.Context, packageID, version string, manifest []byte) error {
	if err := p.q.UpdateNPMVersionManifest(ctx, dbsqlc.UpdateNPMVersionManifestParams{
		PackageID: packageID,
		Version:   version,
		Column3:   manifest,
	}); err != nil {
		return fmt.Errorf("failed to update npm version manifest: %w", err)
	}

	return nil
}

func (p *NPMStore) DeleteNPMVersion(ctx context.Context, packageID, version string) error {
	if err := p.q.DeleteNPMVersion(ctx, dbsqlc.DeleteNPMVersionParams{
		PackageID: packageID,
		Version:   version,
	}); err != nil {
		return fmt.Errorf("failed to delete npm version: %w", err)
	}

	return nil
}

func (p *NPMStore) UpsertNPMDistTag(ctx context.Context, t *models.NPMDistTag) (*models.NPMDistTag, error) {
	row, err := p.q.UpsertNPMDistTag(ctx, dbsqlc.UpsertNPMDistTagParams{
		PackageID: t.PackageID,
		Tag:       t.Tag,
		Version:   t.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upsert npm dist tag: %w", err)
	}

	t.ID = row.ID
	t.UpdatedAt = row.UpdatedAt

	return t, nil
}

func (p *NPMStore) GetNPMDistTags(ctx context.Context, packageID string) (map[string]string, error) {
	rows, err := p.q.GetNPMDistTags(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("failed to query npm dist tags: %w", err)
	}

	tags := make(map[string]string)

	for _, row := range rows {
		tags[row.Tag] = row.Version
	}

	return tags, nil
}

func (p *NPMStore) DeleteNPMDistTag(ctx context.Context, packageID, tag string) error {
	if err := p.q.DeleteNPMDistTag(ctx, dbsqlc.DeleteNPMDistTagParams{
		PackageID: packageID,
		Tag:       tag,
	}); err != nil {
		return fmt.Errorf("failed to delete npm dist tag: %w", err)
	}

	return nil
}

// ListAllNPMPackages lists every npm package for a repository. Used only
// by backup export.
func (p *NPMStore) ListAllNPMPackages(ctx context.Context, repoID string) ([]*models.NPMPackage, error) {
	rows, err := p.q.ListAllNPMPackages(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list all npm packages: %w", err)
	}

	var packages []*models.NPMPackage

	for _, row := range rows {
		packages = append(packages, &models.NPMPackage{
			ID:             row.ID,
			RepositoryID:   row.RepositoryID,
			Scope:          row.Scope,
			Name:           row.Name,
			FullName:       row.FullName,
			LatestVersion:  row.LatestVersion,
			PackumentStale: row.PackumentStale,
			CreatedAt:      row.CreatedAt,
			UpdatedAt:      row.UpdatedAt,
		})
	}

	return packages, nil
}

// ListNPMVersionsByRepo lists every npm version for a repository, with its
// tarball blob's sha256/size resolved via a JOIN (unlike BlobID on the
// other Get* methods here, which is the raw, restore-incompatible blobs.id
// UUID). Used only by backup export.
func (p *NPMStore) ListNPMVersionsByRepo(ctx context.Context, repoID string) ([]*models.NPMVersion, error) {
	rows, err := p.q.ListNPMVersionsByRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list npm versions by repo: %w", err)
	}

	var versions []*models.NPMVersion

	for _, row := range rows {
		versions = append(versions, &models.NPMVersion{
			ID:              row.ID,
			PackageID:       row.PackageID,
			Version:         row.Version,
			ManifestJSON:    row.ManifestJson,
			Integrity:       row.Integrity,
			Shasum:          row.Shasum,
			TarballFilename: row.TarballFilename,
			BlobSHA256:      row.BlobSha256,
			BlobSizeBytes:   row.BlobSizeBytes,
			CreatedAt:       row.CreatedAt,
		})
	}

	return versions, nil
}
