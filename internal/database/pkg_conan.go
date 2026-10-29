// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// ConanStore owns the Conan recipes and packages.
//
// Conan on the generic package tables (spec: /tmp/oa-plug/schema/104_conan.sql).
// The grain is one stored FILE of a recipe revision (empty package_id) or of a
// package (binary) revision of it; all the files of one recipe revision, binary
// packages included, share one package_versions row.
//
//	packages          name = name_key = name
//	package_versions  version     = version
//	                  version_key = version@user_channel#recipe_revision (one V per recipe
//	                                revision; the cleanup worker ranks by
//	                                split_part(version_key, '#', 1) = version@user_channel)
//	                  metadata    = {"user_channel": .., "recipe_revision": ..}
//	package_files     path = name/version@user_channel#rrev/[package_id#package_revision/]filename
//	                         (encodes the 8 columns of the legacy live-unique key, so
//	                         uq_package_files_path is that key; a file stored before 0.5.0
//	                         has filename "" and the path ends in "/")
//	                  filename  = the full reference name/version@user_channel#rrev for a LISTED
//	                              row, the file name otherwise (the real file name is always
//	                              attrs.filename)
//	                  blob_id   = the file blob, NULL for a row restored without content
//	                  size_bytes = NULL (the listing size is the blob's)
//	                  role = primary, for every row
//	                  listed = package_id = '' AND filename IN ('', 'conanmanifest.txt')
//	                  attrs = {"release": recipe_revision (omitted when ''), "filename": ..}
//	                  checksums = {"md5": .., "sha1": ..}, each only when known
//	conan_files       id -> package_files.id, package_id, package_revision
type ConanStore struct{ *core }

// conanListedManifest is the filename that makes a package_id-less row a unit of
// the repository listing (next to the pre-0.5.0 rows with an empty filename).
const conanListedManifest = "conanmanifest.txt"

// conanRevisionPrefix renders name/version@user_channel#recipe_revision, the
// directory of a recipe revision in every path and the listing reference.
func conanRevisionPrefix(name, version, userChannel, recipeRevision string) string {
	return name + "/" + version + "@" + userChannel + "#" + recipeRevision
}

// conanFilePath renders the repository-wide path of a Conan file: every
// coordinate of the 8-column key, '@', '#' and '/' delimiting unambiguously (a
// reference part is [A-Za-z0-9_.+-]; the filename is last and may contain '/').
func conanFilePath(key models.ConanFileKey) string {
	path := conanRevisionPrefix(key.Name, key.Version, key.UserChannel, key.RecipeRevision) + "/"
	if key.PackageID != "" || key.PackageRevision != "" {
		path += key.PackageID + "#" + key.PackageRevision + "/"
	}

	return path + key.Filename
}

// CreateConanPackage stores one Conan file, replacing the blob and digests of
// the live row with the same (recipe revision, package id, package revision,
// filename).
//
// package_files.blob_id is nullable; rows restored from a backup that predates
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

	meta, err := jsonOrEmpty(map[string]string{"user_channel": pkg.UserChannel, "recipe_revision": pkg.RecipeRevision})
	if err != nil {
		return err
	}

	attrs := map[string]string{"filename": pkg.Filename}
	if pkg.RecipeRevision != "" {
		attrs["release"] = pkg.RecipeRevision
	}

	attrsJSON, err := jsonOrEmpty(attrs)
	if err != nil {
		return err
	}

	sums := map[string]string{}
	if pkg.MD5 != "" {
		sums["md5"] = pkg.MD5
	}

	if pkg.SHA1 != "" {
		sums["sha1"] = pkg.SHA1
	}

	sumsJSON, err := jsonOrEmpty(sums)
	if err != nil {
		return err
	}

	// A LISTED row shows the full reference, every other row its file name.
	listed := pkg.PackageID == "" && (pkg.Filename == "" || pkg.Filename == conanListedManifest)
	filename := pkg.Filename

	if listed {
		filename = conanRevisionPrefix(pkg.Name, pkg.Version, pkg.UserChannel, pkg.RecipeRevision)
	}

	w := &PackageWrite{
		RepoID:     pkg.RepositoryID,
		Format:     "conan",
		Name:       pkg.Name,
		NameKey:    pkg.Name,
		Version:    pkg.Version,
		VersionKey: pkg.Version + "@" + pkg.UserChannel + "#" + pkg.RecipeRevision,
		CreatedAt:  pkg.CreatedAt,
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to create conan package: begin: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	q := p.q.WithTx(tx)

	pkgID, err := q.UpsertPackage(ctx, dbsqlc.UpsertPackageParams{
		RepositoryID: w.RepoID,
		Format:       w.Format,
		Name:         w.Name,
		NameKey:      w.NameKey,
		Attrs:        []byte(`{}`),
	})
	if err != nil {
		return fmt.Errorf("failed to create conan package: upsert package: %w", err)
	}

	versionID, _, err := p.liveVersion(ctx, q, w, pkgID, meta)
	if err != nil {
		return fmt.Errorf("failed to create conan package: %w", err)
	}

	row, err := q.UpsertConanFile(ctx, dbsqlc.UpsertConanFileParams{
		VersionID:    versionID,
		RepositoryID: pkg.RepositoryID,
		Path: conanFilePath(models.ConanFileKey{
			Name: pkg.Name, Version: pkg.Version, UserChannel: pkg.UserChannel, RecipeRevision: pkg.RecipeRevision,
			PackageID: pkg.PackageID, PackageRevision: pkg.PackageRevision, Filename: pkg.Filename,
		}),
		Filename:  filename,
		BlobID:    blobID,
		Listed:    listed,
		Attrs:     attrsJSON,
		Checksums: sumsJSON,
		CreatedAt: createdAtOrNil(pkg.CreatedAt),
	})
	if err != nil {
		return fmt.Errorf("failed to create conan package: upsert file: %w", err)
	}

	if err := q.UpsertConanFileCoords(ctx, dbsqlc.UpsertConanFileCoordsParams{
		ID: row.ID, PackageID: pkg.PackageID, PackageRevision: pkg.PackageRevision,
	}); err != nil {
		return fmt.Errorf("failed to create conan package: coordinates: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to create conan package: commit: %w", err)
	}

	return finishCreate(insertedRow(row), nil, "create conan package", &pkg.ID, &pkg.CreatedAt, &pkg.UpdatedAt)
}

// conanPackageRow lists the sqlc row types conanPackageFromRow converts; they share the columns of GetConanPackageRow.
type conanPackageRow interface {
	dbsqlc.GetConanPackageRow | dbsqlc.ListConanRecipeFilesRow | dbsqlc.FindConanPackageBySHA1Row
}

// conanPackageFromRow converts a package_files row joined with its version,
// package, conan_files and blob.
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
	row, err := p.q.GetConanPackage(ctx, dbsqlc.GetConanPackageParams{RepositoryID: repoID, Path: conanFilePath(key)})

	return mapRow(row, err, "get conan package", conanPackageFromRow)
}

// ListConanRecipeFiles returns every live file of one recipe (all its recipe
// and package revisions), oldest first.
func (p *ConanStore) ListConanRecipeFiles(ctx context.Context, repoID, name, version, userChannel string) ([]*models.ConanPackage, error) {
	rows, err := p.q.ListConanRecipeFiles(ctx, dbsqlc.ListConanRecipeFilesParams{
		RepositoryID: repoID,
		NameKey:      name,
		Version:      version,
		UserChannel:  userChannel,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query conan recipe files: %w", err)
	}

	files := make([]*models.ConanPackage, 0, len(rows))

	for _, row := range rows {
		files = append(files, conanPackageFromRow(row))
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

// DeleteConanPackage hard-deletes one live file together with the recipe
// revision (version) and package it leaves empty.
func (p *ConanStore) DeleteConanPackage(ctx context.Context, id string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete conan package: begin: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	q := p.q.WithTx(tx)

	versionID, err := q.DeleteConanFile(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to delete conan package: %w", err)
	}

	if err := pruneEmpty(ctx, q, []string{versionID}); err != nil {
		return fmt.Errorf("failed to delete conan package: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to delete conan package: commit: %w", err)
	}

	return nil
}
