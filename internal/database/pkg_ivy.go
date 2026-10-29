// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// IvyStore owns the Ivy artifacts.
//
// Ivy on the generic package tables (mapping spec /tmp/oa-plug/schema/109_ivy.sql,
// restated as the maintainers' reference). No side table.
//
//	P  packages         name = name_key = organisation ':' module
//	                    attrs = {"organisation", "module"}
//	V  package_versions version = version_key = revision; metadata {}
//	F  package_files    filename = the artifact name, with '.' extension appended
//	                    only when the name does not end with it already (the handler
//	                    stores the whole file name, possibly with a sub-directory);
//	                    path = organisation '/' module '/' revision '/' filename
//	                    (repository-wide live-unique: the lookup key);
//	                    arch '', part '', role primary, listed, unit_key NULL,
//	                    attrs = {"artifact_name", "artifact_type", "artifact_ext"}
//
// An upload to an existing path repoints the file at the new blob (ivy
// publishes with overwrite).
type IvyStore struct{ *core }

// ivyPackageName is the package identity (name and name_key) of a module.
func ivyPackageName(organisation, module string) string { return organisation + ":" + module }

// ivyFilename is what the client downloads: artifactName, extended by
// '.'+artifactExt unless it already ends with it.
func ivyFilename(artifactName, artifactExt string) string {
	if artifactExt == "" || strings.HasSuffix(artifactName, "."+artifactExt) {
		return artifactName
	}

	return artifactName + "." + artifactExt
}

func ivyPath(organisation, module, revision, filename string) string {
	return organisation + "/" + module + "/" + revision + "/" + filename
}

// CreateIvyArtifact stores an Ivy artifact; a live artifact with the same
// coordinates is repointed at the new blob.
func (p *IvyStore) CreateIvyArtifact(ctx context.Context, artifact *models.IvyArtifact) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, artifact.SHA256, artifact.BlobKey, "application/java-archive", artifact.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for ivy artifact: %w", err)
	}

	filename := ivyFilename(artifact.ArtifactName, artifact.ArtifactExt)

	written, err := p.upsertPackageFile(ctx, &PackageWrite{
		RepoID:     artifact.RepositoryID,
		Format:     "ivy",
		Name:       ivyPackageName(artifact.Organisation, artifact.Module),
		NameKey:    ivyPackageName(artifact.Organisation, artifact.Module),
		PkgAttrs:   map[string]any{"organisation": artifact.Organisation, "module": artifact.Module},
		Version:    artifact.Revision,
		VersionKey: artifact.Revision,
		CreatedAt:  artifact.CreatedAt,
		Files: []FileWrite{{
			Path:     ivyPath(artifact.Organisation, artifact.Module, artifact.Revision, filename),
			Filename: filename,
			Attrs: map[string]any{
				"artifact_name": artifact.ArtifactName,
				"artifact_type": artifact.ArtifactType,
				"artifact_ext":  artifact.ArtifactExt,
			},
		}},
	}, blobID, nil)
	if err != nil {
		return fmt.Errorf("failed to create ivy artifact: %w", err)
	}

	artifact.ID, artifact.CreatedAt, artifact.UpdatedAt = written.ID, written.CreatedAt, written.UpdatedAt

	return nil
}

func (p *IvyStore) GetIvyArtifact(
	ctx context.Context,
	repoID, organisation, module, revision, artifactName, artifactExt string,
) (*models.IvyArtifact, error) {
	row, err := p.q.GetIvyArtifact(ctx, dbsqlc.GetIvyArtifactParams{
		RepositoryID: repoID,
		Path:         ivyPath(organisation, module, revision, ivyFilename(artifactName, artifactExt)),
	})

	return mapRow(row, err, "get ivy artifact", func(row dbsqlc.GetIvyArtifactRow) *models.IvyArtifact {
		return &models.IvyArtifact{
			ID:           row.ID,
			RepositoryID: row.RepositoryID,
			Organisation: row.Organisation,
			Module:       row.Module,
			Revision:     row.Revision,
			ArtifactName: row.ArtifactName,
			ArtifactType: row.ArtifactType,
			ArtifactExt:  row.ArtifactExt,
			BlobKey:      row.BlobKey,
			SizeBytes:    row.SizeBytes,
			SHA256:       row.Sha256,
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		}
	})
}

func (p *IvyStore) DeleteIvyArtifact(ctx context.Context, id string) error {
	if err := p.deleteLiveFile(ctx, id); err != nil {
		return fmt.Errorf("failed to delete ivy artifact: %w", err)
	}

	return nil
}

// IvyModuleExists reports whether the repository holds a live version of the module.
func (p *IvyStore) IvyModuleExists(ctx context.Context, repoID, organisation, module string) (bool, error) {
	exists, err := p.q.IvyModuleExists(
		ctx,
		dbsqlc.IvyModuleExistsParams{RepositoryID: repoID, NameKey: ivyPackageName(organisation, module)},
	)
	if err != nil {
		return false, fmt.Errorf("failed to check ivy module: %w", err)
	}

	return exists, nil
}
