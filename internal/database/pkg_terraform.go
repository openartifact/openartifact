// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// TerraformStore owns Terraform/OpenTofu modules and providers.
type TerraformStore struct{ *core }

// terraformModuleMediaType maps the stored archive kind to the media type of
// its blob.
func terraformModuleMediaType(ext string) string {
	switch ext {
	case "zip":
		return "application/zip"
	case "tar.gz":
		return "application/gzip"
	default:
		return ""
	}
}

// terraformModuleRow lists the sqlc row types terraformModuleFromRow converts; they share the columns of GetTerraformModuleRow.
type terraformModuleRow interface {
	dbsqlc.GetTerraformModuleRow | dbsqlc.ListTerraformModuleVersionsRow | dbsqlc.ListTerraformModulesRow
}

func terraformModuleFromRow[R terraformModuleRow](in R) *models.TerraformModule {
	row := dbsqlc.GetTerraformModuleRow(in)

	return &models.TerraformModule{
		ID:            row.ID,
		RepositoryID:  row.RepositoryID,
		Name:          row.Name,
		System:        row.System,
		Version:       row.Version,
		ArchiveExt:    row.ArchiveExt,
		BlobKey:       row.BlobKey,
		Checksum:      row.Checksum,
		SizeBytes:     row.SizeBytes,
		DownloadCount: row.DownloadCount,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
}

// terraformProviderRow lists the sqlc row types terraformProviderFromRow converts; they share the columns of GetTerraformProviderRow.
type terraformProviderRow interface {
	dbsqlc.GetTerraformProviderRow | dbsqlc.ListTerraformProviderPlatformsRow |
		dbsqlc.ListTerraformProviderVersionPlatformsRow | dbsqlc.ListTerraformProvidersRow
}

func terraformProviderFromRow[R terraformProviderRow](in R) *models.TerraformProvider {
	row := dbsqlc.GetTerraformProviderRow(in)

	return &models.TerraformProvider{
		ID:            row.ID,
		RepositoryID:  row.RepositoryID,
		Type:          row.Type,
		Version:       row.Version,
		OS:            row.Os,
		Arch:          row.Arch,
		Protocols:     row.Protocols,
		BlobKey:       row.BlobKey,
		Checksum:      row.Checksum,
		SizeBytes:     row.SizeBytes,
		DownloadCount: row.DownloadCount,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
}

// CreateTerraformModule stores one module version. A live row with the same
// coordinates is a unique violation (versions are immutable).
func (p *TerraformStore) CreateTerraformModule(ctx context.Context, mod *models.TerraformModule) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, mod.Checksum, mod.BlobKey, terraformModuleMediaType(mod.ArchiveExt), mod.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for terraform module: %w", err)
	}

	row, err := p.q.CreateTerraformModule(ctx, dbsqlc.CreateTerraformModuleParams{
		RepositoryID: mod.RepositoryID,
		Name:         mod.Name,
		System:       mod.System,
		Version:      mod.Version,
		ArchiveExt:   mod.ArchiveExt,
		BlobID:       blobID,
		CreatedAt:    createdAtOrNil(mod.CreatedAt),
	})

	return finishCreate(insertedRow(row), err, "create terraform module", &mod.ID, &mod.CreatedAt, &mod.UpdatedAt)
}

// GetTerraformModule returns the live module version, or (nil, nil).
func (p *TerraformStore) GetTerraformModule(ctx context.Context, repoID, name, system, version string) (*models.TerraformModule, error) {
	row, err := p.q.GetTerraformModule(ctx, dbsqlc.GetTerraformModuleParams{
		RepositoryID: repoID, Name: name, System: system, Version: version,
	})

	return mapRow(row, err, "get terraform module", terraformModuleFromRow)
}

// ListTerraformModuleVersions returns the live versions of one module, oldest
// first.
func (p *TerraformStore) ListTerraformModuleVersions(ctx context.Context, repoID, name, system string) ([]*models.TerraformModule, error) {
	rows, err := p.q.ListTerraformModuleVersions(ctx, dbsqlc.ListTerraformModuleVersionsParams{
		RepositoryID: repoID, Name: name, System: system,
	})

	return mapRows(rows, err, "list terraform module versions", terraformModuleFromRow)
}

// ListTerraformModules returns every live module version of the repository.
func (p *TerraformStore) ListTerraformModules(ctx context.Context, repoID string) ([]*models.TerraformModule, error) {
	rows, err := p.q.ListTerraformModules(ctx, repoID)

	return mapRows(rows, err, "list terraform modules", terraformModuleFromRow)
}

// DeleteTerraformModule removes one module version row.
func (p *TerraformStore) DeleteTerraformModule(ctx context.Context, repoID, id string) error {
	if err := p.q.DeleteTerraformModule(ctx, dbsqlc.DeleteTerraformModuleParams{ID: id, RepositoryID: repoID}); err != nil {
		return fmt.Errorf("failed to delete terraform module: %w", err)
	}

	return nil
}

// IncrementTerraformModuleDownloadCount counts one download of a module version.
func (p *TerraformStore) IncrementTerraformModuleDownloadCount(ctx context.Context, repoID, id string) error {
	err := p.q.IncrementTerraformModuleDownloadCount(ctx, dbsqlc.IncrementTerraformModuleDownloadCountParams{ID: id, RepositoryID: repoID})
	if err != nil {
		return fmt.Errorf("failed to count terraform module download: %w", err)
	}

	return nil
}

// TerraformModuleStamp fingerprints the live versions of one module.
func (p *TerraformStore) TerraformModuleStamp(ctx context.Context, repoID, name, system string) (string, error) {
	stamp, err := p.q.TerraformModuleStamp(ctx, dbsqlc.TerraformModuleStampParams{RepositoryID: repoID, Name: name, System: system})
	if err != nil {
		return "", fmt.Errorf("failed to stamp terraform module: %w", err)
	}

	return stamp, nil
}

// CreateTerraformProvider stores one provider platform zip. A live row with the
// same coordinates is a unique violation.
func (p *TerraformStore) CreateTerraformProvider(ctx context.Context, prov *models.TerraformProvider) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, prov.Checksum, prov.BlobKey, "application/zip", prov.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for terraform provider: %w", err)
	}

	protocols := prov.Protocols
	if protocols == nil {
		protocols = []string{}
	}

	row, err := p.q.CreateTerraformProvider(ctx, dbsqlc.CreateTerraformProviderParams{
		RepositoryID: prov.RepositoryID,
		Type:         prov.Type,
		Version:      prov.Version,
		Os:           prov.OS,
		Arch:         prov.Arch,
		Protocols:    protocols,
		BlobID:       blobID,
		CreatedAt:    createdAtOrNil(prov.CreatedAt),
	})

	return finishCreate(insertedRow(row), err, "create terraform provider", &prov.ID, &prov.CreatedAt, &prov.UpdatedAt)
}

// GetTerraformProvider returns the live platform zip, or (nil, nil).
func (p *TerraformStore) GetTerraformProvider(
	ctx context.Context,
	repoID, typ, version, osName, arch string,
) (*models.TerraformProvider, error) {
	row, err := p.q.GetTerraformProvider(ctx, dbsqlc.GetTerraformProviderParams{
		RepositoryID: repoID, Type: typ, Version: version, Os: osName, Arch: arch,
	})

	return mapRow(row, err, "get terraform provider", terraformProviderFromRow)
}

// ListTerraformProviderPlatforms returns every live platform zip of one
// provider type, ordered by version, os and arch.
func (p *TerraformStore) ListTerraformProviderPlatforms(ctx context.Context, repoID, typ string) ([]*models.TerraformProvider, error) {
	rows, err := p.q.ListTerraformProviderPlatforms(ctx, dbsqlc.ListTerraformProviderPlatformsParams{RepositoryID: repoID, Type: typ})

	return mapRows(rows, err, "list terraform provider platforms", terraformProviderFromRow)
}

// ListTerraformProviderVersionPlatforms returns every live platform zip of one
// provider version, ordered by os and arch.
func (p *TerraformStore) ListTerraformProviderVersionPlatforms(
	ctx context.Context,
	repoID, typ, version string,
) ([]*models.TerraformProvider, error) {
	rows, err := p.q.ListTerraformProviderVersionPlatforms(ctx, dbsqlc.ListTerraformProviderVersionPlatformsParams{
		RepositoryID: repoID, Type: typ, Version: version,
	})

	return mapRows(rows, err, "list terraform provider version platforms",
		terraformProviderFromRow)
}

// ListTerraformProviders returns every live provider platform zip of the
// repository.
func (p *TerraformStore) ListTerraformProviders(ctx context.Context, repoID string) ([]*models.TerraformProvider, error) {
	rows, err := p.q.ListTerraformProviders(ctx, repoID)

	return mapRows(rows, err, "list terraform providers", terraformProviderFromRow)
}

// DeleteTerraformProvider removes one provider platform row.
func (p *TerraformStore) DeleteTerraformProvider(ctx context.Context, repoID, id string) error {
	if err := p.q.DeleteTerraformProvider(ctx, dbsqlc.DeleteTerraformProviderParams{ID: id, RepositoryID: repoID}); err != nil {
		return fmt.Errorf("failed to delete terraform provider: %w", err)
	}

	return nil
}

// IncrementTerraformProviderDownloadCount counts one download of a provider zip.
func (p *TerraformStore) IncrementTerraformProviderDownloadCount(ctx context.Context, repoID, id string) error {
	err := p.q.IncrementTerraformProviderDownloadCount(
		ctx,
		dbsqlc.IncrementTerraformProviderDownloadCountParams{ID: id, RepositoryID: repoID},
	)
	if err != nil {
		return fmt.Errorf("failed to count terraform provider download: %w", err)
	}

	return nil
}

// TerraformProviderStamp fingerprints the live platforms of one provider type.
func (p *TerraformStore) TerraformProviderStamp(ctx context.Context, repoID, typ string) (string, error) {
	stamp, err := p.q.TerraformProviderStamp(ctx, dbsqlc.TerraformProviderStampParams{RepositoryID: repoID, Type: typ})
	if err != nil {
		return "", fmt.Errorf("failed to stamp terraform provider: %w", err)
	}

	return stamp, nil
}
