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

// PyPIStore owns the PyPI distributions.
//
// Mapping onto the generic package tables (reference: one distribution file
// is one package_files row):
//
//	packages          one per PEP 503 normalised project name; name = name_key
//	                  = the name the handler stores, attrs = {}
//	package_versions  one per release: version = version_key = version;
//	                  metadata = {summary, author, author_email, license,
//	                  keywords, classifiers} of the release (last upload wins;
//	                  a further file of a release rewrites the same keys)
//	package_files     one per distribution file: path = name/version/filename
//	                  (live-unique, so a duplicate upload is ErrDuplicate),
//	                  filename = filename, arch = python tag ('' when none),
//	                  unit_key = path (the federation unit is the file),
//	                  attrs = {package_type, python_version, requires_python},
//	                  checksums = {} (only the blob's sha256 is recorded)
//
// Cleanup note: the former retention partition was the package name only;
// the generic partition (package, arch, part) splits the files of a release by
// python tag, so pypi needs the "package only" partition from the cleanup
// engine.
type PyPIStore struct{ *core }

// pypiVersionMeta is package_versions.metadata of a release.
type pypiVersionMeta struct {
	Summary     *string  `json:"summary"`
	Author      *string  `json:"author"`
	AuthorEmail *string  `json:"author_email"`
	License     *string  `json:"license"`
	Keywords    *string  `json:"keywords"`
	Classifiers []string `json:"classifiers"`
}

// pypiFileAttrs is package_files.attrs of a distribution file.
type pypiFileAttrs struct {
	PackageType    string  `json:"package_type"`
	PythonVersion  *string `json:"python_version"`
	RequiresPython *string `json:"requires_python"`
}

func pypiPath(name, version, filename string) string {
	return name + "/" + version + "/" + filename
}

// CreatePyPIPackage records one distribution file and its release. A live file
// of the same name, version and filename yields ErrDuplicate and nothing is
// written. pkg.MD5Digest does not round-trip: only the blob's sha256 is kept.
func (p *PyPIStore) CreatePyPIPackage(ctx context.Context, pkg *models.PyPIPackage) error {
	path := pypiPath(pkg.Name, pkg.Version, pkg.Filename)

	written, err := p.writePackage(ctx, &PackageWrite{
		RepoID:  pkg.RepositoryID,
		Format:  "pypi",
		Name:    pkg.Name,
		NameKey: pkg.Name,
		Version: pkg.Version, VersionKey: pkg.Version,
		VersionMeta: pypiVersionMeta{
			Summary:     strPtr(pkg.Summary),
			Author:      strPtr(pkg.Author),
			AuthorEmail: strPtr(pkg.AuthorEmail),
			License:     strPtr(pkg.License),
			Keywords:    strPtr(pkg.Keywords),
			Classifiers: pkg.Classifiers,
		},
		ReplaceVersionMeta: true,
		CreatedAt:          pkg.CreatedAt,
		Files: []FileWrite{{
			Path:     path,
			Filename: pkg.Filename,
			Blob:     BlobWrite{SHA256: pkg.SHA256Digest, Key: pkg.BlobKey, MediaType: "application/zip", Size: pkg.SizeBytes},
			Arch:     pkg.PythonVersion,
			Attrs: pypiFileAttrs{
				PackageType:    pkg.PackageType,
				PythonVersion:  strPtr(pkg.PythonVersion),
				RequiresPython: strPtr(pkg.RequiresPython),
			},
			UnitKey: path,
		}},
	})
	if err != nil {
		return fmt.Errorf("create pypi package: %w", err)
	}

	f := written.Files[0]
	pkg.ID, pkg.CreatedAt, pkg.UpdatedAt = f.ID, f.CreatedAt, f.UpdatedAt

	return nil
}

// pypiPackageRow lists the sqlc row types pypiPackageFromRow converts; they share the columns of GetPyPIPackagesByFilenameRow.
type pypiPackageRow interface {
	dbsqlc.GetPyPIPackagesByFilenameRow | dbsqlc.ListPyPIPackageVersionsRow
}

func pypiPackageFromRow[R pypiPackageRow](in R) (*models.PyPIPackage, error) {
	row := dbsqlc.GetPyPIPackagesByFilenameRow(in)

	var (
		attrs pypiFileAttrs
		meta  pypiVersionMeta
	)

	if err := json.Unmarshal(row.Attrs, &attrs); err != nil {
		return nil, fmt.Errorf("decode pypi file attrs: %w", err)
	}

	if err := json.Unmarshal(row.Metadata, &meta); err != nil {
		return nil, fmt.Errorf("decode pypi release metadata: %w", err)
	}

	return &models.PyPIPackage{
		ID:             row.ID,
		RepositoryID:   row.RepositoryID,
		Name:           row.Name,
		Version:        row.Version,
		Filename:       row.Filename,
		PackageType:    attrs.PackageType,
		PythonVersion:  derefStr(attrs.PythonVersion),
		RequiresPython: derefStr(attrs.RequiresPython),
		Summary:        derefStr(meta.Summary),
		Author:         derefStr(meta.Author),
		AuthorEmail:    derefStr(meta.AuthorEmail),
		License:        derefStr(meta.License),
		Keywords:       derefStr(meta.Keywords),
		Classifiers:    meta.Classifiers,
		BlobKey:        row.BlobKey,
		SHA256Digest:   row.Sha256Digest,
		SizeBytes:      row.SizeBytes,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}

// GetPyPIPackageByFilename returns the live file with this filename, nil
// when there is none and ErrAmbiguousPyPIFilename when several packages of
// the repository hold a file of that name.
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

	return pypiPackageFromRow(rows[0])
}

// DeletePyPIPackage hard-deletes one distribution file and, when it was the
// last one, its release and project.
func (p *PyPIStore) DeletePyPIPackage(ctx context.Context, repoID, name, version, filename string) error {
	id, err := p.q.GetPyPIFileID(ctx, dbsqlc.GetPyPIFileIDParams{RepositoryID: repoID, Path: pypiPath(name, version, filename)})
	if err != nil {
		if isNoRows(err) {
			return nil
		}

		return fmt.Errorf("failed to delete pypi package: %w", err)
	}

	if _, err := p.deleteFiles(ctx, repoID, []string{id}); err != nil {
		return fmt.Errorf("failed to delete pypi package: %w", err)
	}

	return nil
}

// ListPyPIPackageNames returns the stored project names of a repository that
// have a live release, sorted, without loading any file row.
func (p *PyPIStore) ListPyPIPackageNames(ctx context.Context, repoID string) ([]string, error) {
	names, err := p.q.ListPyPIPackageNames(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list pypi package names: %w", err)
	}

	return names, nil
}

// ListPyPIPackageVersions returns the live files of one project by its
// normalised name, newest version first.
func (p *PyPIStore) ListPyPIPackageVersions(ctx context.Context, repoID, name string) ([]*models.PyPIPackage, error) {
	rows, err := p.q.ListPyPIPackageVersions(ctx, dbsqlc.ListPyPIPackageVersionsParams{
		RepositoryID: repoID,
		NameKey:      name,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pypi package versions: %w", err)
	}

	return mapRowsErr(rows, pypiPackageFromRow)
}

// mapRowsErr converts rows with a decoding mapper; no rows give nil.
func mapRowsErr[R, T any](rows []R, mapf func(R) (*T, error)) ([]*T, error) {
	if len(rows) == 0 {
		return nil, nil
	}

	out := make([]*T, 0, len(rows))

	for _, r := range rows {
		v, err := mapf(r)
		if err != nil {
			return nil, err
		}

		out = append(out, v)
	}

	return out, nil
}
