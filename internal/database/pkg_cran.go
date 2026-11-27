// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// CRANStore owns the R packages of the cran repositories.
//
// Mapping of an R package file onto the generic package tables (one package
// version has one file per tree it was uploaded to: the source tarball and
// optionally a Windows zip and macOS tgz per R release):
//
//	packages          name = name_key = R package name (case-sensitive), attrs {}
//	package_versions  version = version_key = the version as written
//	                  ("1.2-3"); metadata {title, description, license,
//	                  maintainer, url} of the DESCRIPTION last uploaded as a
//	                  source package
//	package_files     path = the canonical repository path
//	                  (src/contrib/<pkg>_<ver>.tar.gz,
//	                  bin/windows/contrib/<R>/<pkg>_<ver>.zip, ...), filename
//	                  <pkg>_<ver>.<ext>, arch = the tree's directory (the
//	                  PACKAGES it is listed in), attrs = the standard PACKAGES
//	                  fields of the DESCRIPTION (Depends, Imports, License, ...),
//	                  checksums {md5}, role primary
//
// A version is not exclusive: a binary joins the version of its source. Writing
// a path that is live is a duplicate (ErrDuplicate). Deleting the last file of a
// version removes the version.
type CRANStore struct{ *core }

// cranMetadata is package_versions.metadata of an R package version.
type cranMetadata struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	License     string `json:"license,omitempty"`
	Maintainer  string `json:"maintainer,omitempty"`
	URL         string `json:"url,omitempty"`
}

// CreateCRANFile stores one package file. replaceMeta makes the file's
// descriptive metadata replace that of an existing version (a source package
// does; a binary keeps the version's).
func (p *CRANStore) CreateCRANFile(ctx context.Context, f *models.CRANFile, replaceMeta bool) error {
	out, err := p.writePackage(ctx, &PackageWrite{
		RepoID:     f.RepositoryID,
		Format:     "cran",
		Name:       f.Package,
		NameKey:    f.Package,
		Version:    f.Version,
		VersionKey: f.Version,
		VersionMeta: cranMetadata{
			Title: f.Title, Description: f.Description, License: f.License, Maintainer: f.Maintainer, URL: f.URL,
		},
		ReplaceVersionMeta: replaceMeta,
		CreatedAt:          f.CreatedAt,
		Files: []FileWrite{{
			Path:     f.Path,
			Filename: f.Filename,
			Blob: BlobWrite{
				SHA256: f.Digest, Key: f.BlobKey, MediaType: cranMediaType(f.Path), Size: f.SizeBytes,
			},
			Arch:      f.Tree,
			Attrs:     f.Fields,
			Checksums: map[string]string{"md5": f.MD5},
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to create cran file: %w", err)
	}

	f.ID, f.CreatedAt, f.UpdatedAt = out.Files[0].ID, out.Files[0].CreatedAt, out.Files[0].UpdatedAt

	return nil
}

func cranMediaType(path string) string {
	if len(path) > 4 && path[len(path)-4:] == ".zip" {
		return "application/zip"
	}

	return "application/gzip"
}

// cranFileRow lists the sqlc row types cranFileFromRow converts; they share the columns of GetCRANFileRow.
type cranFileRow interface {
	dbsqlc.GetCRANFileRow | dbsqlc.ListCRANTreeFilesRow | dbsqlc.ListCRANPackageFilesRow
}

func cranFileFromRow[R cranFileRow](in R) *models.CRANFile {
	row := dbsqlc.GetCRANFileRow(in)

	var (
		meta   cranMetadata
		fields map[string]string
		sums   map[string]string
	)

	_ = json.Unmarshal(row.Metadata, &meta)
	_ = json.Unmarshal(row.Attrs, &fields)
	_ = json.Unmarshal(row.Checksums, &sums)

	return &models.CRANFile{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Package:      row.Name,
		Version:      row.Version,
		Path:         row.Path,
		Filename:     row.Filename,
		Tree:         row.Arch,
		Fields:       fields,
		MD5:          sums["md5"],
		Title:        meta.Title,
		Description:  meta.Description,
		License:      meta.License,
		Maintainer:   meta.Maintainer,
		URL:          meta.URL,
		BlobKey:      row.BlobKey,
		Digest:       row.Digest,
		SizeBytes:    row.SizeBytes,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

// GetCRANFile returns the live file at a canonical repository path, nil when
// there is none.
func (p *CRANStore) GetCRANFile(ctx context.Context, repoID, path string) (*models.CRANFile, error) {
	row, err := p.q.GetCRANFile(ctx, dbsqlc.GetCRANFileParams{RepositoryID: repoID, Path: path})

	return mapRow(row, err, "get cran file", cranFileFromRow)
}

// ListCRANTreeFiles returns the live files of one tree (the directory of its
// PACKAGES), ordered by package name and path.
func (p *CRANStore) ListCRANTreeFiles(ctx context.Context, repoID, tree string) ([]*models.CRANFile, error) {
	rows, err := p.q.ListCRANTreeFiles(ctx, dbsqlc.ListCRANTreeFilesParams{RepositoryID: repoID, Arch: tree})

	return mapRowsOrNil(rows, err, "list cran tree files", cranFileFromRow)
}

// ListCRANPackageFiles returns the live files of one package in every tree.
func (p *CRANStore) ListCRANPackageFiles(ctx context.Context, repoID, pkg string) ([]*models.CRANFile, error) {
	rows, err := p.q.ListCRANPackageFiles(ctx, dbsqlc.ListCRANPackageFilesParams{RepositoryID: repoID, Name: pkg})

	return mapRowsOrNil(rows, err, "list cran package files", cranFileFromRow)
}

// ListCRANPackageNames returns the names of the packages with a live file in
// the repository, in order.
func (p *CRANStore) ListCRANPackageNames(ctx context.Context, repoID string) ([]string, error) {
	names, err := p.q.ListCRANPackageNames(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list cran packages: %w", err)
	}

	return names, nil
}

// DeleteCRANFile hard-deletes a live file; its version and package go with
// their last file.
func (p *CRANStore) DeleteCRANFile(ctx context.Context, repoID, fileID string) error {
	if _, err := p.deleteFiles(ctx, repoID, []string{fileID}); err != nil {
		return fmt.Errorf("failed to delete cran file: %w", err)
	}

	return nil
}

// CRANIndexStamp is a cheap validator of every PACKAGES index and listing of a
// repository: it changes exactly when a live file is added or removed.
func (p *CRANStore) CRANIndexStamp(ctx context.Context, repoID string) (string, error) {
	stamp, err := p.q.CRANIndexStamp(ctx, repoID)
	if err != nil {
		return "", fmt.Errorf("failed to stamp cran index: %w", err)
	}

	return stamp, nil
}
