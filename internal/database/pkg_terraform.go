// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// TerraformStore owns Terraform/OpenTofu modules and providers.
//
// Mapping onto the generic package tables. Modules and providers of one
// repository share the tables and are told apart by the package name prefix
// ("modules/", "providers/"), which is also what the listing shows.
//
// Module (one archive is one version and one file; (name, system) is the package):
//
//	packages          name = name_key = "modules/<name>/<system>",
//	                  attrs {kind: "module", name, system}
//	package_versions  version = version_key = version, metadata {}
//	package_files     path modules/<name>/<system>/<version>/<filename>,
//	                  filename <name>-<system>-<version>.<ext>,
//	                  attrs {archive_ext}, the archive blob, role primary,
//	                  download_count (the module's counter), no unit_key
//	                  (terraform is not federated)
//
// Provider (one version per provider version, one file per platform zip):
//
//	packages          name = name_key = "providers/<type>",
//	                  attrs {kind: "provider", type}
//	package_versions  version = version_key = version,
//	                  metadata {protocols: [..]} (version level: the first
//	                  platform of a version sets it)
//	package_files     path providers/<type>/<version>/<os>/<arch>/
//	                  terraform-provider-<type>_<version>_<os>_<arch>.zip,
//	                  filename the last segment, arch "<os>_<arch>" (the listing
//	                  and retention partition column), attrs {os, arch},
//	                  the zip blob, role primary, download_count
//
// The id a client sees is package_files.id. A version is live while one of its
// platforms is live. A duplicate publish is a unique violation on the live
// version (modules) or on the live path (a platform): ErrDuplicate.
type TerraformStore struct{ *core }

const (
	terraformModulePrefix   = "modules/"
	terraformProviderPrefix = "providers/"
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

func terraformModuleKey(name, system string) string {
	return terraformModulePrefix + name + "/" + system
}

func terraformProviderKey(typ string) string {
	return terraformProviderPrefix + typ
}

// terraformProviderFilename is the file name of one platform zip.
func terraformProviderFilename(typ, version, osName, arch string) string {
	return "terraform-provider-" + typ + "_" + version + "_" + osName + "_" + arch + ".zip"
}

// terraformProviderPath is the path of one platform zip, the key GetTerraformProvider reads.
func terraformProviderPath(typ, version, osName, arch string) string {
	return terraformProviderPrefix + typ + "/" + version + "/" + osName + "/" + arch + "/" +
		terraformProviderFilename(typ, version, osName, arch)
}

// terraformProviderMetadata is package_versions.metadata of a provider version.
type terraformProviderMetadata struct {
	Protocols []string `json:"protocols"`
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

	var meta terraformProviderMetadata

	_ = json.Unmarshal(row.Metadata, &meta)

	return &models.TerraformProvider{
		ID:            row.ID,
		RepositoryID:  row.RepositoryID,
		Type:          row.Type,
		Version:       row.Version,
		OS:            row.Os,
		Arch:          row.Arch,
		Protocols:     meta.Protocols,
		BlobKey:       row.BlobKey,
		Checksum:      row.Checksum,
		SizeBytes:     row.SizeBytes,
		DownloadCount: row.DownloadCount,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
}

// CreateTerraformModule stores one module version. A live version with the
// same coordinates is ErrDuplicate (versions are immutable).
func (p *TerraformStore) CreateTerraformModule(ctx context.Context, mod *models.TerraformModule) error {
	key := terraformModuleKey(mod.Name, mod.System)
	filename := mod.Name + "-" + mod.System + "-" + mod.Version + "." + mod.ArchiveExt

	out, err := p.writePackage(ctx, &PackageWrite{
		RepoID:     mod.RepositoryID,
		Format:     "terraform",
		Name:       key,
		NameKey:    key,
		PkgAttrs:   map[string]any{"kind": "module", "name": mod.Name, "system": mod.System},
		Version:    mod.Version,
		VersionKey: mod.Version,
		Exclusive:  true,
		CreatedAt:  mod.CreatedAt,
		Files: []FileWrite{{
			Path:     key + "/" + mod.Version + "/" + filename,
			Filename: filename,
			Attrs:    map[string]any{"archive_ext": mod.ArchiveExt},
			Blob: BlobWrite{
				SHA256: mod.Checksum, Key: mod.BlobKey, MediaType: terraformModuleMediaType(mod.ArchiveExt), Size: mod.SizeBytes,
			},
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to create terraform module: %w", err)
	}

	mod.ID, mod.CreatedAt, mod.UpdatedAt = out.Files[0].ID, out.Files[0].CreatedAt, out.Files[0].UpdatedAt

	return nil
}

// GetTerraformModule returns the live module version, or (nil, nil).
func (p *TerraformStore) GetTerraformModule(ctx context.Context, repoID, name, system, version string) (*models.TerraformModule, error) {
	row, err := p.q.GetTerraformModule(ctx, dbsqlc.GetTerraformModuleParams{
		RepositoryID: repoID, NameKey: terraformModuleKey(name, system), Version: version,
	})

	return mapRow(row, err, "get terraform module", terraformModuleFromRow)
}

// ListTerraformModuleVersions returns the live versions of one module, oldest
// first.
func (p *TerraformStore) ListTerraformModuleVersions(ctx context.Context, repoID, name, system string) ([]*models.TerraformModule, error) {
	rows, err := p.q.ListTerraformModuleVersions(ctx, dbsqlc.ListTerraformModuleVersionsParams{
		RepositoryID: repoID, NameKey: terraformModuleKey(name, system),
	})

	return mapRows(rows, err, "list terraform module versions", terraformModuleFromRow)
}

// ListTerraformModules returns every live module version of the repository.
func (p *TerraformStore) ListTerraformModules(ctx context.Context, repoID string) ([]*models.TerraformModule, error) {
	rows, err := p.q.ListTerraformModules(ctx, repoID)

	return mapRows(rows, err, "list terraform modules", terraformModuleFromRow)
}

// DeleteTerraformModule hard-deletes one live module version file.
func (p *TerraformStore) DeleteTerraformModule(ctx context.Context, repoID, id string) error {
	if err := p.deleteLiveFiles(ctx, repoID, []string{id}); err != nil {
		return fmt.Errorf("failed to delete terraform module: %w", err)
	}

	return nil
}

// IncrementTerraformModuleDownloadCount counts one download of a module version.
func (p *TerraformStore) IncrementTerraformModuleDownloadCount(ctx context.Context, repoID, id string) error {
	err := p.q.IncrementTerraformDownloadCount(ctx, dbsqlc.IncrementTerraformDownloadCountParams{ID: id, RepositoryID: repoID})
	if err != nil {
		return fmt.Errorf("failed to count terraform module download: %w", err)
	}

	return nil
}

// TerraformModuleStamp fingerprints the live versions of one module.
func (p *TerraformStore) TerraformModuleStamp(ctx context.Context, repoID, name, system string) (string, error) {
	stamp, err := p.q.TerraformPackageStamp(ctx, dbsqlc.TerraformPackageStampParams{
		RepositoryID: repoID, NameKey: terraformModuleKey(name, system),
	})
	if err != nil {
		return "", fmt.Errorf("failed to stamp terraform module: %w", err)
	}

	return stamp, nil
}

// CreateTerraformProvider stores one provider platform zip. A live platform
// with the same coordinates is ErrDuplicate. The first platform of a version
// sets the version's protocols; the publish handler enforces that the later
// platforms declare the same.
func (p *TerraformStore) CreateTerraformProvider(ctx context.Context, prov *models.TerraformProvider) error {
	protocols := prov.Protocols
	if protocols == nil {
		protocols = []string{}
	}

	key := terraformProviderKey(prov.Type)
	filename := terraformProviderFilename(prov.Type, prov.Version, prov.OS, prov.Arch)

	out, err := p.writePackage(ctx, &PackageWrite{
		RepoID:      prov.RepositoryID,
		Format:      "terraform",
		Name:        key,
		NameKey:     key,
		PkgAttrs:    map[string]any{"kind": "provider", "type": prov.Type},
		Version:     prov.Version,
		VersionKey:  prov.Version,
		VersionMeta: terraformProviderMetadata{Protocols: protocols},
		CreatedAt:   prov.CreatedAt,
		Files: []FileWrite{{
			Path:     terraformProviderPath(prov.Type, prov.Version, prov.OS, prov.Arch),
			Filename: filename,
			Arch:     prov.OS + "_" + prov.Arch,
			Attrs:    map[string]any{"os": prov.OS, "arch": prov.Arch},
			Blob:     BlobWrite{SHA256: prov.Checksum, Key: prov.BlobKey, MediaType: "application/zip", Size: prov.SizeBytes},
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to create terraform provider: %w", err)
	}

	prov.ID, prov.CreatedAt, prov.UpdatedAt = out.Files[0].ID, out.Files[0].CreatedAt, out.Files[0].UpdatedAt

	return nil
}

// GetTerraformProvider returns the live platform zip, or (nil, nil).
func (p *TerraformStore) GetTerraformProvider(
	ctx context.Context,
	repoID, typ, version, osName, arch string,
) (*models.TerraformProvider, error) {
	row, err := p.q.GetTerraformProvider(ctx, dbsqlc.GetTerraformProviderParams{
		RepositoryID: repoID, Path: terraformProviderPath(typ, version, osName, arch),
	})

	return mapRow(row, err, "get terraform provider", terraformProviderFromRow)
}

// ListTerraformProviderPlatforms returns every live platform zip of one
// provider type, ordered by version, os and arch.
func (p *TerraformStore) ListTerraformProviderPlatforms(ctx context.Context, repoID, typ string) ([]*models.TerraformProvider, error) {
	rows, err := p.q.ListTerraformProviderPlatforms(ctx, dbsqlc.ListTerraformProviderPlatformsParams{
		RepositoryID: repoID, NameKey: terraformProviderKey(typ),
	})

	return mapRows(rows, err, "list terraform provider platforms", terraformProviderFromRow)
}

// ListTerraformProviderVersionPlatforms returns every live platform zip of one
// provider version, ordered by os and arch.
func (p *TerraformStore) ListTerraformProviderVersionPlatforms(
	ctx context.Context,
	repoID, typ, version string,
) ([]*models.TerraformProvider, error) {
	rows, err := p.q.ListTerraformProviderVersionPlatforms(ctx, dbsqlc.ListTerraformProviderVersionPlatformsParams{
		RepositoryID: repoID, NameKey: terraformProviderKey(typ), Version: version,
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

// DeleteTerraformProvider hard-deletes one live provider platform file; the
// version goes with its last platform.
func (p *TerraformStore) DeleteTerraformProvider(ctx context.Context, repoID, id string) error {
	if err := p.deleteLiveFiles(ctx, repoID, []string{id}); err != nil {
		return fmt.Errorf("failed to delete terraform provider: %w", err)
	}

	return nil
}

// IncrementTerraformProviderDownloadCount counts one download of a provider zip.
func (p *TerraformStore) IncrementTerraformProviderDownloadCount(ctx context.Context, repoID, id string) error {
	err := p.q.IncrementTerraformDownloadCount(ctx, dbsqlc.IncrementTerraformDownloadCountParams{ID: id, RepositoryID: repoID})
	if err != nil {
		return fmt.Errorf("failed to count terraform provider download: %w", err)
	}

	return nil
}

// TerraformProviderStamp fingerprints the live platforms of one provider type.
func (p *TerraformStore) TerraformProviderStamp(ctx context.Context, repoID, typ string) (string, error) {
	stamp, err := p.q.TerraformPackageStamp(ctx, dbsqlc.TerraformPackageStampParams{
		RepositoryID: repoID, NameKey: terraformProviderKey(typ),
	})
	if err != nil {
		return "", fmt.Errorf("failed to stamp terraform provider: %w", err)
	}

	return stamp, nil
}
