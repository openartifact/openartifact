// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
	pkgdeb "github.com/openartifact/openartifact/pkg/deb"
)

// DebStore owns the Debian format tables: distributions, components, architectures and package control data.
//
// A deb package lives on the generic package tables (packages P, package_versions V,
// package_files F) plus the side table deb_files. Grain 1:1: one .deb (name, version, arch,
// component) is one P (shared by name), one V and one F. The mapping, field by field:
//
//	P  name, name_key = Package (not normalised); attrs = {}
//	V  version        = Version as published (may carry an epoch)
//	   version_key    = version|arch|component_id (every coordinate of the legacy live key
//	                    that is not the package identity)
//	   metadata       = {summary, description, license, maintainer, homepage} (empty = absent)
//	F  path           = pool/<component>/<prefix>/<name>/<file>, file = filename or
//	                    name_version_arch.deb when the filename is empty (pkgdeb.PoolPath);
//	                    live paths are unique, and every writer refuses a second live row with
//	                    the same filename, so a violation is a conflict (ErrDuplicate)
//	   filename, arch = Filename, Architecture; blob_id = the .deb blob; size_bytes = NULL
//	   part           = distribution_id (the cleanup partition is name, arch, distribution)
//	   attrs          = {installed_size} (only when not zero); checksums = {}
//	deb_files (id = F.id): distribution_id, component_id, control_fields (the Packages stanza
//	   map, written in the transaction of the row by CreateDebPackage, read whole by Packages
//	   generation)
//
// The distribution graph (debian_distributions/components/architectures) is untouched.
type DebStore struct{ *core }

// debVersionKey is the identity of a deb inside its package.
func debVersionKey(version, arch, componentID string) string {
	return version + "|" + arch + "|" + componentID
}

// CreateDebPackage inserts a DEB package with distribution and component references
// and its control data. The row, the control stanza and the maintainer/homepage commit
// together: a metadata regeneration that runs while an upload is in flight sees the
// package complete or not at all.
// A live package with the same name, version, architecture and component, or the same
// pool path, is ErrDuplicate.
func (p *DebStore) CreateDebPackage(ctx context.Context, pkg *models.Component, distID, compID string, ctl models.DebControl) error {
	compName, err := p.q.GetDebianComponentName(ctx, compID)
	if err != nil {
		return fmt.Errorf("get debian component: %w", err)
	}

	control, err := debControlParams(ctl)
	if err != nil {
		return err
	}

	file := pkg.Filename
	if file == "" {
		file = pkg.Name + "_" + pkg.Version + "_" + pkg.Arch + ".deb"
	}

	attrs := map[string]any{}
	putNonZero(attrs, "installed_size", pkg.InstalledSize)

	written, err := p.writeOSPackage(ctx, &PackageWrite{
		RepoID:     pkg.RepositoryID,
		Format:     "deb",
		Name:       pkg.Name,
		NameKey:    pkg.Name,
		Version:    pkg.Version,
		VersionKey: debVersionKey(pkg.Version, pkg.Arch, compID),
		VersionMeta: nonEmptyStrings(map[string]string{
			"summary":     pkg.Summary,
			"description": pkg.Description,
			"license":     pkg.License,
		}),
		Exclusive: true,
		CreatedAt: pkg.CreatedAt,
		Files: []FileWrite{{
			Path:     pkgdeb.PoolPath(compName, pkg.Name, file),
			Filename: pkg.Filename,
			Arch:     pkg.Arch,
			Part:     distID,
			Attrs:    attrs,
		}},
	}, []*string{strOrNil(pkg.ArtifactID)}, func(q *dbsqlc.Queries, files []FileWritten) error {
		if err := q.InsertDebFile(ctx, dbsqlc.InsertDebFileParams{
			ID: files[0].ID, DistributionID: distID, ComponentID: compID,
		}); err != nil {
			return fmt.Errorf("insert deb file: %w", err)
		}

		control.ID = files[0].ID

		if err := q.SetDebControlFields(ctx, control); err != nil {
			return fmt.Errorf("set deb control fields: %w", err)
		}

		return nil
	})
	if err != nil {
		return err
	}

	pkg.ID, pkg.CreatedAt, pkg.UpdatedAt = written.Files[0].ID, written.Files[0].CreatedAt, written.Files[0].UpdatedAt

	return nil
}

// debPackageRow lists the sqlc row types debPackageFromRow converts; they share the columns of GetDebPackageByIDRow.
type debPackageRow interface {
	dbsqlc.GetDebPackageByIDRow | dbsqlc.ListDebPackagesByRepoRow
}

func debPackageFromRow[R debPackageRow](in R) *models.DebPackage {
	row := dbsqlc.GetDebPackageByIDRow(in)

	pkg := &models.DebPackage{
		ID:             row.ID,
		RepositoryID:   row.RepositoryID,
		DistributionID: row.DistributionID,
		ComponentID:    row.ComponentID,
		Name:           row.Name,
		Version:        row.Version,
		Arch:           row.Arch,
		Summary:        row.Summary,
		Description:    row.Description,
		License:        row.License,
		Maintainer:     row.Maintainer,
		Homepage:       row.Homepage,
		Filename:       row.Filename,
		InstalledSize:  row.InstalledSize,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
	if row.BlobID != nil {
		pkg.BlobID = *row.BlobID
	}

	return pkg
}

// GetDebPackageByID fetches a live DEB package by its primary key, or (nil, nil).
func (p *DebStore) GetDebPackageByID(ctx context.Context, id string) (*models.DebPackage, error) {
	row, err := p.q.GetDebPackageByID(ctx, id)

	return mapRow(row, err, "get deb package", debPackageFromRow)
}

// ListDebPackagesByRepo lists all DEB packages in a repository.
func (p *DebStore) ListDebPackagesByRepo(ctx context.Context, repoID string) ([]*models.DebPackage, error) {
	rows, err := p.q.ListDebPackagesByRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("list deb packages: %w", err)
	}

	pkgs := make([]*models.DebPackage, 0, len(rows))
	for _, row := range rows {
		pkgs = append(pkgs, debPackageFromRow(row))
	}

	return pkgs, nil
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
		Filename:     filename,
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
