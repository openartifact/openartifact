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

// AnsibleStore owns the Ansible collections.
//
// Mapping of a collection version onto the generic package tables (one
// published tarball is one version and one file; the collection is the
// package):
//
//	packages          name = name_key = "namespace.name" (the FQCN; neither part
//	                  contains a dot), attrs {namespace, name}
//	package_versions  version = version_key = collection version; metadata
//	                  {dependencies: object, metadata: the galaxy.yml info,
//	                  requires_ansible: text, omitted when empty}
//	package_files     path namespace/name/version/filename, filename as given,
//	                  the tarball blob, role primary, no unit_key (ansible is
//	                  not federated)
//
// The id a client sees (upload response, import task URL) is package_files.id.
// A duplicate publish is a live version with a live file (ErrDuplicate).
type AnsibleStore struct{ *core }

const ansibleMediaType = "application/gzip"

// ansibleMetadata is package_versions.metadata of a collection version.
type ansibleMetadata struct {
	Dependencies    map[string]string `json:"dependencies"`
	Metadata        json.RawMessage   `json:"metadata"`
	RequiresAnsible string            `json:"requires_ansible,omitempty"`
}

func ansibleFQCN(namespace, name string) string {
	return namespace + "." + name
}

func ansibleFromRow(row dbsqlc.ListAnsibleCollectionVersionsRow) (*models.AnsibleCollection, error) {
	var meta ansibleMetadata

	if err := json.Unmarshal(row.Metadata, &meta); err != nil {
		return nil, fmt.Errorf("decode ansible collection metadata: %w", err)
	}

	if meta.Dependencies == nil {
		meta.Dependencies = map[string]string{}
	}

	return &models.AnsibleCollection{
		ID:              row.ID,
		RepositoryID:    row.RepositoryID,
		Namespace:       row.Namespace,
		Name:            row.Name,
		Version:         row.Version,
		Filename:        row.Filename,
		BlobKey:         row.BlobKey,
		SizeBytes:       row.SizeBytes,
		SHA256:          row.Sha256,
		Dependencies:    meta.Dependencies,
		Metadata:        meta.Metadata,
		RequiresAnsible: meta.RequiresAnsible,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}, nil
}

// CreateAnsibleCollection stores one collection version. A live version with
// the same namespace, name and version is ErrDuplicate.
func (p *AnsibleStore) CreateAnsibleCollection(ctx context.Context, c *models.AnsibleCollection) error {
	meta := c.Metadata
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}

	fqcn := ansibleFQCN(c.Namespace, c.Name)

	out, err := p.writePackage(ctx, &PackageWrite{
		RepoID:     c.RepositoryID,
		Format:     "ansible",
		Name:       fqcn,
		NameKey:    fqcn,
		PkgAttrs:   map[string]any{"namespace": c.Namespace, "name": c.Name},
		Version:    c.Version,
		VersionKey: c.Version,
		VersionMeta: ansibleMetadata{
			Dependencies:    nonNilMap(c.Dependencies),
			Metadata:        meta,
			RequiresAnsible: c.RequiresAnsible,
		},
		Exclusive: true,
		CreatedAt: c.CreatedAt,
		Files: []FileWrite{{
			Path:     c.Namespace + "/" + c.Name + "/" + c.Version + "/" + c.Filename,
			Filename: c.Filename,
			Blob:     BlobWrite{SHA256: c.SHA256, Key: c.BlobKey, MediaType: ansibleMediaType, Size: c.SizeBytes},
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to create ansible collection: %w", err)
	}

	c.ID, c.CreatedAt, c.UpdatedAt = out.Files[0].ID, out.Files[0].CreatedAt, out.Files[0].UpdatedAt

	return nil
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}

	return m
}

// GetAnsibleCollection returns the live version of a collection, or (nil, nil).
func (p *AnsibleStore) GetAnsibleCollection(
	ctx context.Context,
	repoID, namespace, name, version string,
) (*models.AnsibleCollection, error) {
	row, err := p.q.GetAnsibleCollection(ctx, dbsqlc.GetAnsibleCollectionParams{
		RepositoryID: repoID, Fqcn: ansibleFQCN(namespace, name), Version: version,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get ansible collection: %w", err)
	}

	return ansibleFromRow(dbsqlc.ListAnsibleCollectionVersionsRow(row))
}

// GetAnsibleCollectionByID returns the live collection version of the
// repository with the ID, or (nil, nil).
func (p *AnsibleStore) GetAnsibleCollectionByID(ctx context.Context, repoID, id string) (*models.AnsibleCollection, error) {
	row, err := p.q.GetAnsibleCollectionByID(ctx, dbsqlc.GetAnsibleCollectionByIDParams{RepositoryID: repoID, ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get ansible collection by id: %w", err)
	}

	return ansibleFromRow(dbsqlc.ListAnsibleCollectionVersionsRow(row))
}

// ListAnsibleCollectionVersions returns every live version of one collection.
func (p *AnsibleStore) ListAnsibleCollectionVersions(
	ctx context.Context,
	repoID, namespace, name string,
) ([]*models.AnsibleCollection, error) {
	rows, err := p.q.ListAnsibleCollectionVersions(ctx, dbsqlc.ListAnsibleCollectionVersionsParams{
		RepositoryID: repoID, Fqcn: ansibleFQCN(namespace, name),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list ansible collection versions: %w", err)
	}

	out := make([]*models.AnsibleCollection, len(rows))

	for i, row := range rows {
		if out[i], err = ansibleFromRow(row); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// ListAnsibleCollections returns every live version of every collection of the repository.
func (p *AnsibleStore) ListAnsibleCollections(ctx context.Context, repoID string) ([]*models.AnsibleCollection, error) {
	rows, err := p.q.ListAnsibleCollections(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list ansible collections: %w", err)
	}

	out := make([]*models.AnsibleCollection, len(rows))

	for i, row := range rows {
		if out[i], err = ansibleFromRow(dbsqlc.ListAnsibleCollectionVersionsRow(row)); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// ListAnsibleCollectionNames returns a page of the collections of the
// repository in name order, and how many collections it has.
func (p *AnsibleStore) ListAnsibleCollectionNames(
	ctx context.Context, repoID string, limit, offset int,
) ([]*models.AnsibleCollectionName, int, error) {
	total, err := p.q.CountAnsibleCollectionNames(ctx, repoID)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count ansible collections: %w", err)
	}

	rows, err := p.q.ListAnsibleCollectionNames(ctx, dbsqlc.ListAnsibleCollectionNamesParams{
		RepositoryID: repoID, Limit: int32(limit), Offset: int32(offset), //nolint:gosec // bounded by the caller's page size
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list ansible collections: %w", err)
	}

	out := make([]*models.AnsibleCollectionName, len(rows))
	for i, row := range rows {
		out[i] = &models.AnsibleCollectionName{
			Namespace: row.Namespace, Name: row.Name, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}
	}

	return out, int(total), nil
}

// DeleteAnsibleCollection hard-deletes a live collection version file; the
// version and the collection go with their last file.
func (p *AnsibleStore) DeleteAnsibleCollection(ctx context.Context, repoID, id string) error {
	if err := p.deleteLiveFilesByID(ctx, repoID, []string{id}); err != nil {
		return fmt.Errorf("failed to delete ansible collection: %w", err)
	}

	return nil
}
