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

// SBTStore owns the sbt artifacts.
//
// sbt on the generic package tables (mapping spec /tmp/oa-plug/schema/119_sbt.sql,
// restated as the maintainers' reference). No side table.
//
//	P  packages         name = name_key = group_id ':' artifact_id
//	                    attrs = {"group_id", "artifact_id"}
//	V  package_versions version = version_key = version; metadata {}
//	F  package_files    filename = the file name; arch = the scala version ('' unless recorded);
//	                    path = group_id '/' artifact_id '/' version '/' filename
//	                    (repository-wide live-unique: the lookup key);
//	                    part '', role primary, listed, unit_key NULL, attrs {}
//
// An upload to an existing path repoints the file at the new blob.
type SBTStore struct{ *core }

// sbtPackageName is the package identity (name and name_key) of an artifact.
func sbtPackageName(groupID, artifactID string) string { return groupID + ":" + artifactID }

func sbtPath(groupID, artifactID, version, filename string) string {
	return groupID + "/" + artifactID + "/" + version + "/" + filename
}

// SBTModuleExists reports whether the repository holds a live version of the artifact.
func (p *SBTStore) SBTModuleExists(ctx context.Context, repoID, groupID, artifactID string) (bool, error) {
	exists, err := p.q.SBTModuleExists(
		ctx,
		dbsqlc.SBTModuleExistsParams{RepositoryID: repoID, NameKey: sbtPackageName(groupID, artifactID)},
	)
	if err != nil {
		return false, fmt.Errorf("failed to check sbt artifact: %w", err)
	}

	return exists, nil
}

// CreateSBTArtifact stores an SBT artifact; a live artifact with the same
// coordinates is repointed at the new blob.
func (p *SBTStore) CreateSBTArtifact(ctx context.Context, artifact *models.SBTArtifact) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, artifact.SHA256, artifact.BlobKey, "application/java-archive", artifact.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for sbt artifact: %w", err)
	}

	written, err := p.upsertPackageFile(ctx, &PackageWrite{
		RepoID:     artifact.RepositoryID,
		Format:     "sbt",
		Name:       sbtPackageName(artifact.GroupID, artifact.ArtifactID),
		NameKey:    sbtPackageName(artifact.GroupID, artifact.ArtifactID),
		PkgAttrs:   map[string]any{"group_id": artifact.GroupID, "artifact_id": artifact.ArtifactID},
		Version:    artifact.Version,
		VersionKey: artifact.Version,
		CreatedAt:  artifact.CreatedAt,
		Files: []FileWrite{{
			Path:     sbtPath(artifact.GroupID, artifact.ArtifactID, artifact.Version, artifact.Filename),
			Filename: artifact.Filename,
			Arch:     artifact.ScalaVersion,
		}},
	}, blobID, nil)
	if err != nil {
		return fmt.Errorf("failed to create sbt artifact: %w", err)
	}

	artifact.ID, artifact.CreatedAt, artifact.UpdatedAt = written.ID, written.CreatedAt, written.UpdatedAt

	return nil
}

func (p *SBTStore) GetSBTArtifact(
	ctx context.Context,
	repoID, groupID, artifactID, version, filename string,
) (*models.SBTArtifact, error) {
	row, err := p.q.GetSBTArtifact(ctx, dbsqlc.GetSBTArtifactParams{
		RepositoryID: repoID,
		Path:         sbtPath(groupID, artifactID, version, filename),
	})

	return mapRow(row, err, "get sbt artifact", func(row dbsqlc.GetSBTArtifactRow) *models.SBTArtifact {
		return &models.SBTArtifact{
			ID:           row.ID,
			RepositoryID: row.RepositoryID,
			GroupID:      row.GroupID,
			ArtifactID:   row.ArtifactID,
			Version:      row.Version,
			ScalaVersion: row.ScalaVersion,
			Filename:     row.Filename,
			BlobKey:      row.BlobKey,
			SizeBytes:    row.SizeBytes,
			SHA256:       row.Sha256,
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		}
	})
}

func (p *SBTStore) DeleteSBTArtifact(ctx context.Context, id string) error {
	if err := p.deleteLiveFile(ctx, id); err != nil {
		return fmt.Errorf("failed to delete sbt artifact: %w", err)
	}

	return nil
}
