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

// PyPIStore owns the PyPI distributions.
type PyPIStore struct{ *core }

func (p *PyPIStore) CreatePyPIPackage(ctx context.Context, pkg *models.PyPIPackage) error {
	// pypi_packages has no md5_digest column and the blobs table only tracks
	// sha256, so pkg.MD5Digest does not round-trip.
	blobID, err := p.upsertBlobWithMediaType(ctx, pkg.SHA256Digest, pkg.BlobKey, "application/zip", pkg.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for pypi package: %w", err)
	}

	row, err := p.q.CreatePyPIPackage(ctx, dbsqlc.CreatePyPIPackageParams{
		CreatedAt:      createdAtOrNil(pkg.CreatedAt),
		RepositoryID:   pkg.RepositoryID,
		Name:           pkg.Name,
		Version:        pkg.Version,
		Filename:       pkg.Filename,
		PackageType:    pkg.PackageType,
		PythonVersion:  strPtr(pkg.PythonVersion),
		RequiresPython: strPtr(pkg.RequiresPython),
		Summary:        strPtr(pkg.Summary),
		Author:         strPtr(pkg.Author),
		AuthorEmail:    strPtr(pkg.AuthorEmail),
		License:        strPtr(pkg.License),
		Keywords:       strPtr(pkg.Keywords),
		Classifiers:    pkg.Classifiers,
		BlobID:         blobID,
	})

	return finishCreate(insertedRow(row), err, "create pypi package", &pkg.ID, &pkg.CreatedAt, &pkg.UpdatedAt)
}

// pypiPackageFromRow maps a sqlc PyPI package row; the list rows convert to it.
// pypiPackageRow lists the sqlc row types pypiPackageFromRow converts; they share the columns of GetPyPIPackagesByFilenameRow.
type pypiPackageRow interface {
	dbsqlc.GetPyPIPackagesByFilenameRow | dbsqlc.ListPyPIPackagesRow | dbsqlc.ListPyPIPackageVersionsRow
}

func pypiPackageFromRow[R pypiPackageRow](in R) *models.PyPIPackage {
	row := dbsqlc.GetPyPIPackagesByFilenameRow(in)

	return &models.PyPIPackage{
		ID:             row.ID,
		RepositoryID:   row.RepositoryID,
		Name:           row.Name,
		Version:        row.Version,
		Filename:       row.Filename,
		PackageType:    row.PackageType,
		PythonVersion:  derefStr(row.PythonVersion),
		RequiresPython: derefStr(row.RequiresPython),
		Summary:        derefStr(row.Summary),
		Author:         derefStr(row.Author),
		AuthorEmail:    derefStr(row.AuthorEmail),
		License:        derefStr(row.License),
		Keywords:       derefStr(row.Keywords),
		Classifiers:    row.Classifiers,
		BlobKey:        row.BlobKey,
		SHA256Digest:   row.Sha256Digest,
		SizeBytes:      row.SizeBytes,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func (p *PyPIStore) GetPyPIPackageByFilename(ctx context.Context, repoID, filename string) (*models.PyPIPackage, error) {
	rows, err := p.q.GetPyPIPackagesByFilename(ctx, dbsqlc.GetPyPIPackagesByFilenameParams{RepositoryID: repoID, Filename: filename})
	if err != nil {
		return nil, fmt.Errorf("failed to get pypi package by filename: %w", err)
	}

	switch len(rows) {
	case 0:
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	case 1:
	default:
		return nil, ErrAmbiguousPyPIFilename
	}

	row := rows[0]

	return pypiPackageFromRow(row), nil
}

func (p *PyPIStore) DeletePyPIPackage(ctx context.Context, repoID, name, version, filename string) error {
	err := p.q.DeletePyPIPackage(ctx, dbsqlc.DeletePyPIPackageParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		Filename:     filename,
	})
	if err != nil {
		return fmt.Errorf("failed to delete pypi package: %w", err)
	}

	return nil
}

func (p *PyPIStore) ListPyPIPackages(ctx context.Context, repoID string) ([]*models.PyPIPackage, error) {
	rows, err := p.q.ListPyPIPackages(ctx, repoID)

	return mapRowsOrNil(rows, err, "list pypi packages", pypiPackageFromRow)
}

func (p *PyPIStore) ListPyPIPackageNames(ctx context.Context, repoID string) ([]string, error) {
	names, err := p.q.ListPyPIPackageNames(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list pypi package names: %w", err)
	}

	return names, nil
}

func (p *PyPIStore) ListPyPIPackageVersions(ctx context.Context, repoID, name string) ([]*models.PyPIPackage, error) {
	rows, err := p.q.ListPyPIPackageVersions(ctx, dbsqlc.ListPyPIPackageVersionsParams{
		RepositoryID: repoID,
		Name:         name,
	})

	return mapRowsOrNil(rows, err, "list pypi package versions", pypiPackageFromRow)
}
