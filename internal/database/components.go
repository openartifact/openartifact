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

// ComponentStore owns the components table shared by the deb and rpm package rows.
type ComponentStore struct{ *core }

// rpmComponentDest lists the scan targets of an rpmComponentColumns row.
func rpmComponentDest(c *models.Component) []any {
	return []any{
		&c.ID, &c.RepositoryID, &c.SHA256, &c.Name, &c.Version,
		&c.Release, &c.Epoch, &c.Arch, &c.Summary, &c.Description, &c.License,
		&c.URL, &c.Vendor, &c.Group, &c.Packager, &c.SourceRPM, &c.BuildHost, &c.BuildTime,
		&c.PackageSize, &c.InstalledSize, &c.ArchiveSize, &c.Filename, &c.CreatedAt, &c.UpdatedAt,
	}
}

// scanRpmComponent scans one rpmComponentColumns row into a models.Component.
func scanRpmComponent(row rowScanner) (*models.Component, error) {
	c := &models.Component{Format: "rpm"}
	if err := row.Scan(rpmComponentDest(c)...); err != nil {
		return nil, err //nolint:wrapcheck // callers wrap with their own context and test sql.ErrNoRows
	}

	return c, nil
}

// scanDebComponent scans one debComponentColumns row into a models.Component.
// deb_packages has no rpm-only columns (release, epoch, source_rpm, ...); those
// fields are left at their zero value.
func scanDebComponent(row rowScanner) (*models.Component, error) {
	c := &models.Component{Format: "deb"}
	if err := row.Scan(
		&c.ID, &c.RepositoryID, &c.SHA256, &c.Name, &c.Version,
		&c.Arch, &c.Summary, &c.Description, &c.License,
		&c.PackageSize, &c.InstalledSize, &c.Filename, &c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, err //nolint:wrapcheck // callers wrap with their own context and test sql.ErrNoRows
	}

	return c, nil
}

// rpmComponentColumns selects an RPM package (alias pk, LEFT JOIN blobs for sha256).
const rpmComponentColumns = `pk.id, pk.repository_id, COALESCE(b.sha256,''), pk.name, pk.version,
		       COALESCE(pk.release,''), pk.epoch, COALESCE(pk.arch,''),
		       COALESCE(pk.summary,''), COALESCE(pk.description,''), COALESCE(pk.license,''),
		       COALESCE(pk.url,''), COALESCE(pk.vendor,''), COALESCE(pk.group_name,''),
		       COALESCE(pk.packager,''), COALESCE(pk.source_rpm,''), COALESCE(pk.build_host,''),
		       COALESCE(EXTRACT(EPOCH FROM pk.build_time)::bigint,0),
		       COALESCE(pk.package_size,0), COALESCE(pk.installed_size,0), COALESCE(pk.archive_size,0),
		       COALESCE(pk.filename,''), pk.created_at, pk.updated_at
		FROM rpm_packages pk LEFT JOIN blobs b ON b.id = pk.blob_id`

// debComponentColumns selects a DEB package (alias pk, LEFT JOIN blobs for sha256/size).
const debComponentColumns = `pk.id, pk.repository_id, COALESCE(b.sha256,''), pk.name, pk.version,
		       COALESCE(pk.arch,''), COALESCE(pk.summary,''), COALESCE(pk.description,''),
		       COALESCE(pk.license,''), COALESCE(b.size_bytes,0), COALESCE(pk.installed_size,0),
		       COALESCE(pk.filename,''), pk.created_at, pk.updated_at
		FROM deb_packages pk LEFT JOIN blobs b ON b.id = pk.blob_id`

// getComponentWhere finds the first live package matching where (written
// against the alias pk), trying rpm_packages then deb_packages (row IDs are
// shared across both tables). what names the lookup in the error message.
func (p *ComponentStore) getComponentWhere(ctx context.Context, what, where string, args ...any) (*models.Component, error) {
	const live = " AND pk.deleted_at IS NULL LIMIT 1"

	c, err := scanRpmComponent(p.db.QueryRowContext(ctx, "SELECT "+rpmComponentColumns+" WHERE "+where+live, args...))
	if err == nil {
		return c, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get component by %s: %w", what, err)
	}

	c, err = scanDebComponent(p.db.QueryRowContext(ctx, "SELECT "+debComponentColumns+" WHERE "+where+live, args...))

	return orNotFound(c, err, "get component by "+what)
}

// GetComponentByID looks up a package by its primary key, trying rpm_packages
// then deb_packages.
func (p *ComponentStore) GetComponentByID(ctx context.Context, id string) (*models.Component, error) {
	return p.getComponentWhere(ctx, "id", "pk.id = $1", id)
}

func (p *ComponentStore) GetComponentByFilename(ctx context.Context, repoID string, filename string) (*models.Component, error) {
	return p.getComponentWhere(ctx, "filename", "pk.repository_id = $1 AND pk.filename = $2", repoID, filename)
}

// GetComponentBySHA256 finds a live deb or rpm package in a repository by the
// sha256 of its blob using a single-row lookup per table.
func (p *ComponentStore) GetComponentBySHA256(ctx context.Context, repoID, sha256 string) (*models.Component, error) {
	return p.getComponentWhere(ctx, "sha256", "pk.repository_id = $1 AND b.sha256 = $2", repoID, sha256)
}

// GetComponentsByRepo lists RPM packages for a repository.
func (p *ComponentStore) GetComponentsByRepo(ctx context.Context, repoID string) ([]models.Component, error) {
	rows, err := p.db.QueryContext(ctx, "SELECT "+rpmComponentColumns+" WHERE pk.repository_id = $1 AND pk.deleted_at IS NULL", repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to query components: %w", err)
	}

	return collectRows(rows, "failed to scan component", "failed to iterate components",
		func(row rowScanner) (models.Component, error) {
			c := models.Component{Format: "rpm"}

			return c, row.Scan(rpmComponentDest(&c)...) //nolint:wrapcheck // collectRows wraps
		})
}

func (p *ComponentStore) DeleteComponent(ctx context.Context, componentID string) error {
	// Try deb_packages first, then rpm_packages. Only one will match.
	res, err := p.db.ExecContext(ctx, `DELETE FROM deb_packages WHERE id = $1 AND deleted_at IS NULL`, componentID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows > 0 {
		return nil
	}
	_, err = p.db.ExecContext(ctx, `DELETE FROM rpm_packages WHERE id = $1 AND deleted_at IS NULL`, componentID)
	return err
}
