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

// NuGetStore owns the NuGet packages.
//
// Mapping of a package version onto the generic package tables (one .nupkg is
// one version and one file; the package id is the package):
//
//	packages          name = package id as stored (the handler lower-cases it),
//	                  name_key = lower(package id): every lookup is by name_key
//	package_versions  version = version_key = the normalised version; metadata
//	                  {title, description, summary, authors, owners, tags,
//	                  project_url, license_url, icon_url, min_client_version
//	                  (empty values omitted), require_license_acceptance,
//	                  is_prerelease (always written), dependency_groups
//	                  (omitted until derived; written, also as [], once
//	                  derived)}
//	package_files     path <id>/<version>/<id>.<version>.nupkg (the flat
//	                  container path), filename <id>.<version>.nupkg, the nupkg
//	                  blob, role primary, download_count (the package's
//	                  counter), unit_key <id>@<version> (federation)
//
// The id of a package version is package_files.id. A duplicate publish is a
// live version with a live file: ErrDuplicate, the stored content stays.
type NuGetStore struct{ *core }

// nugetMetadata is package_versions.metadata of a package version.
type nugetMetadata struct {
	Title                    string          `json:"title,omitempty"`
	Description              string          `json:"description,omitempty"`
	Summary                  string          `json:"summary,omitempty"`
	Authors                  string          `json:"authors,omitempty"`
	Owners                   string          `json:"owners,omitempty"`
	Tags                     string          `json:"tags,omitempty"`
	ProjectURL               string          `json:"project_url,omitempty"`
	LicenseURL               string          `json:"license_url,omitempty"`
	IconURL                  string          `json:"icon_url,omitempty"`
	MinClientVersion         string          `json:"min_client_version,omitempty"`
	RequireLicenseAcceptance bool            `json:"require_license_acceptance"`
	IsPrerelease             bool            `json:"is_prerelease"`
	DependencyGroups         json.RawMessage `json:"dependency_groups,omitempty"`
}

// nugetKey is the package identity: the lower-cased package id.
func nugetKey(packageID string) string {
	return strings.ToLower(packageID)
}

func (p *NuGetStore) CreateNuGetPackage(ctx context.Context, pkg *models.NuGetPackage) error {
	// PackageHash is SHA512-base64 (~88 chars): too long for blobs.sha256
	// VARCHAR(64) and not a real sha256, so BlobKey (the storage key) doubles
	// as the blob dedup key here, same as the docker_blobs precedent for
	// formats that don't compute a content hash before BlobStore.Put.
	// pkg.PackageHash and pkg.PackageHashAlgorithm do not round-trip.
	filename := pkg.PackageID + "." + pkg.Version + ".nupkg"

	out, err := p.writePackage(ctx, &PackageWrite{
		RepoID:     pkg.RepositoryID,
		Format:     "nuget",
		Name:       pkg.PackageID,
		NameKey:    nugetKey(pkg.PackageID),
		Version:    pkg.Version,
		VersionKey: pkg.Version,
		VersionMeta: nugetMetadata{
			Title:                    pkg.Title,
			Description:              pkg.Description,
			Summary:                  pkg.Summary,
			Authors:                  pkg.Authors,
			Owners:                   pkg.Owners,
			Tags:                     pkg.Tags,
			ProjectURL:               pkg.ProjectURL,
			LicenseURL:               pkg.LicenseURL,
			IconURL:                  pkg.IconURL,
			MinClientVersion:         pkg.MinClientVersion,
			RequireLicenseAcceptance: pkg.RequireLicenseAcceptance,
			IsPrerelease:             pkg.IsPrerelease,
			DependencyGroups:         pkg.DependencyGroups,
		},
		Exclusive: true,
		CreatedAt: pkg.CreatedAt,
		Files: []FileWrite{{
			Path:     pkg.PackageID + "/" + pkg.Version + "/" + filename,
			Filename: filename,
			Blob:     BlobWrite{SHA256: pkg.BlobKey, Key: pkg.BlobKey, MediaType: "application/zip", Size: pkg.PackageSize},
			UnitKey:  pkg.PackageID + "@" + pkg.Version,
			// The imported counter survives (a restore keeps the downloads).
			DownloadCount: int64(pkg.DownloadCount),
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to create nuget package: %w", err)
	}

	pkg.ID, pkg.CreatedAt, pkg.UpdatedAt = out.Files[0].ID, out.Files[0].CreatedAt, out.Files[0].UpdatedAt

	return nil
}

func (p *NuGetStore) GetNuGetPackage(ctx context.Context, repoID, packageID, version string) (*models.NuGetPackage, error) {
	row, err := p.q.GetNuGetPackage(ctx, dbsqlc.GetNuGetPackageParams{
		RepositoryID: repoID,
		NameKey:      nugetKey(packageID),
		Version:      version,
	})

	return mapRow(row, err, "get nuget package", nugetRowToModel)
}

// DeleteNuGetPackage hard-deletes the live file of a package version; the
// version and the package go with their last file.
func (p *NuGetStore) DeleteNuGetPackage(ctx context.Context, repoID, packageID, version string) error {
	ids, err := p.q.ListNuGetPackageFileIDs(ctx, dbsqlc.ListNuGetPackageFileIDsParams{
		RepositoryID: repoID,
		NameKey:      nugetKey(packageID),
		Version:      version,
	})
	if err != nil {
		return fmt.Errorf("failed to delete nuget package: %w", err)
	}

	if _, err := p.deleteFiles(ctx, repoID, ids); err != nil {
		return fmt.Errorf("failed to delete nuget package: %w", err)
	}

	return nil
}

// SetNuGetDependencyGroups records the dependency groups of a package that has
// none stored yet. A stored value is never overwritten and the file's
// updated_at is not touched.
func (p *NuGetStore) SetNuGetDependencyGroups(ctx context.Context, repoID, packageID, version string, groups []byte) error {
	if err := p.q.SetNuGetDependencyGroups(ctx, dbsqlc.SetNuGetDependencyGroupsParams{
		DependencyGroups: groups,
		RepositoryID:     repoID,
		NameKey:          nugetKey(packageID),
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
		NameKey:      nugetKey(packageID),
	})

	return mapRowsOrNil(rows, err, "list nuget package versions", nugetRowToModel)
}

// SearchNuGetPackages pages over matching packages: skip/take count packages,
// the total is the number of matching packages, and every live version of each
// package on the page is returned (grouped by package id, oldest first).
func (p *NuGetStore) SearchNuGetPackages(ctx context.Context, repoID, query string, skip, take int) ([]*models.NuGetPackage, int, error) {
	pattern := "%" + escapeLike(strings.ToLower(query)) + "%"

	total, err := p.q.SearchNuGetPackagesCount(ctx, dbsqlc.SearchNuGetPackagesCountParams{
		RepositoryID: repoID,
		Pattern:      pattern,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count nuget packages: %w", err)
	}

	rows, err := p.q.SearchNuGetPackages(ctx, dbsqlc.SearchNuGetPackagesParams{
		RepositoryID: repoID,
		Pattern:      pattern,
		PageOffset:   toInt32(skip),
		PageLimit:    toInt32(take),
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
		NameKey:      nugetKey(packageID),
		Version:      version,
	}); err != nil {
		return fmt.Errorf("failed to increment nuget download count: %w", err)
	}

	return nil
}

// nugetPackageRow lists the sqlc row types nugetRowToModel converts; they share the columns of GetNuGetPackageRow.
type nugetPackageRow interface {
	dbsqlc.GetNuGetPackageRow | dbsqlc.ListNuGetPackagesRow | dbsqlc.ListNuGetPackageVersionsRow
}

// nugetRowToModel converts the structurally identical nuget row types (Get,
// List, ListVersions and Search share the SELECT list) via Go's struct
// conversion rule. PackageHashAlgorithm never round-trips.
func nugetRowToModel[R nugetPackageRow](in R) *models.NuGetPackage {
	row := dbsqlc.GetNuGetPackageRow(in)

	var meta nugetMetadata

	_ = json.Unmarshal(row.Metadata, &meta)

	return &models.NuGetPackage{
		ID:                       row.ID,
		RepositoryID:             row.RepositoryID,
		PackageID:                row.PackageID,
		Version:                  row.Version,
		Title:                    meta.Title,
		Description:              meta.Description,
		Summary:                  meta.Summary,
		Authors:                  meta.Authors,
		Owners:                   meta.Owners,
		Tags:                     meta.Tags,
		ProjectURL:               meta.ProjectURL,
		LicenseURL:               meta.LicenseURL,
		IconURL:                  meta.IconURL,
		RequireLicenseAcceptance: meta.RequireLicenseAcceptance,
		IsPrerelease:             meta.IsPrerelease,
		MinClientVersion:         meta.MinClientVersion,
		DependencyGroups:         meta.DependencyGroups,
		BlobKey:                  row.BlobKey,
		PackageHash:              row.PackageHash,
		PackageSize:              row.PackageSize,
		DownloadCount:            int(row.DownloadCount),
		CreatedAt:                row.CreatedAt,
		UpdatedAt:                row.UpdatedAt,
	}
}
