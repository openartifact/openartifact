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

// CondaStore owns the Conda packages.
type CondaStore struct{ *core }

// CreateCondaPackage creates a new Conda package file. An empty
// pkg.Extension is stored as ".tar.bz2" and an empty pkg.Extra as "{}".
// A live row for the same (name, version, build, platform, extension) yields
// ErrDuplicate and is left untouched.
func (p *CondaStore) CreateCondaPackage(ctx context.Context, pkg *models.CondaPackage) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, pkg.SHA256, pkg.BlobKey, "application/x-conda", pkg.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for conda package: %w", err)
	}

	ext := pkg.Extension
	if ext == "" {
		ext = models.CondaExtTarBz2
	}

	extra := pkg.Extra
	if len(extra) == 0 || string(extra) == "null" {
		extra = json.RawMessage(`{}`)
	}

	depends := pkg.Depends
	if depends == nil {
		depends = []string{}
	}

	row, err := p.q.CreateCondaPackage(ctx, dbsqlc.CreateCondaPackageParams{
		CreatedAt:    createdAtOrNil(pkg.CreatedAt),
		RepositoryID: pkg.RepositoryID,
		Name:         pkg.Name,
		Version:      pkg.Version,
		BuildString:  pkg.BuildString,
		Platform:     pkg.Platform,
		Depends:      depends,
		BlobID:       blobID,
		Extension:    ext,
		BuildNumber:  toInt32(pkg.BuildNumber),
		Md5:          pkg.MD5,
		TimestampMs:  pkg.Timestamp,
		IndexExtra:   extra,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDuplicate
	}

	if err != nil {
		return fmt.Errorf("failed to create conda package: %w", err)
	}

	pkg.ID = row.ID
	pkg.Extension = ext
	pkg.CreatedAt = row.CreatedAt
	pkg.UpdatedAt = row.UpdatedAt

	return nil
}

// GetCondaPackageFile returns the live package file with this exact
// name, version, build, platform and extension, or nil.
func (p *CondaStore) GetCondaPackageFile(
	ctx context.Context,
	repoID, name, version, buildString, platform, extension string,
) (*models.CondaPackage, error) {
	row, err := p.q.GetCondaPackageFile(ctx, dbsqlc.GetCondaPackageFileParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		BuildString:  buildString,
		Platform:     platform,
		Extension:    extension,
	})

	return mapRow(row, err, "get conda package", condaRowToModel)
}

// condaRowToModel converts any of the three structurally identical conda row
// types (GetFile/List/ListAll share the same SELECT list).
// condaPackageRow lists the sqlc row types condaRowToModel converts; they share the columns of GetCondaPackageFileRow.
type condaPackageRow interface {
	dbsqlc.GetCondaPackageFileRow | dbsqlc.ListCondaPackagesRow | dbsqlc.ListAllCondaPackagesRow
}

func condaRowToModel[R condaPackageRow](in R) *models.CondaPackage {
	row := dbsqlc.GetCondaPackageFileRow(in)

	return &models.CondaPackage{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Name:         row.Name,
		Version:      row.Version,
		BuildString:  row.BuildString,
		Platform:     row.Platform,
		Extension:    row.Extension,
		BuildNumber:  int(row.BuildNumber),
		Timestamp:    row.TimestampMs,
		Depends:      row.Depends,
		Extra:        row.IndexExtra,
		BlobKey:      row.BlobKey,
		SizeBytes:    row.SizeBytes,
		SHA256:       row.Sha256,
		MD5:          row.Md5,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

func (p *CondaStore) DeleteCondaPackage(ctx context.Context, id string) error {
	err := p.q.DeleteCondaPackage(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to delete conda package: %w", err)
	}

	return nil
}

func (p *CondaStore) ListCondaPackages(ctx context.Context, repoID, platform string) ([]*models.CondaPackage, error) {
	rows, err := p.q.ListCondaPackages(ctx, dbsqlc.ListCondaPackagesParams{
		RepositoryID: repoID,
		Platform:     platform,
	})

	return mapRowsOrNil(rows, err, "query conda packages", condaRowToModel)
}

// ListAllCondaPackages lists all conda packages for a repository across
// every platform. Used only by backup export, where ListCondaPackages'
// mandatory platform filter would silently drop everything else.
func (p *CondaStore) ListAllCondaPackages(ctx context.Context, repoID string) ([]*models.CondaPackage, error) {
	rows, err := p.q.ListAllCondaPackages(ctx, repoID)

	return mapRowsOrNil(rows, err, "query all conda packages", condaRowToModel)
}
