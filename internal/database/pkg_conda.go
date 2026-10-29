// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// CondaStore owns the Conda packages.
//
// Mapping onto the generic package tables:
//
//	packages          one per package name; name = name_key (case-sensitive)
//	package_versions  one per BUILD: version = version,
//	                  version_key = version-build_string|platform,
//	                  metadata = {build_string, platform}. The .tar.bz2 and the
//	                  .conda file of a build are two files of one version, so
//	                  retention ranks a build once.
//	package_files     one per file: path = platform/filename (live-unique),
//	                  filename = name-version-build_string<ext>, arch = platform,
//	                  attrs = {release (build string, omitted when empty),
//	                  extension, build_number, timestamp_ms, depends (order
//	                  kept), index_extra}, checksums = {md5} (omitted when
//	                  empty), role primary, no unit_key (conda is not
//	                  federated)
//
// Cleanup note: partition (package, arch = platform, part ”), as the former
// (name, platform) partition with the build as the ranked unit.
type CondaStore struct{ *core }

// condaVersionMeta is package_versions.metadata of a build.
type condaVersionMeta struct {
	BuildString string `json:"build_string"`
	Platform    string `json:"platform"`
}

// condaFileAttrs is package_files.attrs of a package file (one repodata.json
// record).
type condaFileAttrs struct {
	Release     string          `json:"release,omitempty"`
	Extension   string          `json:"extension"`
	BuildNumber int32           `json:"build_number"`
	TimestampMs int64           `json:"timestamp_ms"`
	Depends     []string        `json:"depends"`
	IndexExtra  json.RawMessage `json:"index_extra"`
}

func condaVersionKey(version, buildString, platform string) string {
	return version + "-" + buildString + "|" + platform
}

func condaFilePath(platform, filename string) string {
	return platform + "/" + filename
}

// CreateCondaPackage creates a new Conda package file. An empty
// pkg.Extension is stored as ".tar.bz2" and an empty pkg.Extra as "{}".
// A live file at the same platform/filename yields ErrDuplicate and is left
// untouched.
func (p *CondaStore) CreateCondaPackage(ctx context.Context, pkg *models.CondaPackage) error {
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

	attrs := condaFileAttrs{
		Release:     pkg.BuildString,
		Extension:   ext,
		BuildNumber: toInt32(pkg.BuildNumber),
		TimestampMs: pkg.Timestamp,
		Depends:     depends,
		IndexExtra:  extra,
	}

	var checksums map[string]string
	if pkg.MD5 != "" {
		checksums = map[string]string{"md5": pkg.MD5}
	}

	filename := pkg.Name + "-" + pkg.Version + "-" + pkg.BuildString + ext

	written, err := p.writePackage(ctx, &PackageWrite{
		RepoID:  pkg.RepositoryID,
		Format:  "conda",
		Name:    pkg.Name,
		NameKey: pkg.Name,
		Version: pkg.Version, VersionKey: condaVersionKey(pkg.Version, pkg.BuildString, pkg.Platform),
		VersionMeta: condaVersionMeta{BuildString: pkg.BuildString, Platform: pkg.Platform},
		CreatedAt:   pkg.CreatedAt,
		Files: []FileWrite{{
			Path:      condaFilePath(pkg.Platform, filename),
			Filename:  filename,
			Blob:      BlobWrite{SHA256: pkg.SHA256, Key: pkg.BlobKey, MediaType: "application/x-conda", Size: pkg.SizeBytes},
			Arch:      pkg.Platform,
			Attrs:     attrs,
			Checksums: checksums,
		}},
	})
	if err != nil {
		if errors.Is(err, ErrDuplicate) {
			return ErrDuplicate
		}

		return fmt.Errorf("failed to create conda package: %w", err)
	}

	f := written.Files[0]
	pkg.ID, pkg.Extension, pkg.CreatedAt, pkg.UpdatedAt = f.ID, ext, f.CreatedAt, f.UpdatedAt

	return nil
}

// GetCondaPackageFile returns the live package file with this exact
// name, version, build, platform and extension, or nil.
func (p *CondaStore) GetCondaPackageFile(
	ctx context.Context,
	repoID, name, version, buildString, platform, extension string,
) (*models.CondaPackage, error) {
	filename := name + "-" + version + "-" + buildString + extension

	row, err := p.q.GetCondaPackageFile(ctx, dbsqlc.GetCondaPackageFileParams{
		RepositoryID: repoID,
		Path:         condaFilePath(platform, filename),
		Name:         name,
		Version:      version,
	})
	if isNoRows(err) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get conda package: %w", err)
	}

	return condaRowToModel(row)
}

// condaPackageRow lists the sqlc row types condaRowToModel converts; they share the columns of GetCondaPackageFileRow.
type condaPackageRow interface {
	dbsqlc.GetCondaPackageFileRow | dbsqlc.ListCondaPackagesRow
}

func condaRowToModel[R condaPackageRow](in R) (*models.CondaPackage, error) {
	row := dbsqlc.GetCondaPackageFileRow(in)

	var (
		attrs condaFileAttrs
		meta  condaVersionMeta
		sums  map[string]string
	)

	if err := json.Unmarshal(row.Attrs, &attrs); err != nil {
		return nil, fmt.Errorf("decode conda file attrs: %w", err)
	}

	if err := json.Unmarshal(row.Metadata, &meta); err != nil {
		return nil, fmt.Errorf("decode conda build metadata: %w", err)
	}

	if err := json.Unmarshal(row.Checksums, &sums); err != nil {
		return nil, fmt.Errorf("decode conda checksums: %w", err)
	}

	return &models.CondaPackage{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Name:         row.Name,
		Version:      row.Version,
		BuildString:  meta.BuildString,
		Platform:     row.Arch,
		Extension:    attrs.Extension,
		BuildNumber:  int(attrs.BuildNumber),
		Timestamp:    attrs.TimestampMs,
		Depends:      attrs.Depends,
		Extra:        attrs.IndexExtra,
		BlobKey:      row.BlobKey,
		SizeBytes:    row.SizeBytes,
		SHA256:       row.Sha256,
		MD5:          sums["md5"],
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

// DeleteCondaPackage hard-deletes one package file and, when it was the last
// file of its build, the build and the package.
func (p *CondaStore) DeleteCondaPackage(ctx context.Context, id string) error {
	repoID, err := p.q.GetCondaFileRepo(ctx, id)
	if err != nil {
		if isNoRows(err) {
			return nil
		}

		return fmt.Errorf("failed to delete conda package: %w", err)
	}

	if _, err := p.deleteFiles(ctx, repoID, []string{id}); err != nil {
		return fmt.Errorf("failed to delete conda package: %w", err)
	}

	return nil
}

// ListCondaPackages lists the live package files of one platform (subdir).
func (p *CondaStore) ListCondaPackages(ctx context.Context, repoID, platform string) ([]*models.CondaPackage, error) {
	rows, err := p.q.ListCondaPackages(ctx, dbsqlc.ListCondaPackagesParams{
		RepositoryID: repoID,
		Arch:         platform,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query conda packages: %w", err)
	}

	return mapRowsErr(rows, condaRowToModel)
}
