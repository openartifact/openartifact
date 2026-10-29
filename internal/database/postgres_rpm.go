// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

// RpmPackageExists reports whether the repository already has a live package with the NEVRA.arch.
func (p *RPMStore) RpmPackageExists(ctx context.Context, repoID, name, version, release string, epoch int, arch string) (bool, error) {
	exists, err := p.q.RpmPackageExists(ctx, dbsqlc.RpmPackageExistsParams{
		RepositoryID: repoID, NameKey: name, VersionKey: rpmVersionKey(version, release, epoch, arch),
	})
	if err != nil {
		return false, fmt.Errorf("check rpm package exists: %w", err)
	}

	return exists, nil
}

// ListRpmPackagesForMetadata loads every rpm package of a repo with dependency,
// file and changelog data plus the header range, for metadata generation.
func (p *RPMStore) ListRpmPackagesForMetadata(ctx context.Context, repoID string) ([]pkgrpm.Package, error) {
	// Sorted in SQL over the whole repository, as the legacy query did.
	rows, err := p.db.QueryContext(ctx, `
		SELECT p.name, v.version, COALESCE(f.attrs ->> 'release', ''), COALESCE((f.attrs ->> 'epoch')::bigint, 0), f.arch,
		       COALESCE(v.metadata ->> 'summary', ''), COALESCE(v.metadata ->> 'description', ''), COALESCE(v.metadata ->> 'license', ''),
		       COALESCE(v.metadata ->> 'url', ''), COALESCE(v.metadata ->> 'vendor', ''), COALESCE(v.metadata ->> 'group_name', ''),
		       COALESCE(v.metadata ->> 'packager', ''), COALESCE(v.metadata ->> 'source_rpm', ''), COALESCE(v.metadata ->> 'build_host', ''),
		       COALESCE((f.attrs ->> 'build_time')::bigint, 0),
		       EXTRACT(EPOCH FROM f.created_at)::bigint,
		       COALESCE(f.size_bytes, 0), COALESCE((f.attrs ->> 'installed_size')::bigint, 0), COALESCE((f.attrs ->> 'archive_size')::bigint, 0),
		       b.sha256, r.header_start, r.header_end, r.rpm_extra
		FROM package_files f
		JOIN rpm_files r ON r.id = f.id
		JOIN package_versions v ON v.id = f.version_id
		JOIN packages p ON p.id = v.package_id
		JOIN blobs b ON b.id = f.blob_id
		WHERE f.repository_id = $1 AND f.deleted_at IS NULL
		ORDER BY p.name, v.version, COALESCE(f.attrs ->> 'release', ''), f.arch`, repoID)
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
			&pkg.BuildTime, &pkg.FileTime, &pkg.PackageSize, &pkg.InstalledSize, &pkg.ArchiveSize,
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

// UpdateRpmPackageExtra replaces rpm_extra for the packages of a repo backed by
// the blob with the given sha256. When the extra data carries sizes
// (sizes_parsed, see pkgrpm.Package.MarshalExtra) the installed_size and
// archive_size attrs are corrected from it too: rows stored before the
// header size tags were read properly hold 0.
func (p *RPMStore) UpdateRpmPackageExtra(ctx context.Context, repoID, sha256 string, extra []byte) error {
	if err := p.q.UpdateRpmPackageExtra(ctx, dbsqlc.UpdateRpmPackageExtraParams{
		RepositoryID: repoID, Sha256: sha256, Extra: extra,
	}); err != nil {
		return fmt.Errorf("update rpm package extra: %w", err)
	}

	return nil
}
