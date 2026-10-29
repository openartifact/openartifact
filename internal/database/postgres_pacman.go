// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// pacmanMediaType maps the compression of a package file to the media type of
// its blob.
func pacmanMediaType(filename string) string {
	switch {
	case strings.HasSuffix(filename, ".zst"):
		return "application/zstd"
	case strings.HasSuffix(filename, ".xz"):
		return "application/x-xz"
	case strings.HasSuffix(filename, ".gz"):
		return "application/gzip"
	default:
		return ""
	}
}

// pacmanRow lists the sqlc row types pacmanFromRow converts; they share the columns of ListPacmanPackagesRow.
type pacmanRow interface {
	dbsqlc.ListPacmanPackagesRow | dbsqlc.GetPacmanPackageByFilenameRow
}

func pacmanFromRow[R pacmanRow](in R) *models.PacmanPackage {
	row := dbsqlc.ListPacmanPackagesRow(in)

	return &models.PacmanPackage{
		ID:              row.ID,
		RepositoryID:    row.RepositoryID,
		Name:            row.Name,
		Base:            row.Base,
		Version:         row.Version,
		Arch:            row.Arch,
		RepoArch:        row.RepoArch,
		Filename:        row.Filename,
		BlobKey:         row.BlobKey,
		SizeBytes:       row.SizeBytes,
		SHA256:          row.Sha256,
		MD5:             row.Md5,
		Description:     row.Description,
		URL:             row.Url,
		Packager:        row.Packager,
		BuildDate:       row.BuildDate,
		InstalledSize:   row.InstalledSize,
		Licenses:        row.Licenses,
		Groups:          row.PackageGroups,
		Replaces:        row.Replaces,
		Conflicts:       row.Conflicts,
		Provides:        row.Provides,
		Depends:         row.Depends,
		OptDepends:      row.Optdepends,
		MakeDepends:     row.Makedepends,
		CheckDepends:    row.Checkdepends,
		Signature:       row.Signature,
		SignatureSource: row.SignatureSource,
		SignatureKey:    row.SignatureKey,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

// CreatePacmanPackage stores one package file. A live row with the same file
// name, or the same name, version and architecture, is a unique violation.
func (p *PostgresDB) CreatePacmanPackage(ctx context.Context, pkg *models.PacmanPackage) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, pkg.SHA256, pkg.BlobKey, pacmanMediaType(pkg.Filename), pkg.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for pacman package: %w", err)
	}

	row, err := p.q.CreatePacmanPackage(ctx, dbsqlc.CreatePacmanPackageParams{
		RepositoryID:    pkg.RepositoryID,
		Name:            pkg.Name,
		Base:            pkg.Base,
		Version:         pkg.Version,
		Arch:            pkg.Arch,
		RepoArch:        pkg.RepoArch,
		Filename:        pkg.Filename,
		BlobID:          blobID,
		Md5:             pkg.MD5,
		Description:     pkg.Description,
		Url:             pkg.URL,
		Packager:        pkg.Packager,
		BuildDate:       pkg.BuildDate,
		InstalledSize:   pkg.InstalledSize,
		Licenses:        nonNilStrings(pkg.Licenses),
		PackageGroups:   nonNilStrings(pkg.Groups),
		Replaces:        nonNilStrings(pkg.Replaces),
		Conflicts:       nonNilStrings(pkg.Conflicts),
		Provides:        nonNilStrings(pkg.Provides),
		Depends:         nonNilStrings(pkg.Depends),
		Optdepends:      nonNilStrings(pkg.OptDepends),
		Makedepends:     nonNilStrings(pkg.MakeDepends),
		Checkdepends:    nonNilStrings(pkg.CheckDepends),
		FileList:        pkg.FileList,
		Signature:       pkg.Signature,
		SignatureSource: pkg.SignatureSource,
		SignatureKey:    pkg.SignatureKey,
		CreatedAt:       createdAtOrNil(pkg.CreatedAt),
	})

	return finishCreate(insertedRow(row), err, "create pacman package", &pkg.ID, &pkg.CreatedAt, &pkg.UpdatedAt)
}

// GetPacmanPackageByFilename returns the live package published under the file
// name, or (nil, nil).
func (p *PostgresDB) GetPacmanPackageByFilename(ctx context.Context, repoID, filename string) (*models.PacmanPackage, error) {
	row, err := p.q.GetPacmanPackageByFilename(ctx, dbsqlc.GetPacmanPackageByFilenameParams{RepositoryID: repoID, Filename: filename})

	return mapRow(row, err, "get pacman package", pacmanFromRow)
}

// ListPacmanPackages returns every live package of the repository, without file lists.
func (p *PostgresDB) ListPacmanPackages(ctx context.Context, repoID string) ([]*models.PacmanPackage, error) {
	rows, err := p.q.ListPacmanPackages(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list pacman packages: %w", err)
	}

	pkgs := make([]*models.PacmanPackage, len(rows))
	for i, row := range rows {
		pkgs[i] = pacmanFromRow(row)
	}

	return pkgs, nil
}

// ListPacmanFileLists returns the file lists of the live packages that have one.
func (p *PostgresDB) ListPacmanFileLists(ctx context.Context, repoID string) (map[string]string, error) {
	rows, err := p.q.ListPacmanFileLists(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list pacman file lists: %w", err)
	}

	lists := make(map[string]string, len(rows))

	for _, row := range rows {
		if row.FileList != nil {
			lists[row.ID] = *row.FileList
		}
	}

	return lists, nil
}

// ListPacmanPackagesWithoutFileList returns the live packages that still need
// their file list read from the blob.
func (p *PostgresDB) ListPacmanPackagesWithoutFileList(ctx context.Context, repoID string) ([]*models.PacmanPackage, error) {
	rows, err := p.q.ListPacmanPackagesWithoutFileList(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list pacman packages without file list: %w", err)
	}

	pkgs := make([]*models.PacmanPackage, len(rows))
	for i, row := range rows {
		pkgs[i] = &models.PacmanPackage{ID: row.ID, RepositoryID: repoID, BlobKey: row.BlobKey, Filename: row.Filename}
	}

	return pkgs, nil
}

// SetPacmanFileList records the file list of a package that has none.
func (p *PostgresDB) SetPacmanFileList(ctx context.Context, repoID, id, fileList string) error {
	if err := p.q.SetPacmanFileList(ctx, dbsqlc.SetPacmanFileListParams{ID: id, RepositoryID: repoID, FileList: &fileList}); err != nil {
		return fmt.Errorf("failed to set pacman file list: %w", err)
	}

	return nil
}

// SetPacmanSignature records the signature of a package file.
func (p *PostgresDB) SetPacmanSignature(ctx context.Context, repoID, id, signature, source, key string) error {
	err := p.q.SetPacmanSignature(ctx, dbsqlc.SetPacmanSignatureParams{
		ID: id, RepositoryID: repoID, Signature: signature, SignatureSource: source, SignatureKey: key,
	})
	if err != nil {
		return fmt.Errorf("failed to set pacman signature: %w", err)
	}

	return nil
}

// DeletePacmanPackage removes a live package row.
func (p *PostgresDB) DeletePacmanPackage(ctx context.Context, repoID, id string) error {
	if err := p.q.DeletePacmanPackage(ctx, dbsqlc.DeletePacmanPackageParams{ID: id, RepositoryID: repoID}); err != nil {
		return fmt.Errorf("failed to delete pacman package: %w", err)
	}

	return nil
}
