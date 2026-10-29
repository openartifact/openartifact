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

// APKStore owns the Alpine apk packages and the signing key.
type APKStore struct{ *core }

// apkMediaType is the media type of a stored .apk (concatenated gzip streams).
const apkMediaType = "application/gzip"

// apkPackageRow lists the sqlc row types apkPackageFromRow converts; they share the columns of GetApkPackageFileRow.
type apkPackageRow interface {
	dbsqlc.GetApkPackageFileRow | dbsqlc.ListApkPackagesRow
}

func apkPackageFromRow[R apkPackageRow](in R) *models.ApkPackage {
	row := dbsqlc.GetApkPackageFileRow(in)

	return &models.ApkPackage{
		ID:               row.ID,
		RepositoryID:     row.RepositoryID,
		Branch:           row.Branch,
		Repo:             row.Repo,
		Arch:             row.Arch,
		PkgArch:          row.PkgArch,
		Name:             row.Name,
		Version:          row.Version,
		Checksum:         row.Checksum,
		DataHash:         row.DataSha256,
		Description:      row.Description,
		URL:              row.Url,
		License:          row.License,
		Origin:           row.Origin,
		Maintainer:       row.Maintainer,
		Commit:           row.Commit,
		BuildTime:        row.BuildTime,
		InstalledSize:    row.InstalledSize,
		Depends:          row.Depends,
		Provides:         row.Provides,
		InstallIf:        row.InstallIf,
		ProviderPriority: int(row.ProviderPriority),
		BlobKey:          row.BlobKey,
		BlobSHA256:       row.BlobSha256,
		SizeBytes:        row.SizeBytes,
		DownloadCount:    row.DownloadCount,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

// CreateApkPackage stores one .apk. A live row with the same coordinates is a
// unique violation (IsUniqueViolation).
func (p *APKStore) CreateApkPackage(ctx context.Context, pkg *models.ApkPackage) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, pkg.BlobSHA256, pkg.BlobKey, apkMediaType, pkg.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for apk package: %w", err)
	}

	row, err := p.q.CreateApkPackage(ctx, dbsqlc.CreateApkPackageParams{
		RepositoryID:     pkg.RepositoryID,
		Branch:           pkg.Branch,
		Repo:             pkg.Repo,
		Arch:             pkg.Arch,
		PkgArch:          pkg.PkgArch,
		Name:             pkg.Name,
		Version:          pkg.Version,
		Checksum:         pkg.Checksum,
		DataSha256:       pkg.DataHash,
		Description:      pkg.Description,
		Url:              pkg.URL,
		License:          pkg.License,
		Origin:           pkg.Origin,
		Maintainer:       pkg.Maintainer,
		Commit:           pkg.Commit,
		BuildTime:        pkg.BuildTime,
		InstalledSize:    pkg.InstalledSize,
		Depends:          nonNilStrings(pkg.Depends),
		Provides:         nonNilStrings(pkg.Provides),
		InstallIf:        nonNilStrings(pkg.InstallIf),
		ProviderPriority: int32(pkg.ProviderPriority), //nolint:gosec // a small signed priority from PKGINFO
		BlobID:           blobID,
		CreatedAt:        createdAtOrNil(pkg.CreatedAt),
	})

	return finishCreate(insertedRow(row), err, "create apk package", &pkg.ID, &pkg.CreatedAt, &pkg.UpdatedAt)
}

// GetApkPackageFile returns the live .apk, or (nil, nil).
func (p *APKStore) GetApkPackageFile(ctx context.Context, repoID, branch, repo, arch, name, version string) (*models.ApkPackage, error) {
	row, err := p.q.GetApkPackageFile(ctx, dbsqlc.GetApkPackageFileParams{
		RepositoryID: repoID, Branch: branch, Repo: repo, Arch: arch, Name: name, Version: version,
	})

	return mapRow(row, err, "get apk package", apkPackageFromRow)
}

// ListApkPackages returns every live package of the repository ordered by
// branch, repo, arch, name and version.
func (p *APKStore) ListApkPackages(ctx context.Context, repoID string) ([]*models.ApkPackage, error) {
	rows, err := p.q.ListApkPackages(ctx, repoID)

	return mapRows(rows, err, "list apk packages", apkPackageFromRow)
}

// DeleteApkPackage removes one package row.
func (p *APKStore) DeleteApkPackage(ctx context.Context, id, repoID string) error {
	if err := p.q.DeleteApkPackage(ctx, dbsqlc.DeleteApkPackageParams{ID: id, RepositoryID: repoID}); err != nil {
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
