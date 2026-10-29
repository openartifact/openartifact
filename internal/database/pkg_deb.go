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

// DebStore owns the Debian format tables: distributions, components, architectures and package control data.
type DebStore struct{ *core }

// CreateDebPackage inserts a DEB package with distribution and component references.
func (p *DebStore) CreateDebPackage(ctx context.Context, pkg *models.Component, distID, compID string) error {
	err := p.db.QueryRowContext(
		ctx,
		`INSERT INTO deb_packages (repository_id, distribution_id, component_id, blob_id, name, version, arch, description,
			summary, license, maintainer, homepage, filename, installed_size, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,COALESCE($15::timestamptz, now()))
		 ON CONFLICT DO NOTHING
		 RETURNING id, created_at, updated_at`,
		pkg.RepositoryID,
		distID,
		compID,
		pkg.ArtifactID,
		pkg.Name,
		pkg.Version,
		pkg.Arch,
		pkg.Description,
		pkg.Summary,
		pkg.License,
		"",
		"",
		pkg.Filename,
		pkg.InstalledSize,
		createdAtOrNil(pkg.CreatedAt),
	).Scan(&pkg.ID, &pkg.CreatedAt, &pkg.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDuplicate
	}

	return err
}

// GetDebPackageByID fetches a DEB package by its primary key.
func (p *DebStore) GetDebPackageByID(ctx context.Context, id string) (*models.DebPackage, error) {
	pkg := &models.DebPackage{}
	err := p.db.QueryRowContext(ctx,
		`SELECT id, repository_id, distribution_id, component_id, blob_id,
		        name, version, arch,
		        COALESCE(summary,''), COALESCE(description,''), COALESCE(license,''),
		        COALESCE(maintainer,''), COALESCE(homepage,''),
		        COALESCE(filename,''), COALESCE(installed_size,0),
		        created_at, updated_at
		 FROM deb_packages WHERE id = $1 AND deleted_at IS NULL`, id).
		Scan(&pkg.ID, &pkg.RepositoryID, &pkg.DistributionID, &pkg.ComponentID, &pkg.BlobID,
			&pkg.Name, &pkg.Version, &pkg.Arch,
			&pkg.Summary, &pkg.Description, &pkg.License,
			&pkg.Maintainer, &pkg.Homepage,
			&pkg.Filename, &pkg.InstalledSize,
			&pkg.CreatedAt, &pkg.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}
	if err != nil {
		return nil, fmt.Errorf("get deb package: %w", err)
	}
	return pkg, nil
}

// ListDebPackagesByRepo lists all DEB packages in a repository.
func (p *DebStore) ListDebPackagesByRepo(ctx context.Context, repoID string) ([]*models.DebPackage, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, repository_id, distribution_id, component_id, blob_id,
		        name, version, arch,
		        COALESCE(summary,''), COALESCE(description,''), COALESCE(license,''),
		        COALESCE(maintainer,''), COALESCE(homepage,''),
		        COALESCE(filename,''), COALESCE(installed_size,0),
		        created_at, updated_at
		 FROM deb_packages WHERE repository_id = $1 AND deleted_at IS NULL`, repoID)
	if err != nil {
		return nil, fmt.Errorf("list deb packages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var pkgs []*models.DebPackage
	for rows.Next() {
		pkg := &models.DebPackage{}
		if err := rows.Scan(&pkg.ID, &pkg.RepositoryID, &pkg.DistributionID, &pkg.ComponentID, &pkg.BlobID,
			&pkg.Name, &pkg.Version, &pkg.Arch,
			&pkg.Summary, &pkg.Description, &pkg.License,
			&pkg.Maintainer, &pkg.Homepage,
			&pkg.Filename, &pkg.InstalledSize,
			&pkg.CreatedAt, &pkg.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan deb package: %w", err)
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs, rows.Err()
}

func (p *DebStore) CreateDebianDistribution(ctx context.Context, dist *models.DebianDistribution) error {
	row, err := p.q.CreateDebianDistribution(ctx, dbsqlc.CreateDebianDistributionParams{
		RepositoryID: dist.RepositoryID,
		Name:         dist.Name,
		Codename:     strPtr(dist.Codename),
		Description:  strPtr(dist.Description),
	})

	return finishCreate(insertedRow(row), err, "create debian distribution", &dist.ID, &dist.CreatedAt, &dist.UpdatedAt)
}

func (p *DebStore) GetDebianDistributionsByRepo(ctx context.Context, repoID string) ([]models.DebianDistribution, error) {
	rows, err := p.q.GetDebianDistributionsByRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to query debian distributions: %w", err)
	}

	dists := make([]models.DebianDistribution, len(rows))
	for i, row := range rows {
		dists[i] = models.DebianDistribution{
			ID:           row.ID,
			RepositoryID: row.RepositoryID,
			Name:         row.Name,
			Codename:     row.Codename,
			Description:  row.Description,
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		}
	}

	return dists, nil
}

func (p *DebStore) CreateDebianComponent(ctx context.Context, comp *models.DebianComponent) error {
	row, err := p.q.CreateDebianComponent(ctx, dbsqlc.CreateDebianComponentParams{
		DistributionID: comp.DistributionID,
		Name:           comp.Name,
		Description:    strPtr(comp.Description),
	})

	return finishCreate(insertedRow(row), err, "create debian component", &comp.ID, &comp.CreatedAt, &comp.UpdatedAt)
}

func (p *DebStore) GetDebianComponentsByDist(ctx context.Context, distID string) ([]models.DebianComponent, error) {
	rows, err := p.q.GetDebianComponentsByDist(ctx, distID)
	if err != nil {
		return nil, fmt.Errorf("failed to query debian components: %w", err)
	}

	comps := make([]models.DebianComponent, len(rows))
	for i, row := range rows {
		comps[i] = models.DebianComponent{
			ID:             row.ID,
			DistributionID: row.DistributionID,
			Name:           row.Name,
			Description:    row.Description,
			CreatedAt:      row.CreatedAt,
			UpdatedAt:      row.UpdatedAt,
		}
	}

	return comps, nil
}

func (p *DebStore) CreateDebianArchitecture(ctx context.Context, arch *models.DebianArchitecture) error {
	row, err := p.q.CreateDebianArchitecture(ctx, dbsqlc.CreateDebianArchitectureParams{
		DistributionID: arch.DistributionID,
		Name:           arch.Name,
	})

	return finishCreate(insertedRow(row), err, "create debian architecture", &arch.ID, &arch.CreatedAt, &arch.UpdatedAt)
}

func (p *DebStore) GetDebianArchitecturesByDist(ctx context.Context, distID string) ([]models.DebianArchitecture, error) {
	rows, err := p.q.GetDebianArchitecturesByDist(ctx, distID)
	if err != nil {
		return nil, fmt.Errorf("failed to query debian architectures: %w", err)
	}

	archs := make([]models.DebianArchitecture, len(rows))
	for i, row := range rows {
		archs[i] = models.DebianArchitecture{
			ID:             row.ID,
			DistributionID: row.DistributionID,
			Name:           row.Name,
			CreatedAt:      row.CreatedAt,
			UpdatedAt:      row.UpdatedAt,
		}
	}

	return archs, nil
}

func (p *DebStore) GetDebianPackageByFilename(ctx context.Context, repoID string, filename string) (*models.Component, error) {
	row, err := p.q.GetDebianPackageByFilename(ctx, dbsqlc.GetDebianPackageByFilenameParams{
		RepositoryID: repoID,
		Filename:     strPtr(filename),
	})

	return mapRow(row, err, "get debian package by filename", debianComponentFromRow)
}

// debianComponentRow lists the sqlc row types debianComponentFromRow converts.
type debianComponentRow interface {
	dbsqlc.GetDebianPackageByFilenameRow | dbsqlc.GetDebianPackagesByDistCompRow
}

func debianComponentFromRow[R debianComponentRow](in R) *models.Component {
	row := dbsqlc.GetDebianPackageByFilenameRow(in)

	return &models.Component{
		ID:            row.ID,
		RepositoryID:  row.RepositoryID,
		SHA256:        row.Sha256,
		Format:        "deb",
		Name:          row.Name,
		Version:       row.Version,
		Arch:          row.Arch,
		Summary:       row.Summary,
		Description:   row.Description,
		License:       row.License,
		Filename:      row.Filename,
		PackageSize:   row.PackageSize,
		InstalledSize: row.InstalledSize,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
}

func (p *DebStore) GetDebianPackagesByDistComp(
	ctx context.Context,
	repoID string,
	dist string,
	comp string,
	arch string,
) ([]models.Component, error) {
	rows, err := p.q.GetDebianPackagesByDistComp(ctx, dbsqlc.GetDebianPackagesByDistCompParams{
		RepositoryID: repoID,
		Dist:         dist,
		Comp:         comp,
		Arch:         arch,
	})

	return mapRows(rows, err, "query debian packages", func(row dbsqlc.GetDebianPackagesByDistCompRow) models.Component {
		return *debianComponentFromRow(row)
	})
}
