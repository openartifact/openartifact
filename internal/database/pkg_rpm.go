// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// RPMStore owns the RPM format tables.
//
// An rpm package lives on the generic package tables (packages P, package_versions V,
// package_files F) plus the side table rpm_files. Grain 1:1: one .rpm (name, version,
// release, epoch, arch) is one P (shared by name), one V and one F. The mapping:
//
//	P  name, name_key = Name; attrs = {}
//	V  version        = Version (no release, no epoch)
//	   version_key    = version-release:epoch|arch (the part before '|' is the cleanup
//	                    ordering text; with the arch it is the whole legacy live key)
//	   metadata       = {summary, description, license, url, vendor, group_name, packager,
//	                    source_rpm, build_host} (empty = absent)
//	F  path           = filename, prefixed with '<epoch>:' when epoch is not 0; filename is
//	                    the canonical name-version-release.arch.rpm, rendered from the
//	                    columns when empty
//	   filename, arch = Filename, Arch; blob_id = the .rpm blob
//	   size_bytes     = package_size (wins over the blob size in listings; NULL = 0)
//	   part           = ''
//	   attrs          = {release, epoch, build_time, archive_size, installed_size}, a key only
//	                    when it is not ''/0; build_time is whole unix seconds
//	   checksums      = {}
//	rpm_files (id = F.id): header_start, header_end, rpm_extra (the parsed header extras,
//	   stored exactly as parsed and copied as-is; never reset or re-parse it)
type RPMStore struct{ *core }

func (p *RPMStore) GetRPMRepo(ctx context.Context, repositoryID string) (*models.RPMRepo, error) {
	row, err := p.q.GetRPMRepo(ctx, repositoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get rpm repo: %w", err)
	}

	return &models.RPMRepo{
		ID:            row.ID,
		RepositoryID:  row.RepositoryID,
		BaseArch:      row.BaseArch,
		MetadataStale: row.MetadataStale,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}, nil
}

func (p *RPMStore) CreateRPMRepo(ctx context.Context, repo *models.RPMRepo) error {
	row, err := p.q.CreateRPMRepo(ctx, dbsqlc.CreateRPMRepoParams{
		RepositoryID: repo.RepositoryID,
		BaseArch:     repo.BaseArch,
	})

	return finishCreate(insertedRow(row), err, "create rpm repo", &repo.ID, &repo.CreatedAt, &repo.UpdatedAt)
}

// MarkRepoStale marks repository metadata as stale for regeneration.
// NOTE: Currently only tracks staleness for RPM repos via rpm_repos table.
// DEB staleness tracking needs a separate mechanism (e.g., a stale flag on repositories table).
func (p *RPMStore) MarkRepoStale(ctx context.Context, repositoryID string) error {
	return p.q.MarkRepoStale(ctx, repositoryID)
}

// rpmVersionKey is the identity of an rpm inside its package: the legacy
// ordering text "version-release:epoch" and the architecture.
func rpmVersionKey(version, release string, epoch int, arch string) string {
	return version + "-" + release + ":" + strconv.Itoa(epoch) + "|" + arch
}

// CreateRpmPackage inserts an RPM package. A live package with the same NEVRA.arch
// or the same path is ErrDuplicate.
func (p *RPMStore) CreateRpmPackage(ctx context.Context, pkg *models.Component) error {
	path := pkg.Filename
	if path == "" {
		path = pkg.Name + "-" + pkg.Version + "-" + pkg.Release + "." + pkg.Arch + ".rpm"
	}

	if pkg.Epoch != 0 {
		path = strconv.Itoa(pkg.Epoch) + ":" + path
	}

	attrs := map[string]any{}
	if pkg.Release != "" {
		attrs["release"] = pkg.Release
	}

	putNonZero(attrs, "epoch", int64(pkg.Epoch))
	putNonZero(attrs, "build_time", pkg.BuildTime)
	putNonZero(attrs, "archive_size", pkg.ArchiveSize)
	putNonZero(attrs, "installed_size", pkg.InstalledSize)

	var size *int64
	if pkg.PackageSize != 0 {
		size = &pkg.PackageSize
	}

	extra := pkg.RPMExtra
	if len(extra) == 0 {
		extra = []byte(`{}`)
	}

	written, err := p.writeOSPackage(ctx, &PackageWrite{
		RepoID:     pkg.RepositoryID,
		Format:     "rpm",
		Name:       pkg.Name,
		NameKey:    pkg.Name,
		Version:    pkg.Version,
		VersionKey: rpmVersionKey(pkg.Version, pkg.Release, pkg.Epoch, pkg.Arch),
		VersionMeta: nonEmptyStrings(map[string]string{
			"summary":     pkg.Summary,
			"description": pkg.Description,
			"license":     pkg.License,
			"url":         pkg.URL,
			"vendor":      pkg.Vendor,
			"group_name":  pkg.Group,
			"packager":    pkg.Packager,
			"source_rpm":  pkg.SourceRPM,
			"build_host":  pkg.BuildHost,
		}),
		Exclusive: true,
		CreatedAt: pkg.CreatedAt,
		Files: []FileWrite{{
			Path:      path,
			Filename:  pkg.Filename,
			SizeBytes: size,
			Arch:      pkg.Arch,
			Attrs:     attrs,
		}},
	}, []*string{strOrNil(pkg.ArtifactID)}, func(q *dbsqlc.Queries, files []FileWritten) error {
		if err := q.InsertRpmFile(ctx, dbsqlc.InsertRpmFileParams{
			ID: files[0].ID, HeaderStart: pkg.HeaderStart, HeaderEnd: pkg.HeaderEnd, RpmExtra: extra,
		}); err != nil {
			return fmt.Errorf("insert rpm file: %w", err)
		}

		return nil
	})
	if err != nil {
		return err
	}

	pkg.ID, pkg.CreatedAt, pkg.UpdatedAt = written.Files[0].ID, written.Files[0].CreatedAt, written.Files[0].UpdatedAt

	return nil
}

// GetRpmPackageByID fetches a live RPM package by its primary key, or (nil, nil).
func (p *RPMStore) GetRpmPackageByID(ctx context.Context, id string) (*models.RpmPackage, error) {
	row, err := p.q.GetRpmPackageByID(ctx, id)

	return mapRow(row, err, "get rpm package", func(row dbsqlc.GetRpmPackageByIDRow) *models.RpmPackage {
		pkg := &models.RpmPackage{
			ID:            row.ID,
			RepositoryID:  row.RepositoryID,
			Name:          row.Name,
			Version:       row.Version,
			Release:       row.Release,
			Epoch:         int(row.Epoch),
			Arch:          row.Arch,
			Summary:       row.Summary,
			Description:   row.Description,
			License:       row.License,
			URL:           row.Url,
			Vendor:        row.Vendor,
			GroupName:     row.GroupName,
			Packager:      row.Packager,
			SourceRPM:     row.SourceRpm,
			BuildHost:     row.BuildHost,
			Filename:      row.Filename,
			PackageSize:   row.PackageSize,
			InstalledSize: row.InstalledSize,
			ArchiveSize:   row.ArchiveSize,
			CreatedAt:     row.CreatedAt,
			UpdatedAt:     row.UpdatedAt,
		}
		if row.BlobID != nil {
			pkg.BlobID = *row.BlobID
		}

		if row.BuildTime != 0 {
			t := time.Unix(row.BuildTime, 0)
			pkg.BuildTime = &t
		}

		return pkg
	})
}
