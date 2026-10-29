// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// ConanStore owns the Conan recipes and packages.
type ConanStore struct{ *core }

// CreateConanPackage stores one Conan file, replacing the blob of the live row
// with the same (recipe revision, package id, package revision, filename).
//
// conan_packages.blob_id is nullable; rows restored from a backup that predates
// the sha256 field carry only the blob key, which then stands in for the digest.
func (p *ConanStore) CreateConanPackage(ctx context.Context, pkg *models.ConanPackage) error {
	var blobID *string

	if pkg.BlobKey != "" {
		sha := pkg.SHA256
		if sha == "" {
			sha = pkg.BlobKey
		}

		id, err := p.upsertBlobWithMediaType(ctx, sha, pkg.BlobKey, "application/octet-stream", pkg.SizeBytes)
		if err != nil {
			return fmt.Errorf("failed to upsert blob for conan package: %w", err)
		}

		blobID = &id
	}

	row, err := p.q.CreateConanPackage(ctx, dbsqlc.CreateConanPackageParams{
		CreatedAt:       createdAtOrNil(pkg.CreatedAt),
		RepositoryID:    pkg.RepositoryID,
		Name:            pkg.Name,
		Version:         pkg.Version,
		UserChannel:     pkg.UserChannel,
		RecipeRevision:  pkg.RecipeRevision,
		PackageID:       pkg.PackageID,
		PackageRevision: pkg.PackageRevision,
		Filename:        pkg.Filename,
		BlobID:          blobID,
		Md5:             pkg.MD5,
		Sha1:            pkg.SHA1,
	})

	return finishCreate(insertedRow(row), err, "create conan package", &pkg.ID, &pkg.CreatedAt, &pkg.UpdatedAt)
}

// conanPackageFromRow converts a conan_packages row joined with its blob.
// conanPackageRow lists the sqlc row types conanPackageFromRow converts; they share the columns of GetConanPackageRow.
type conanPackageRow interface {
	dbsqlc.GetConanPackageRow | dbsqlc.ListConanPackagesRow | dbsqlc.FindConanPackageBySHA1Row
}

func conanPackageFromRow[R conanPackageRow](in R) *models.ConanPackage {
	row := dbsqlc.GetConanPackageRow(in)

	return &models.ConanPackage{
		ID:              row.ID,
		RepositoryID:    row.RepositoryID,
		Name:            row.Name,
		Version:         row.Version,
		UserChannel:     row.UserChannel,
		RecipeRevision:  row.RecipeRevision,
		PackageID:       row.PackageID,
		PackageRevision: row.PackageRevision,
		Filename:        row.Filename,
		BlobKey:         row.BlobKey,
		SHA256:          row.Sha256,
		SizeBytes:       row.SizeBytes,
		MD5:             row.Md5,
		SHA1:            row.Sha1,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

// GetConanPackage returns the live file stored under key, or (nil, nil).
func (p *ConanStore) GetConanPackage(ctx context.Context, repoID string, key models.ConanFileKey) (*models.ConanPackage, error) {
	row, err := p.q.GetConanPackage(ctx, dbsqlc.GetConanPackageParams{
		RepositoryID:    repoID,
		Name:            key.Name,
		Version:         key.Version,
		UserChannel:     key.UserChannel,
		RecipeRevision:  key.RecipeRevision,
		PackageID:       key.PackageID,
		PackageRevision: key.PackageRevision,
		Filename:        key.Filename,
	})

	return mapRow(row, err, "get conan package", conanPackageFromRow)
}

// ListConanRecipeFiles returns every live file of one recipe (all its recipe
// and package revisions), oldest first.
func (p *ConanStore) ListConanRecipeFiles(ctx context.Context, repoID, name, version, userChannel string) ([]*models.ConanPackage, error) {
	rows, err := p.q.ListConanRecipeFiles(ctx, dbsqlc.ListConanRecipeFilesParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		UserChannel:  userChannel,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query conan recipe files: %w", err)
	}

	files := make([]*models.ConanPackage, 0, len(rows))

	for _, row := range rows {
		files = append(files, conanPackageFromRow(dbsqlc.GetConanPackageRow(row)))
	}

	return files, nil
}

// ListConanRecipes returns the distinct recipes that have at least one file.
func (p *ConanStore) ListConanRecipes(ctx context.Context, repoID string) ([]models.ConanRecipe, error) {
	rows, err := p.q.ListConanRecipes(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to query conan recipes: %w", err)
	}

	recipes := make([]models.ConanRecipe, 0, len(rows))

	for _, row := range rows {
		recipes = append(recipes, models.ConanRecipe{Name: row.Name, Version: row.Version, UserChannel: row.UserChannel})
	}

	return recipes, nil
}

// FindConanPackageBySHA1 returns a live file of the repository whose content
// has the given SHA-1, or (nil, nil).
func (p *ConanStore) FindConanPackageBySHA1(ctx context.Context, repoID, sha1 string) (*models.ConanPackage, error) {
	row, err := p.q.FindConanPackageBySHA1(ctx, dbsqlc.FindConanPackageBySHA1Params{RepositoryID: repoID, Sha1: sha1})

	return mapRow(row, err, "find conan package by sha1", conanPackageFromRow)
}

// SetConanPackageDigests records the MD5 and SHA-1 of a file stored without
// them.
func (p *ConanStore) SetConanPackageDigests(ctx context.Context, id, md5, sha1 string) error {
	if err := p.q.SetConanPackageDigests(ctx, dbsqlc.SetConanPackageDigestsParams{ID: id, Md5: md5, Sha1: sha1}); err != nil {
		return fmt.Errorf("failed to set conan package digests: %w", err)
	}

	return nil
}

func (p *ConanStore) DeleteConanPackage(ctx context.Context, id string) error {
	if err := p.q.DeleteConanPackage(ctx, id); err != nil {
		return fmt.Errorf("failed to delete conan package: %w", err)
	}

	return nil
}

func (p *ConanStore) ListConanPackages(ctx context.Context, repoID string) ([]*models.ConanPackage, error) {
	rows, err := p.q.ListConanPackages(ctx, repoID)

	return mapRowsOrNil(rows, err, "query conan packages", conanPackageFromRow)
}
