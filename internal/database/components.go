// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// ComponentStore reads and deletes the deb and rpm package rows as
// models.Component. Both formats live on the generic tables; the component is
// assembled from the file (package_files), its version (package_versions: the
// descriptive metadata) and its package (packages: the name), exactly as the
// rpm and deb specs map them:
//
//	Name                      = packages.name
//	Version                   = package_versions.version
//	Summary, Description,
//	License, URL, Vendor,
//	Group, Packager,
//	SourceRPM, BuildHost      = package_versions.metadata keys summary, description,
//	                            license, url, vendor, group_name, packager,
//	                            source_rpm, build_host (absent = '')
//	Release, Epoch,
//	BuildTime, ArchiveSize,
//	InstalledSize             = package_files.attrs keys of that name (absent = ''/0)
//	Arch, Filename            = package_files.arch, filename
//	PackageSize               = COALESCE(package_files.size_bytes, blob size, 0)
//
// The header range and the parsed extras (rpm_files) are bulk data the readers
// of a component never use, so they are not loaded here.
type ComponentStore struct{ *core }

// componentSelect selects one component per live deb or rpm file (aliases f,
// v, pk; LEFT JOIN blobs for sha256 and size).
const componentSelect = `SELECT f.id, f.repository_id, COALESCE(b.sha256, ''), pk.format, pk.name, v.version,
		       COALESCE(f.attrs->>'release', ''), COALESCE((f.attrs->>'epoch')::int, 0), f.arch,
		       COALESCE(v.metadata->>'summary', ''), COALESCE(v.metadata->>'description', ''), COALESCE(v.metadata->>'license', ''),
		       COALESCE(v.metadata->>'url', ''), COALESCE(v.metadata->>'vendor', ''), COALESCE(v.metadata->>'group_name', ''),
		       COALESCE(v.metadata->>'packager', ''), COALESCE(v.metadata->>'source_rpm', ''), COALESCE(v.metadata->>'build_host', ''),
		       COALESCE((f.attrs->>'build_time')::bigint, 0),
		       COALESCE(f.size_bytes, b.size_bytes, 0), COALESCE((f.attrs->>'installed_size')::bigint, 0),
		       COALESCE((f.attrs->>'archive_size')::bigint, 0),
		       f.filename, f.created_at, f.updated_at
		FROM package_files f
		JOIN package_versions v ON v.id = f.version_id
		JOIN packages pk ON pk.id = v.package_id
		LEFT JOIN blobs b ON b.id = f.blob_id
		WHERE f.deleted_at IS NULL AND pk.format IN ('deb', 'rpm')`

// scanComponent scans one componentSelect row.
func scanComponent(row rowScanner) (models.Component, error) {
	var c models.Component

	err := row.Scan(
		&c.ID, &c.RepositoryID, &c.SHA256, &c.Format, &c.Name, &c.Version,
		&c.Release, &c.Epoch, &c.Arch,
		&c.Summary, &c.Description, &c.License,
		&c.URL, &c.Vendor, &c.Group,
		&c.Packager, &c.SourceRPM, &c.BuildHost,
		&c.BuildTime,
		&c.PackageSize, &c.InstalledSize, &c.ArchiveSize,
		&c.Filename, &c.CreatedAt, &c.UpdatedAt,
	)

	return c, err //nolint:wrapcheck // callers wrap with their own context and test sql.ErrNoRows
}

// getComponentWhere finds the first live deb or rpm file matching where
// (written against the aliases f, v, pk and b). what names the lookup in the
// error message.
func (p *ComponentStore) getComponentWhere(ctx context.Context, what, where string, args ...any) (*models.Component, error) {
	c, err := scanComponent(p.db.QueryRowContext(ctx, componentSelect+" AND "+where+" LIMIT 1", args...))
	if err != nil {
		return orNotFound[models.Component](nil, err, "get component by "+what)
	}

	return &c, nil
}

// GetComponentByID looks up a deb or rpm package by its file id.
func (p *ComponentStore) GetComponentByID(ctx context.Context, id string) (*models.Component, error) {
	return p.getComponentWhere(ctx, "id", "f.id = $1", id)
}

// GetComponentByFilename looks up a live package of a repository by its file name.
func (p *ComponentStore) GetComponentByFilename(ctx context.Context, repoID string, filename string) (*models.Component, error) {
	return p.getComponentWhere(ctx, "filename", "f.repository_id = $1 AND f.filename = $2", repoID, filename)
}

// GetComponentBySHA256 finds a live deb or rpm package in a repository by the
// sha256 of its blob.
func (p *ComponentStore) GetComponentBySHA256(ctx context.Context, repoID, sha256 string) (*models.Component, error) {
	return p.getComponentWhere(ctx, "sha256", "f.repository_id = $1 AND b.sha256 = $2", repoID, sha256)
}

// GetComponentsByRepo lists the RPM packages of a repository.
func (p *ComponentStore) GetComponentsByRepo(ctx context.Context, repoID string) ([]models.Component, error) {
	rows, err := p.db.QueryContext(ctx, componentSelect+" AND f.repository_id = $1 AND pk.format = 'rpm'", repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to query components: %w", err)
	}

	return collectRows(rows, "failed to scan component", "failed to iterate components", scanComponent)
}

// DeleteComponent hard-deletes one live deb or rpm file (its side row goes
// with it) and the version and package it leaves empty. A missing id is not an
// error.
func (p *ComponentStore) DeleteComponent(ctx context.Context, componentID string) error {
	var repoID string

	err := p.db.QueryRowContext(ctx,
		`SELECT f.repository_id::text FROM package_files f JOIN package_versions v ON v.id = f.version_id
		 JOIN packages pk ON pk.id = v.package_id
		 WHERE f.id = $1 AND f.deleted_at IS NULL AND pk.format IN ('deb', 'rpm')`, componentID).Scan(&repoID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}

		return fmt.Errorf("find component to delete: %w", err)
	}

	if _, err := p.deleteFiles(ctx, repoID, []string{componentID}); err != nil {
		return fmt.Errorf("delete component: %w", err)
	}

	return nil
}
