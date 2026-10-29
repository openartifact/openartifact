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

// npm on the generic package tables, without a side table: dist-tags are
// package_tags.
//
//	packages          one row per npm package; created by the first publish or
//	                  dist-tag upsert, removed by DeleteNPMPackage.
//	                  name = name_key = full_name ('@scope/name' or 'name'),
//	                  attrs = {"scope": "", "name": <unscoped name>,
//	                  "latest_version": "", "packument_stale": bool}
//	                  (packument_stale is volatile worker state; absent reads as true).
//	package_versions  one row per published version incarnation;
//	                  version = version_key = the version,
//	                  metadata = {"manifest_json": <package.json manifest>}.
//	package_files     exactly one file per version, the tarball (role primary):
//	                  path = full_name + "/-/" + tarball filename (repository-wide
//	                  unique, the bare filename repeats across scopes),
//	                  filename = tarball filename,
//	                  checksums = {"shasum": sha1 hex, "integrity": SRI string}
//	                  (a key is omitted when empty),
//	                  unit_key = full_name + "@" + version (federation unit).
//	package_tags      dist-tags: tag, version (a version STRING resolved at read
//	                  time, so a tag of a trashed version is hidden and visible
//	                  again after a restore), unit_key = full_name + "#" + tag.
//
// The packument's ETag and render-cache key is packages.updated_at. The generic
// tables have no trigger for it, so every statement here that changes what the
// packument shows (version insert, manifest update, version delete, dist-tag
// upsert/delete, mark stale, latest-version update) moves it in the same
// statement or transaction. The trash/purge engine does the same for trash,
// restore and purge of a version file (see the report of this port for the
// statements).
//
// NPMVersion.ID is the id of the tarball package_files row (the artifact id);
// the version row has its own id that no caller needs.

// NPMStore owns the npm packages, versions and dist-tags.
type NPMStore struct{ *core }

// npmTarballPath is the repository-unique locator of a tarball.
func npmTarballPath(fullName, filename string) string {
	return fullName + "/-/" + filename
}

func (p *NPMStore) CreateNPMPackage(ctx context.Context, pkg *models.NPMPackage) (*models.NPMPackage, error) {
	row, err := p.q.CreateNPMPackage(ctx, dbsqlc.CreateNPMPackageParams{
		RepositoryID:   pkg.RepositoryID,
		Scope:          pkg.Scope,
		Name:           pkg.Name,
		FullName:       pkg.FullName,
		LatestVersion:  pkg.LatestVersion,
		PackumentStale: pkg.PackumentStale,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create npm package: %w", err)
	}

	pkg.ID = row.ID
	pkg.CreatedAt = row.CreatedAt
	pkg.UpdatedAt = row.UpdatedAt

	return pkg, nil
}

func (p *NPMStore) GetNPMPackageByName(ctx context.Context, repoID, fullName string) (*models.NPMPackage, error) {
	row, err := p.q.GetNPMPackageByName(ctx, dbsqlc.GetNPMPackageByNameParams{
		RepositoryID: repoID,
		NameKey:      fullName,
	})
	if isNoRows(err) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get npm package by name: %w", err)
	}

	return &models.NPMPackage{
		ID:             row.ID,
		RepositoryID:   row.RepositoryID,
		Scope:          row.Scope,
		Name:           row.Name,
		FullName:       row.FullName,
		LatestVersion:  row.LatestVersion,
		PackumentStale: row.PackumentStale,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}

func (p *NPMStore) DeleteNPMPackage(ctx context.Context, packageID string) error {
	if err := p.q.DeleteNPMPackage(ctx, packageID); err != nil {
		return fmt.Errorf("failed to delete npm package: %w", err)
	}

	return nil
}

func (p *NPMStore) MarkPackumentStale(ctx context.Context, packageID string) error {
	if err := p.q.MarkPackumentStale(ctx, packageID); err != nil {
		return fmt.Errorf("failed to mark packument stale: %w", err)
	}

	return nil
}

// CreateNPMVersion writes the version and its tarball file in one transaction.
// v.BlobID is an existing blobs.id. A live version of the same number, or a
// live tarball with the same path, fails with ErrDuplicate. On success v.ID is
// the id of the tarball file.
func (p *NPMStore) CreateNPMVersion(ctx context.Context, v *models.NPMVersion) (*models.NPMVersion, error) {
	manifest := json.RawMessage(v.ManifestJSON)
	if len(manifest) == 0 {
		manifest = json.RawMessage(`{}`)
	}

	verMeta, err := json.Marshal(map[string]json.RawMessage{"manifest_json": manifest})
	if err != nil {
		return nil, fmt.Errorf("failed to encode npm version metadata: %w", err)
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin npm version write: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	q := p.q.WithTx(tx)

	pkg, err := q.GetNPMPackageIdentity(ctx, v.PackageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get npm package of version: %w", err)
	}

	sums := make(map[string]string, 2)
	if v.Shasum != "" {
		sums["shasum"] = v.Shasum
	}

	if v.Integrity != "" {
		sums["integrity"] = v.Integrity
	}

	// writePackageTx upserts the package by its name (bumping updated_at, the
	// packument ETag) and keeps the attrs untouched; the blob is already stored.
	var blobID *string
	if v.BlobID != "" {
		blobID = &v.BlobID
	}

	written, err := p.writePackageTx(ctx, q, &PackageWrite{
		RepoID:     pkg.RepositoryID,
		Format:     "npm",
		Name:       pkg.Name,
		NameKey:    pkg.Name,
		Version:    v.Version,
		VersionKey: v.Version,
		Exclusive:  true,
		CreatedAt:  v.CreatedAt,
		Files: []FileWrite{{
			Path:      npmTarballPath(pkg.Name, v.TarballFilename),
			Filename:  v.TarballFilename,
			Checksums: sums,
			UnitKey:   pkg.Name + "@" + v.Version,
		}},
	}, []*string{blobID}, json.RawMessage(`{}`), verMeta)
	if err != nil {
		return nil, fmt.Errorf("failed to create npm version: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit npm version: %w", err)
	}

	v.ID = written.Files[0].ID
	v.CreatedAt = written.Files[0].CreatedAt

	return v, nil
}

func (p *NPMStore) GetNPMVersion(ctx context.Context, packageID, version string) (*models.NPMVersion, error) {
	row, err := p.q.GetNPMVersion(ctx, dbsqlc.GetNPMVersionParams{
		PackageID:  packageID,
		VersionKey: version,
	})
	if isNoRows(err) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get npm version: %w", err)
	}

	return &models.NPMVersion{
		ID:              row.ID,
		PackageID:       row.PackageID,
		Version:         row.Version,
		ManifestJSON:    row.ManifestJson,
		Integrity:       row.Integrity,
		Shasum:          row.Shasum,
		BlobID:          row.BlobID,
		TarballFilename: row.TarballFilename,
		BlobSHA256:      row.BlobSha256,
		BlobSizeBytes:   row.BlobSizeBytes,
		CreatedAt:       row.CreatedAt,
	}, nil
}

func (p *NPMStore) GetNPMVersionsByPackage(ctx context.Context, packageID string) ([]*models.NPMVersion, error) {
	rows, err := p.q.GetNPMVersionsByPackage(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("failed to query npm versions: %w", err)
	}

	var versions []*models.NPMVersion

	for _, row := range rows {
		versions = append(versions, &models.NPMVersion{
			ID:              row.ID,
			PackageID:       row.PackageID,
			Version:         row.Version,
			ManifestJSON:    row.ManifestJson,
			Integrity:       row.Integrity,
			Shasum:          row.Shasum,
			BlobID:          row.BlobID,
			TarballFilename: row.TarballFilename,
			BlobSHA256:      row.BlobSha256,
			BlobSizeBytes:   row.BlobSizeBytes,
			CreatedAt:       row.CreatedAt,
		})
	}

	return versions, nil
}

// GetNPMVersionStates returns number and deprecation of every live version
// of the package, without the manifests.
func (p *NPMStore) GetNPMVersionStates(ctx context.Context, packageID string) ([]models.NPMVersionState, error) {
	rows, err := p.q.GetNPMVersionStates(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("failed to query npm version states: %w", err)
	}

	states := make([]models.NPMVersionState, len(rows))
	for i, row := range rows {
		states[i] = models.NPMVersionState{Version: row.Version, Deprecated: row.Deprecated}
	}

	return states, nil
}

func (p *NPMStore) UpdateNPMPackageLatestVersion(ctx context.Context, packageID, latestVersion string) error {
	if err := p.q.UpdateNPMPackageLatestVersion(ctx, dbsqlc.UpdateNPMPackageLatestVersionParams{
		ID:            packageID,
		LatestVersion: latestVersion,
	}); err != nil {
		return fmt.Errorf("failed to update npm package latest version: %w", err)
	}

	return nil
}

// UpdateNPMVersionManifest replaces the stored manifest of a live version
// (used for `npm deprecate`).
func (p *NPMStore) UpdateNPMVersionManifest(ctx context.Context, packageID, version string, manifest []byte) error {
	if err := p.q.UpdateNPMVersionManifest(ctx, dbsqlc.UpdateNPMVersionManifestParams{
		PackageID: packageID,
		Version:   version,
		Manifest:  manifest,
	}); err != nil {
		return fmt.Errorf("failed to update npm version manifest: %w", err)
	}

	return nil
}

// DeleteNPMVersion hard-deletes the live version with its tarball row and the
// dist-tags that named it (unless a trashed incarnation of the same version
// remains), and bumps the packument ETag, in one transaction. The package row
// stays: DeleteNPMPackage removes it once no version row is left.
func (p *NPMStore) DeleteNPMVersion(ctx context.Context, packageID, version string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin npm version delete: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	q := p.q.WithTx(tx)

	versionIDs, err := q.DeleteNPMLiveVersionFiles(ctx, dbsqlc.DeleteNPMLiveVersionFilesParams{
		PackageID:  packageID,
		VersionKey: version,
	})
	if err != nil {
		return fmt.Errorf("failed to delete npm version files: %w", err)
	}

	if len(versionIDs) == 0 {
		return nil
	}

	if _, err := q.PruneEmptyVersions(ctx, versionIDs); err != nil {
		return fmt.Errorf("failed to delete npm version: %w", err)
	}

	if err := q.DeleteNPMTagsOfVersion(ctx, dbsqlc.DeleteNPMTagsOfVersionParams{
		PackageID: packageID,
		Version:   version,
	}); err != nil {
		return fmt.Errorf("failed to delete npm tags of version: %w", err)
	}

	if err := q.TouchPackage(ctx, packageID); err != nil {
		return fmt.Errorf("failed to touch npm package: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit npm version delete: %w", err)
	}

	return nil
}

func (p *NPMStore) UpsertNPMDistTag(ctx context.Context, t *models.NPMDistTag) (*models.NPMDistTag, error) {
	row, err := p.q.UpsertNPMDistTag(ctx, dbsqlc.UpsertNPMDistTagParams{
		PackageID: t.PackageID,
		Tag:       t.Tag,
		Version:   t.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upsert npm dist tag: %w", err)
	}

	t.ID = row.ID
	t.UpdatedAt = row.UpdatedAt

	return t, nil
}

func (p *NPMStore) GetNPMDistTags(ctx context.Context, packageID string) (map[string]string, error) {
	rows, err := p.q.GetNPMDistTags(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("failed to query npm dist tags: %w", err)
	}

	tags := make(map[string]string)

	for _, row := range rows {
		tags[row.Tag] = row.Version
	}

	return tags, nil
}

func (p *NPMStore) DeleteNPMDistTag(ctx context.Context, packageID, tag string) error {
	if err := p.q.DeleteNPMDistTag(ctx, dbsqlc.DeleteNPMDistTagParams{
		PackageID: packageID,
		Tag:       tag,
	}); err != nil {
		return fmt.Errorf("failed to delete npm dist tag: %w", err)
	}

	return nil
}
