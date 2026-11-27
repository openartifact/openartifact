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

// PubStore owns the Dart and Flutter pub packages.
//
// Mapping of a package version onto the generic package tables (one published
// archive is one version and one file; the pub package is the package):
//
//	packages          name = name_key = the package name (lower-case letters,
//	                  digits and underscores)
//	package_versions  version = version_key = the package version; metadata
//	                  {pubspec: the pubspec.yaml of the archive as JSON}
//	package_files     path name/version/filename, filename name-version.tar.gz,
//	                  the archive blob, role primary, no unit_key (pub is not
//	                  federated)
//
// The id a client sees is package_files.id. A duplicate publish is a live
// version with a live file (ErrDuplicate).
type PubStore struct{ *core }

const pubMediaType = "application/gzip"

// pubMetadata is package_versions.metadata of a package version.
type pubMetadata struct {
	Pubspec json.RawMessage `json:"pubspec"`
}

func pubFromRow(row dbsqlc.ListPubVersionsRow) (*models.PubPackage, error) {
	var meta pubMetadata

	if err := json.Unmarshal(row.Metadata, &meta); err != nil {
		return nil, fmt.Errorf("decode pub package metadata: %w", err)
	}

	return &models.PubPackage{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Name:         row.Name,
		Version:      row.Version,
		Filename:     row.Filename,
		BlobKey:      row.BlobKey,
		SizeBytes:    row.SizeBytes,
		SHA256:       row.Sha256,
		Pubspec:      meta.Pubspec,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

// CreatePubPackage stores one package version. A live version with the same
// name and version is ErrDuplicate.
func (p *PubStore) CreatePubPackage(ctx context.Context, c *models.PubPackage) error {
	spec := c.Pubspec
	if len(spec) == 0 {
		spec = json.RawMessage(`{}`)
	}

	out, err := p.writePackage(ctx, &PackageWrite{
		RepoID:      c.RepositoryID,
		Format:      "pub",
		Name:        c.Name,
		NameKey:     c.Name,
		Version:     c.Version,
		VersionKey:  c.Version,
		VersionMeta: pubMetadata{Pubspec: spec},
		Exclusive:   true,
		CreatedAt:   c.CreatedAt,
		Files: []FileWrite{{
			Path:     c.Name + "/" + c.Version + "/" + c.Filename,
			Filename: c.Filename,
			Blob:     BlobWrite{SHA256: c.SHA256, Key: c.BlobKey, MediaType: pubMediaType, Size: c.SizeBytes},
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to create pub package: %w", err)
	}

	c.ID, c.CreatedAt, c.UpdatedAt = out.Files[0].ID, out.Files[0].CreatedAt, out.Files[0].UpdatedAt

	return nil
}

// GetPubPackage returns the live version of a package, or (nil, nil).
func (p *PubStore) GetPubPackage(ctx context.Context, repoID, name, version string) (*models.PubPackage, error) {
	row, err := p.q.GetPubPackage(ctx, dbsqlc.GetPubPackageParams{RepositoryID: repoID, Name: name, Version: version})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get pub package: %w", err)
	}

	return pubFromRow(dbsqlc.ListPubVersionsRow(row))
}

// ListPubVersions returns every live version of one package, oldest first.
func (p *PubStore) ListPubVersions(ctx context.Context, repoID, name string) ([]*models.PubPackage, error) {
	rows, err := p.q.ListPubVersions(ctx, dbsqlc.ListPubVersionsParams{RepositoryID: repoID, Name: name})
	if err != nil {
		return nil, fmt.Errorf("failed to list pub versions: %w", err)
	}

	out := make([]*models.PubPackage, len(rows))

	for i, row := range rows {
		if out[i], err = pubFromRow(row); err != nil {
			return nil, err
		}
	}

	return out, nil
}
