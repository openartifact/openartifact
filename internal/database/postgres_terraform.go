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

func terraformModuleFromRow(row dbsqlc.GetTerraformModuleRow) *models.TerraformModule {
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

func terraformProviderFromRow(row dbsqlc.GetTerraformProviderRow) *models.TerraformProvider {
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
func (p *PostgresDB) CreateTerraformModule(ctx context.Context, mod *models.TerraformModule) error {
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
	if err != nil {
		return fmt.Errorf("failed to create terraform module: %w", err)
	}

	mod.ID = row.ID
	mod.CreatedAt = row.CreatedAt
	mod.UpdatedAt = row.UpdatedAt

	return nil
}

// GetTerraformModule returns the live module version, or (nil, nil).
func (p *PostgresDB) GetTerraformModule(ctx context.Context, repoID, name, system, version string) (*models.TerraformModule, error) {
	row, err := p.q.GetTerraformModule(ctx, dbsqlc.GetTerraformModuleParams{
		RepositoryID: repoID, Name: name, System: system, Version: version,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get terraform module: %w", err)
	}

	return terraformModuleFromRow(row), nil
}

// ListTerraformModuleVersions returns the live versions of one module, oldest
// first.
func (p *PostgresDB) ListTerraformModuleVersions(ctx context.Context, repoID, name, system string) ([]*models.TerraformModule, error) {
	rows, err := p.q.ListTerraformModuleVersions(ctx, dbsqlc.ListTerraformModuleVersionsParams{
		RepositoryID: repoID, Name: name, System: system,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list terraform module versions: %w", err)
	}

	mods := make([]*models.TerraformModule, 0, len(rows))
	for _, row := range rows {
		mods = append(mods, terraformModuleFromRow(dbsqlc.GetTerraformModuleRow(row)))
	}

	return mods, nil
}

// ListTerraformModules returns every live module version of the repository.
func (p *PostgresDB) ListTerraformModules(ctx context.Context, repoID string) ([]*models.TerraformModule, error) {
	rows, err := p.q.ListTerraformModules(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list terraform modules: %w", err)
	}

	mods := make([]*models.TerraformModule, 0, len(rows))
	for _, row := range rows {
		mods = append(mods, terraformModuleFromRow(dbsqlc.GetTerraformModuleRow(row)))
	}

	return mods, nil
}

// DeleteTerraformModule removes one module version row.
func (p *PostgresDB) DeleteTerraformModule(ctx context.Context, repoID, id string) error {
	if err := p.q.DeleteTerraformModule(ctx, dbsqlc.DeleteTerraformModuleParams{ID: id, RepositoryID: repoID}); err != nil {
		return fmt.Errorf("failed to delete terraform module: %w", err)
	}

	return nil
}

// IncrementTerraformModuleDownloadCount counts one download of a module version.
func (p *PostgresDB) IncrementTerraformModuleDownloadCount(ctx context.Context, repoID, id string) error {
	err := p.q.IncrementTerraformModuleDownloadCount(ctx, dbsqlc.IncrementTerraformModuleDownloadCountParams{ID: id, RepositoryID: repoID})
	if err != nil {
		return fmt.Errorf("failed to count terraform module download: %w", err)
	}

	return nil
}

// TerraformModuleStamp fingerprints the live versions of one module.
func (p *PostgresDB) TerraformModuleStamp(ctx context.Context, repoID, name, system string) (string, error) {
	stamp, err := p.q.TerraformModuleStamp(ctx, dbsqlc.TerraformModuleStampParams{RepositoryID: repoID, Name: name, System: system})
	if err != nil {
		return "", fmt.Errorf("failed to stamp terraform module: %w", err)
	}

	return stamp, nil
}

// CreateTerraformProvider stores one provider platform zip. A live row with the
// same coordinates is a unique violation.
func (p *PostgresDB) CreateTerraformProvider(ctx context.Context, prov *models.TerraformProvider) error {
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
	if err != nil {
		return fmt.Errorf("failed to create terraform provider: %w", err)
	}

	prov.ID = row.ID
	prov.CreatedAt = row.CreatedAt
	prov.UpdatedAt = row.UpdatedAt

	return nil
}

// GetTerraformProvider returns the live platform zip, or (nil, nil).
func (p *PostgresDB) GetTerraformProvider(
	ctx context.Context,
	repoID, typ, version, osName, arch string,
) (*models.TerraformProvider, error) {
	row, err := p.q.GetTerraformProvider(ctx, dbsqlc.GetTerraformProviderParams{
		RepositoryID: repoID, Type: typ, Version: version, Os: osName, Arch: arch,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get terraform provider: %w", err)
	}

	return terraformProviderFromRow(row), nil
}

// ListTerraformProviderPlatforms returns every live platform zip of one
// provider type, ordered by version, os and arch.
func (p *PostgresDB) ListTerraformProviderPlatforms(ctx context.Context, repoID, typ string) ([]*models.TerraformProvider, error) {
	rows, err := p.q.ListTerraformProviderPlatforms(ctx, dbsqlc.ListTerraformProviderPlatformsParams{RepositoryID: repoID, Type: typ})
	if err != nil {
		return nil, fmt.Errorf("failed to list terraform provider platforms: %w", err)
	}

	provs := make([]*models.TerraformProvider, 0, len(rows))
	for _, row := range rows {
		provs = append(provs, terraformProviderFromRow(dbsqlc.GetTerraformProviderRow(row)))
	}

	return provs, nil
}

// ListTerraformProviderVersionPlatforms returns every live platform zip of one
// provider version, ordered by os and arch.
func (p *PostgresDB) ListTerraformProviderVersionPlatforms(
	ctx context.Context,
	repoID, typ, version string,
) ([]*models.TerraformProvider, error) {
	rows, err := p.q.ListTerraformProviderVersionPlatforms(ctx, dbsqlc.ListTerraformProviderVersionPlatformsParams{
		RepositoryID: repoID, Type: typ, Version: version,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list terraform provider version platforms: %w", err)
	}

	provs := make([]*models.TerraformProvider, 0, len(rows))
	for _, row := range rows {
		provs = append(provs, terraformProviderFromRow(dbsqlc.GetTerraformProviderRow(row)))
	}

	return provs, nil
}

// ListTerraformProviders returns every live provider platform zip of the
// repository.
func (p *PostgresDB) ListTerraformProviders(ctx context.Context, repoID string) ([]*models.TerraformProvider, error) {
	rows, err := p.q.ListTerraformProviders(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list terraform providers: %w", err)
	}

	provs := make([]*models.TerraformProvider, 0, len(rows))
	for _, row := range rows {
		provs = append(provs, terraformProviderFromRow(dbsqlc.GetTerraformProviderRow(row)))
	}

	return provs, nil
}

// DeleteTerraformProvider removes one provider platform row.
func (p *PostgresDB) DeleteTerraformProvider(ctx context.Context, repoID, id string) error {
	if err := p.q.DeleteTerraformProvider(ctx, dbsqlc.DeleteTerraformProviderParams{ID: id, RepositoryID: repoID}); err != nil {
		return fmt.Errorf("failed to delete terraform provider: %w", err)
	}

	return nil
}

// IncrementTerraformProviderDownloadCount counts one download of a provider zip.
func (p *PostgresDB) IncrementTerraformProviderDownloadCount(ctx context.Context, repoID, id string) error {
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
func (p *PostgresDB) TerraformProviderStamp(ctx context.Context, repoID, typ string) (string, error) {
	stamp, err := p.q.TerraformProviderStamp(ctx, dbsqlc.TerraformProviderStampParams{RepositoryID: repoID, Type: typ})
	if err != nil {
		return "", fmt.Errorf("failed to stamp terraform provider: %w", err)
	}

	return stamp, nil
}
