// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

// nullableJSON returns nil (SQL NULL) for empty JSON so COALESCE applies the default.
func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}

	return string(b)
}

// RpmPackageExists reports whether the repository already has a package with the NEVRA.
func (p *PostgresDB) RpmPackageExists(ctx context.Context, repoID, name, version, release string, epoch int, arch string) (bool, error) {
	var exists bool

	err := p.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM rpm_packages
		  WHERE repository_id = $1 AND name = $2 AND version = $3 AND release = $4 AND epoch = $5 AND arch = $6 AND deleted_at IS NULL)`,
		repoID, name, version, release, epoch, arch).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check rpm package exists: %w", err)
	}

	return exists, nil
}

// ListRpmPackagesForMetadata loads every rpm package of a repo with dependency,
// file and changelog data plus the header range, for metadata generation.
func (p *PostgresDB) ListRpmPackagesForMetadata(ctx context.Context, repoID string) ([]pkgrpm.Package, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT rp.name, rp.version, COALESCE(rp.release, ''), rp.epoch, COALESCE(rp.arch, ''),
		       COALESCE(rp.summary, ''), COALESCE(rp.description, ''), COALESCE(rp.license, ''),
		       COALESCE(rp.url, ''), COALESCE(rp.vendor, ''), COALESCE(rp.group_name, ''),
		       COALESCE(rp.packager, ''), COALESCE(rp.source_rpm, ''), COALESCE(rp.build_host, ''),
		       COALESCE(EXTRACT(EPOCH FROM rp.build_time)::bigint, 0),
		       COALESCE(rp.package_size, 0), COALESCE(rp.installed_size, 0), COALESCE(rp.archive_size, 0),
		       b.sha256, rp.header_start, rp.header_end, rp.rpm_extra
		FROM rpm_packages rp
		JOIN blobs b ON b.id = rp.blob_id
		WHERE rp.repository_id = $1 AND rp.deleted_at IS NULL
		ORDER BY rp.name, rp.version, rp.release, rp.arch`, repoID)
	if err != nil {
		return nil, fmt.Errorf("query rpm packages for metadata: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []pkgrpm.Package

	for rows.Next() {
		var (
			pkg   pkgrpm.Package
			extra []byte
		)

		if err := rows.Scan(
			&pkg.Name, &pkg.Version, &pkg.Release, &pkg.Epoch, &pkg.Arch,
			&pkg.Summary, &pkg.Description, &pkg.License, &pkg.URL,
			&pkg.Vendor, &pkg.Group, &pkg.Packager, &pkg.SourceRPM, &pkg.BuildHost,
			&pkg.BuildTime, &pkg.PackageSize, &pkg.InstalledSize, &pkg.ArchiveSize,
			&pkg.SHA256, &pkg.HeaderStart, &pkg.HeaderEnd, &extra,
		); err != nil {
			return nil, fmt.Errorf("scan rpm package: %w", err)
		}

		if err := pkg.UnmarshalExtra(extra); err != nil {
			return nil, err
		}

		out = append(out, pkg)
	}

	return out, rows.Err()
}

// UpdateRpmPackageExtra replaces rpm_extra for the packages of a repo backed by the blob with the given sha256.
func (p *PostgresDB) UpdateRpmPackageExtra(ctx context.Context, repoID, sha256 string, extra []byte) error {
	if _, err := p.db.ExecContext(ctx, `
		UPDATE rpm_packages SET rpm_extra = $3::jsonb
		WHERE repository_id = $1 AND deleted_at IS NULL AND blob_id IN (SELECT id FROM blobs WHERE sha256 = $2)`,
		repoID, sha256, extra); err != nil {
		return fmt.Errorf("update rpm package extra: %w", err)
	}

	return nil
}
