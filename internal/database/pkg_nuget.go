// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// NuGetStore owns the NuGet packages.
type NuGetStore struct{ *core }

func (p *NuGetStore) CreateNuGetPackage(ctx context.Context, pkg *models.NuGetPackage) error {
	// PackageHash is SHA512-base64 (~88 chars): too long for blobs.sha256
	// VARCHAR(64) and not a real sha256, so BlobKey (the storage key) doubles
	// as the blob dedup key here, same as the docker_blobs precedent for
	// formats that don't compute a content hash before BlobStore.Put.
	// The schema has no package_hash_algorithm column, so
	// pkg.PackageHashAlgorithm does not round-trip.
	blobID, err := p.upsertBlobWithMediaType(ctx, pkg.BlobKey, pkg.BlobKey, "application/zip", pkg.PackageSize)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for nuget package: %w", err)
	}

	row, err := p.q.CreateNuGetPackage(ctx, dbsqlc.CreateNuGetPackageParams{
		CreatedAt:                createdAtOrNil(pkg.CreatedAt),
		RepositoryID:             pkg.RepositoryID,
		PackageID:                pkg.PackageID,
		Version:                  pkg.Version,
		Title:                    strPtr(pkg.Title),
		Description:              strPtr(pkg.Description),
		Summary:                  strPtr(pkg.Summary),
		Authors:                  strPtr(pkg.Authors),
		Owners:                   strPtr(pkg.Owners),
		Tags:                     strPtr(pkg.Tags),
		ProjectUrl:               strPtr(pkg.ProjectURL),
		LicenseUrl:               strPtr(pkg.LicenseURL),
		IconUrl:                  strPtr(pkg.IconURL),
		RequireLicenseAcceptance: pkg.RequireLicenseAcceptance,
		IsPrerelease:             pkg.IsPrerelease,
		MinClientVersion:         strPtr(pkg.MinClientVersion),
		BlobID:                   blobID,
		DependencyGroups:         pkg.DependencyGroups,
	})
	if errors.Is(err, sql.ErrNoRows) {
		// ON CONFLICT DO NOTHING returned no row: the live (repo, id, version)
		// already exists. Its content is left untouched.
		return ErrDuplicate
	}

	return finishCreate(insertedRow(row), err, "create nuget package", &pkg.ID, &pkg.CreatedAt, &pkg.UpdatedAt)
}

func (p *NuGetStore) GetNuGetPackage(ctx context.Context, repoID, packageID, version string) (*models.NuGetPackage, error) {
	row, err := p.q.GetNuGetPackage(ctx, dbsqlc.GetNuGetPackageParams{
		RepositoryID: repoID,
		PackageID:    packageID,
		Version:      version,
	})

	return mapRow(row, err, "get nuget package", nugetRowToModel)
}

func (p *NuGetStore) DeleteNuGetPackage(ctx context.Context, repoID, packageID, version string) error {
	if err := p.q.DeleteNuGetPackage(ctx, dbsqlc.DeleteNuGetPackageParams{
		RepositoryID: repoID,
		PackageID:    packageID,
		Version:      version,
	}); err != nil {
		return fmt.Errorf("failed to delete nuget package: %w", err)
	}

	return nil
}

// SetNuGetDependencyGroups records the dependency groups of a package that has
// none stored yet. A stored value is never overwritten and updated_at is not
// touched.
func (p *NuGetStore) SetNuGetDependencyGroups(ctx context.Context, repoID, packageID, version string, groups []byte) error {
	if err := p.q.SetNuGetDependencyGroups(ctx, dbsqlc.SetNuGetDependencyGroupsParams{
		DependencyGroups: groups,
		RepositoryID:     repoID,
		PackageID:        packageID,
		Version:          version,
	}); err != nil {
		return fmt.Errorf("failed to set nuget dependency groups: %w", err)
	}

	return nil
}

func (p *NuGetStore) ListNuGetPackages(ctx context.Context, repoID string) ([]*models.NuGetPackage, error) {
	rows, err := p.q.ListNuGetPackages(ctx, repoID)

	return mapRowsOrNil(rows, err, "list nuget packages", nugetRowToModel)
}

func (p *NuGetStore) ListNuGetPackageVersions(ctx context.Context, repoID, packageID string) ([]*models.NuGetPackage, error) {
	rows, err := p.q.ListNuGetPackageVersions(ctx, dbsqlc.ListNuGetPackageVersionsParams{
		RepositoryID: repoID,
		PackageID:    packageID,
	})

	return mapRowsOrNil(rows, err, "list nuget package versions", nugetRowToModel)
}

// SearchNuGetPackages pages over matching packages: skip/take count packages,
// the total is the number of matching packages, and every live version of each
// package on the page is returned (grouped by package id, oldest first).
func (p *NuGetStore) SearchNuGetPackages(ctx context.Context, repoID, query string, skip, take int) ([]*models.NuGetPackage, int, error) {
	searchQuery := "%" + escapeLike(strings.ToLower(query)) + "%"

	total, err := p.q.SearchNuGetPackagesCount(ctx, dbsqlc.SearchNuGetPackagesCountParams{
		RepositoryID: repoID,
		PackageID:    searchQuery,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count nuget packages: %w", err)
	}

	rows, err := p.q.SearchNuGetPackages(ctx, dbsqlc.SearchNuGetPackagesParams{
		RepositoryID: repoID,
		PackageID:    searchQuery,
		Offset:       toInt32(skip),
		Limit:        toInt32(take),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to search nuget packages: %w", err)
	}

	var packages []*models.NuGetPackage

	for _, row := range rows {
		packages = append(packages, nugetRowToModel(dbsqlc.GetNuGetPackageRow(row)))
	}

	return packages, int(total), nil
}

func (p *NuGetStore) IncrementNuGetDownloadCount(ctx context.Context, repoID, packageID, version string) error {
	if err := p.q.IncrementNuGetDownloadCount(ctx, dbsqlc.IncrementNuGetDownloadCountParams{
		RepositoryID: repoID,
		PackageID:    packageID,
		Version:      version,
	}); err != nil {
		return fmt.Errorf("failed to increment nuget download count: %w", err)
	}

	return nil
}

// nugetRowToModel converts any of the four structurally-identical nuget row
// types (Get/List/ListVersions/Search all share the same SELECT list) via
// Go's struct conversion rule (identical field sequence, tags ignored).
// The schema has no package_hash_algorithm column, so PackageHashAlgorithm
// never round-trips.
// nugetPackageRow lists the sqlc row types nugetRowToModel converts; they share the columns of GetNuGetPackageRow.
type nugetPackageRow interface {
	dbsqlc.GetNuGetPackageRow | dbsqlc.ListNuGetPackagesRow | dbsqlc.ListNuGetPackageVersionsRow
}

func nugetRowToModel[R nugetPackageRow](in R) *models.NuGetPackage {
	row := dbsqlc.GetNuGetPackageRow(in)

	return &models.NuGetPackage{
		ID:                       row.ID,
		RepositoryID:             row.RepositoryID,
		PackageID:                row.PackageID,
		Version:                  row.Version,
		Title:                    derefStr(row.Title),
		Description:              derefStr(row.Description),
		Summary:                  derefStr(row.Summary),
		Authors:                  derefStr(row.Authors),
		Owners:                   derefStr(row.Owners),
		Tags:                     derefStr(row.Tags),
		ProjectURL:               derefStr(row.ProjectUrl),
		LicenseURL:               derefStr(row.LicenseUrl),
		IconURL:                  derefStr(row.IconUrl),
		RequireLicenseAcceptance: row.RequireLicenseAcceptance,
		IsPrerelease:             row.IsPrerelease,
		MinClientVersion:         derefStr(row.MinClientVersion),
		DependencyGroups:         row.DependencyGroups,
		BlobKey:                  row.BlobKey,
		PackageHash:              row.PackageHash,
		PackageSize:              row.PackageSize,
		DownloadCount:            int(row.DownloadCount),
		CreatedAt:                row.CreatedAt,
		UpdatedAt:                row.UpdatedAt,
	}
}
