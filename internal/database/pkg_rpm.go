// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// RPMStore owns the RPM format tables.
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

// CreateRpmPackage inserts an RPM package.
func (p *RPMStore) CreateRpmPackage(ctx context.Context, pkg *models.Component) error {
	err := p.db.QueryRowContext(
		ctx,
		`INSERT INTO rpm_packages (repository_id, blob_id, name, version, release, epoch, arch, description, summary, license, url,
			vendor, group_name, packager, source_rpm, build_host, build_time, package_size, archive_size, installed_size, filename,
			header_start, header_end, rpm_extra, created_at)
		 VALUES
			($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,
			to_timestamp(NULLIF($17::bigint,0)),$18,$19,$20,$21,$22,$23,COALESCE($24::jsonb,'{}'::jsonb),
			COALESCE($25::timestamptz, now()))
		 ON CONFLICT DO NOTHING
		 RETURNING id, created_at, updated_at`,
		pkg.RepositoryID,
		pkg.ArtifactID,
		pkg.Name,
		pkg.Version,
		pkg.Release,
		int64(pkg.Epoch),
		pkg.Arch,
		pkg.Description,
		pkg.Summary,
		pkg.License,
		pkg.URL,
		pkg.Vendor,
		pkg.Group,
		pkg.Packager,
		pkg.SourceRPM,
		pkg.BuildHost,
		pkg.BuildTime,
		pkg.PackageSize,
		pkg.ArchiveSize,
		pkg.InstalledSize,
		pkg.Filename,
		pkg.HeaderStart,
		pkg.HeaderEnd,
		nullableJSON(pkg.RPMExtra),
		createdAtOrNil(pkg.CreatedAt),
	).Scan(&pkg.ID, &pkg.CreatedAt, &pkg.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDuplicate
	}

	return err
}

// GetRpmPackageByID fetches an RPM package by its primary key.
func (p *RPMStore) GetRpmPackageByID(ctx context.Context, id string) (*models.RpmPackage, error) {
	pkg := &models.RpmPackage{}
	err := p.db.QueryRowContext(ctx,
		`SELECT id, repository_id, blob_id,
		        name, version, COALESCE(release,''), epoch, COALESCE(arch,''),
		        COALESCE(summary,''), COALESCE(description,''), COALESCE(license,''),
		        COALESCE(url,''), COALESCE(vendor,''), COALESCE(group_name,''),
		        COALESCE(packager,''), COALESCE(source_rpm,''), COALESCE(build_host,''),
		        build_time,
		        COALESCE(filename,''), COALESCE(package_size,0), COALESCE(installed_size,0), COALESCE(archive_size,0),
		        created_at, updated_at
		 FROM rpm_packages WHERE id = $1 AND deleted_at IS NULL`, id).
		Scan(&pkg.ID, &pkg.RepositoryID, &pkg.BlobID,
			&pkg.Name, &pkg.Version, &pkg.Release, &pkg.Epoch, &pkg.Arch,
			&pkg.Summary, &pkg.Description, &pkg.License,
			&pkg.URL, &pkg.Vendor, &pkg.GroupName,
			&pkg.Packager, &pkg.SourceRPM, &pkg.BuildHost,
			&pkg.BuildTime,
			&pkg.Filename, &pkg.PackageSize, &pkg.InstalledSize, &pkg.ArchiveSize,
			&pkg.CreatedAt, &pkg.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}
	if err != nil {
		return nil, fmt.Errorf("get rpm package: %w", err)
	}
	return pkg, nil
}
