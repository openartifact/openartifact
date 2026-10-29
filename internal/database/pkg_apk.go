// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// APKStore owns the Alpine apk packages and the signing key.
//
// An apk lives on the generic package tables (packages P, package_versions V,
// package_files F); there is no side table: the APKINDEX stanza fields are small,
// read only as a whole by the index generation and ride in V.metadata. Grain 1:1: one
// .apk of the mirror tree branch/repo/arch/name-version.apk is one P (shared by
// branch/repo/name), one V and one F. The mapping:
//
//	P  name, name_key = branch/repo/name; attrs = {branch, repo, name}
//	V  version        = version
//	   version_key    = version|arch (arch is the mirror directory, not pkg_arch)
//	   metadata       = {pkg_arch, checksum, data_sha256, description, url, license, origin,
//	                    maintainer, commit, depends[], provides[], install_if[],
//	                    provider_priority}; all keys written, the arrays keep their order
//	F  path           = branch/repo/arch/name-version.apk; filename = name-version.apk
//	   arch           = the mirror directory; blob_id = the .apk blob; size_bytes = NULL
//	   part           = ''; checksums = {}
//	   attrs          = {build_time, installed_size} (only when not zero)
//	   download_count = the download counter
type APKStore struct{ *core }

// apkMediaType is the media type of a stored .apk (concatenated gzip streams).
const apkMediaType = "application/gzip"

// apkMeta is package_versions.metadata of an apk.
type apkMeta struct {
	PkgArch          string   `json:"pkg_arch"`
	Checksum         string   `json:"checksum"`
	DataSHA256       string   `json:"data_sha256"`
	Description      string   `json:"description"`
	URL              string   `json:"url"`
	License          string   `json:"license"`
	Origin           string   `json:"origin"`
	Maintainer       string   `json:"maintainer"`
	Commit           string   `json:"commit"`
	Depends          []string `json:"depends"`
	Provides         []string `json:"provides"`
	InstallIf        []string `json:"install_if"`
	ProviderPriority int      `json:"provider_priority"`
}

// apkPackageRow lists the sqlc row types apkPackageFromRow converts; they share the columns of GetApkPackageFileRow.
type apkPackageRow interface {
	dbsqlc.GetApkPackageFileRow | dbsqlc.ListApkPackagesRow
}

func apkPackageFromRow[R apkPackageRow](in R) (*models.ApkPackage, error) {
	row := dbsqlc.GetApkPackageFileRow(in)

	var meta apkMeta
	if err := json.Unmarshal(row.Metadata, &meta); err != nil {
		return nil, fmt.Errorf("decode apk metadata of %s: %w", row.ID, err)
	}

	return &models.ApkPackage{
		ID:               row.ID,
		RepositoryID:     row.RepositoryID,
		Branch:           row.Branch,
		Repo:             row.Repo,
		Arch:             row.Arch,
		PkgArch:          meta.PkgArch,
		Name:             row.Name,
		Version:          row.Version,
		Checksum:         meta.Checksum,
		DataHash:         meta.DataSHA256,
		Description:      meta.Description,
		URL:              meta.URL,
		License:          meta.License,
		Origin:           meta.Origin,
		Maintainer:       meta.Maintainer,
		Commit:           meta.Commit,
		BuildTime:        row.BuildTime,
		InstalledSize:    row.InstalledSize,
		Depends:          meta.Depends,
		Provides:         meta.Provides,
		InstallIf:        meta.InstallIf,
		ProviderPriority: meta.ProviderPriority,
		BlobKey:          row.BlobKey,
		BlobSHA256:       row.BlobSha256,
		SizeBytes:        row.SizeBytes,
		DownloadCount:    row.DownloadCount,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}, nil
}

// apkPath is the mirror-tree path of a package: branch/repo/arch/name-version.apk.
func apkPath(branch, repo, arch, name, version string) string {
	return branch + "/" + repo + "/" + arch + "/" + name + "-" + version + ".apk"
}

// CreateApkPackage stores one .apk. A live row with the same coordinates is
// ErrDuplicate.
func (p *APKStore) CreateApkPackage(ctx context.Context, pkg *models.ApkPackage) error {
	attrs := map[string]any{}
	putNonZero(attrs, "build_time", pkg.BuildTime)
	putNonZero(attrs, "installed_size", pkg.InstalledSize)

	listName := pkg.Branch + "/" + pkg.Repo + "/" + pkg.Name

	written, err := p.writePackage(ctx, &PackageWrite{
		RepoID:   pkg.RepositoryID,
		Format:   "apk",
		Name:     listName,
		NameKey:  listName,
		PkgAttrs: map[string]any{"branch": pkg.Branch, "repo": pkg.Repo, "name": pkg.Name},
		Version:  pkg.Version, VersionKey: pkg.Version + "|" + pkg.Arch,
		VersionMeta: apkMeta{
			PkgArch:          pkg.PkgArch,
			Checksum:         pkg.Checksum,
			DataSHA256:       pkg.DataHash,
			Description:      pkg.Description,
			URL:              pkg.URL,
			License:          pkg.License,
			Origin:           pkg.Origin,
			Maintainer:       pkg.Maintainer,
			Commit:           pkg.Commit,
			Depends:          nonNilStrings(pkg.Depends),
			Provides:         nonNilStrings(pkg.Provides),
			InstallIf:        nonNilStrings(pkg.InstallIf),
			ProviderPriority: pkg.ProviderPriority,
		},
		Exclusive: true,
		CreatedAt: pkg.CreatedAt,
		Files: []FileWrite{{
			Path:     apkPath(pkg.Branch, pkg.Repo, pkg.Arch, pkg.Name, pkg.Version),
			Filename: pkg.Name + "-" + pkg.Version + ".apk",
			Blob:     BlobWrite{SHA256: pkg.BlobSHA256, Key: pkg.BlobKey, MediaType: apkMediaType, Size: pkg.SizeBytes},
			Arch:     pkg.Arch,
			Attrs:    attrs,
		}},
	})
	if err != nil {
		return err
	}

	pkg.ID, pkg.CreatedAt, pkg.UpdatedAt = written.Files[0].ID, written.Files[0].CreatedAt, written.Files[0].UpdatedAt

	return nil
}

// GetApkPackageFile returns the live .apk, or (nil, nil).
func (p *APKStore) GetApkPackageFile(ctx context.Context, repoID, branch, repo, arch, name, version string) (*models.ApkPackage, error) {
	row, err := p.q.GetApkPackageFile(ctx, dbsqlc.GetApkPackageFileParams{
		RepositoryID: repoID, Path: apkPath(branch, repo, arch, name, version),
	})

	return mapRowErr(row, err, "get apk package", apkPackageFromRow)
}

// ListApkPackages returns every live package of the repository ordered by
// branch, repo, arch, name and version.
func (p *APKStore) ListApkPackages(ctx context.Context, repoID string) ([]*models.ApkPackage, error) {
	rows, err := p.q.ListApkPackages(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list apk packages: %w", err)
	}

	return mapRowsErr(rows, apkPackageFromRow)
}

// DeleteApkPackage removes one live package row (and its version and package
// when nothing else holds them).
func (p *APKStore) DeleteApkPackage(ctx context.Context, id, repoID string) error {
	if _, err := p.deleteLiveFiles(ctx, repoID, nil, []string{id}); err != nil {
		return fmt.Errorf("failed to delete apk package: %w", err)
	}

	return nil
}

// IncrementApkDownloadCount counts one download of a package.
func (p *APKStore) IncrementApkDownloadCount(ctx context.Context, id, repoID string) error {
	if err := p.q.IncrementApkDownloadCount(ctx, dbsqlc.IncrementApkDownloadCountParams{ID: id, RepositoryID: repoID}); err != nil {
		return fmt.Errorf("failed to count apk download: %w", err)
	}

	return nil
}

// GetApkSigningKey returns the repository's signing key, or (nil, nil).
func (p *APKStore) GetApkSigningKey(ctx context.Context, repoID string) (*models.ApkSigningKey, error) {
	row, err := p.q.GetApkSigningKey(ctx, repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get apk signing key: %w", err)
	}

	return &models.ApkSigningKey{
		RepositoryID:  row.RepositoryID,
		KeyName:       row.KeyName,
		Fingerprint:   row.Fingerprint,
		PublicKeyPEM:  row.PublicKey,
		PrivateKeyEnc: row.PrivateKey,
		CreatedBy:     row.CreatedBy,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}, nil
}

// CreateApkSigningKeyIfAbsent stores the key unless the repository has one;
// created reports whether it did.
func (p *APKStore) CreateApkSigningKeyIfAbsent(ctx context.Context, k *models.ApkSigningKey) (bool, error) {
	n, err := p.q.CreateApkSigningKeyIfAbsent(ctx, dbsqlc.CreateApkSigningKeyIfAbsentParams{
		RepositoryID: k.RepositoryID, KeyName: k.KeyName, Fingerprint: k.Fingerprint,
		PublicKey: k.PublicKeyPEM, PrivateKey: k.PrivateKeyEnc, CreatedBy: k.CreatedBy,
	})
	if err != nil {
		return false, fmt.Errorf("failed to create apk signing key: %w", err)
	}

	return n > 0, nil
}

// ReplaceApkSigningKey stores the key, replacing the repository's current one.
func (p *APKStore) ReplaceApkSigningKey(ctx context.Context, k *models.ApkSigningKey) error {
	err := p.q.ReplaceApkSigningKey(ctx, dbsqlc.ReplaceApkSigningKeyParams{
		RepositoryID: k.RepositoryID, KeyName: k.KeyName, Fingerprint: k.Fingerprint,
		PublicKey: k.PublicKeyPEM, PrivateKey: k.PrivateKeyEnc, CreatedBy: k.CreatedBy,
	})
	if err != nil {
		return fmt.Errorf("failed to replace apk signing key: %w", err)
	}

	return nil
}
