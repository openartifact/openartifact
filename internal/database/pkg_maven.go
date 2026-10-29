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

// MavenStore owns the Maven artifacts and the generated Maven metadata pruning.
type MavenStore struct{ *core }

// PruneMavenMetadata deletes the generated maven-metadata.xml rows below the
// directory dir (e.g. "com/example/app/"): the artifact's own maven-metadata.xml
// and the one of each snapshot version directory, except the paths in keep. The
// regeneration calls it with the paths it just wrote, so metadata of an
// artifact (or snapshot version) that has no live file left disappears
// instead of advertising versions that were deleted or trashed. The blob shas of
// the deleted rows are returned so the caller can reclaim the objects.
func (p *MavenStore) PruneMavenMetadata(ctx context.Context, repositoryID, dir string, keep []string) ([]string, error) {
	if keep == nil {
		keep = []string{}
	}

	rows, err := p.db.QueryContext(ctx,
		`DELETE FROM repository_metadata
		  WHERE repository_id = $1 AND starts_with(path, $2) AND NOT (path = ANY($3::text[]))
		    AND (substr(path, length($2) + 1) = 'maven-metadata.xml'
		         OR (substr(path, length($2) + 1) LIKE '%-SNAPSHOT/maven-metadata.xml'
		             AND position('/' IN substr(path, length($2) + 1)) = length(substr(path, length($2) + 1)) - length('maven-metadata.xml')))
		RETURNING blob_sha256`,
		repositoryID, dir, keep,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to prune maven metadata: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var shas []string

	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, fmt.Errorf("failed to scan pruned maven metadata: %w", err)
		}

		shas = append(shas, sha)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to prune maven metadata: %w", err)
	}

	return shas, nil
}

func (p *MavenStore) CreateMavenArtifact(ctx context.Context, a *models.MavenArtifact) error {
	id, err := p.q.CreateMavenArtifact(ctx, dbsqlc.CreateMavenArtifactParams{
		RepositoryID:        a.RepositoryID,
		BlobID:              a.BlobID,
		GroupID:             a.GroupID,
		ArtifactIDCoord:     a.ArtifactCoord,
		Version:             a.Version,
		Classifier:          a.Classifier,
		Extension:           a.Extension,
		SnapshotTimestamp:   strPtr(a.SnapshotTimestamp),
		SnapshotBuildNumber: toInt32(a.SnapshotBuildNumber),
		IsSnapshot:          a.IsSnapshot,
		RepoPath:            a.RepoPath,
		CreatedAt:           createdAtOrNil(a.CreatedAt),
	})
	if err != nil {
		return fmt.Errorf("failed to create maven artifact: %w", err)
	}

	// ON CONFLICT returns the existing row id; always bind the returned id.
	a.ID = id

	return p.storeMavenChecksums(ctx, a)
}

func (p *MavenStore) GetMavenArtifactByPath(ctx context.Context, repoID, path string) (*models.MavenArtifact, error) {
	row, err := p.q.GetMavenArtifactByPath(ctx, dbsqlc.GetMavenArtifactByPathParams{
		RepositoryID: repoID,
		RepoPath:     path,
	})

	return getMavenArtifact(ctx, p, row, err, "maven artifact by path")
}

// mavenArtifactRow lists the sqlc row types mavenArtifactFromRow converts.
type mavenArtifactRow interface {
	dbsqlc.GetMavenArtifactByPathRow | dbsqlc.GetMavenArtifactsByGAVRow | dbsqlc.GetLatestSnapshotRow | dbsqlc.GetLatestSnapshotFileRow
}

func mavenArtifactFromRow[R mavenArtifactRow](in R) *models.MavenArtifact {
	row := dbsqlc.GetMavenArtifactByPathRow(in)

	return &models.MavenArtifact{
		ID:                  row.ID,
		RepositoryID:        row.RepositoryID,
		BlobID:              row.BlobID,
		GroupID:             row.GroupID,
		ArtifactCoord:       row.ArtifactIDCoord,
		Version:             row.Version,
		Classifier:          row.Classifier,
		Extension:           row.Extension,
		SnapshotTimestamp:   derefStr(row.SnapshotTimestamp),
		SnapshotBuildNumber: int(row.SnapshotBuildNumber),
		IsSnapshot:          row.IsSnapshot,
		RepoPath:            row.RepoPath,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}
}

// getMavenArtifact converts the result of a single-row sqlc query and fills in
// its checksums; (nil, nil) when there is no such artifact.
func getMavenArtifact[R mavenArtifactRow](
	ctx context.Context,
	p *MavenStore,
	row R,
	err error,
	what string,
) (*models.MavenArtifact, error) {
	art, err := mapRow(row, err, "get "+what, mavenArtifactFromRow[R])
	if art == nil || err != nil {
		return nil, err
	}

	if err := p.fillMavenChecksums(ctx, art); err != nil {
		return nil, err
	}

	return art, nil
}

func (p *MavenStore) GetMavenArtifactsByGAV(
	ctx context.Context,
	repoID, groupID, artifactID, version string,
) ([]*models.MavenArtifact, error) {
	rows, err := p.q.GetMavenArtifactsByGAV(ctx, dbsqlc.GetMavenArtifactsByGAVParams{
		RepositoryID:    repoID,
		GroupID:         groupID,
		ArtifactIDCoord: artifactID,
		Version:         version,
	})

	artifacts, err := mapRowsOrNil(rows, err, "query maven artifacts by gav", mavenArtifactFromRow)
	if err != nil {
		return nil, err
	}

	if err := p.fillMavenChecksums(ctx, artifacts...); err != nil {
		return nil, err
	}

	return artifacts, nil
}

// ListMavenGARefs loads the versions and snapshot files of one group:artifact
// in a single narrow query (no artifact ids, paths or checksum join).
func (p *MavenStore) ListMavenGARefs(ctx context.Context, repoID, groupID, artifactID string) (*models.MavenGARefs, error) {
	rows, err := p.q.ListMavenGARefs(ctx, dbsqlc.ListMavenGARefsParams{
		RepositoryID:    repoID,
		GroupID:         groupID,
		ArtifactIDCoord: artifactID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query maven refs by ga: %w", err)
	}

	refs := &models.MavenGARefs{}

	for _, row := range rows {
		if !row.SnapshotRow {
			refs.Versions = append(refs.Versions, row.Version)

			continue
		}

		refs.Snapshots = append(refs.Snapshots, models.MavenSnapshotRef{
			Version:     row.Version,
			Classifier:  row.Classifier,
			Extension:   row.Extension,
			Timestamp:   row.SnapshotTimestamp,
			BuildNumber: int(row.SnapshotBuildNumber),
		})
	}

	return refs, nil
}

func (p *MavenStore) GetLatestSnapshot(ctx context.Context, repoID, groupID, artifactID, version string) (*models.MavenArtifact, error) {
	row, err := p.q.GetLatestSnapshot(ctx, dbsqlc.GetLatestSnapshotParams{
		RepositoryID:    repoID,
		GroupID:         groupID,
		ArtifactIDCoord: artifactID,
		Version:         version,
	})

	return getMavenArtifact(ctx, p, row, err, "latest snapshot")
}

func (p *MavenStore) GetLatestSnapshotFile(
	ctx context.Context,
	repoID, groupID, artifactID, version, classifier, extension string,
) (*models.MavenArtifact, error) {
	row, err := p.q.GetLatestSnapshotFile(ctx, dbsqlc.GetLatestSnapshotFileParams{
		RepositoryID:    repoID,
		GroupID:         groupID,
		ArtifactIDCoord: artifactID,
		Version:         version,
		Classifier:      classifier,
		Extension:       extension,
	})

	return getMavenArtifact(ctx, p, row, err, "latest snapshot file")
}

func (p *MavenStore) DeleteMavenArtifact(ctx context.Context, id string) error {
	if err := p.q.DeleteMavenArtifact(ctx, id); err != nil {
		return fmt.Errorf("failed to delete maven artifact: %w", err)
	}

	return nil
}

// ListMavenArtifactsByRepo lists every maven artifact for a repository,
// with its blob's sha256/size resolved via a JOIN (unlike BlobID on the
// other Get* methods here, which is the raw, restore-incompatible blobs.id
// UUID). Used only by backup export.
func (p *MavenStore) ListMavenArtifactsByRepo(ctx context.Context, repoID string) ([]*models.MavenArtifact, error) {
	rows, err := p.q.ListMavenArtifactsByRepo(ctx, repoID)

	return mapRowsOrNil(rows, err, "list maven artifacts by repo", func(row dbsqlc.ListMavenArtifactsByRepoRow) *models.MavenArtifact {
		art := mavenArtifactFromRow(dbsqlc.GetMavenArtifactByPathRow{
			ID: row.ID, RepositoryID: row.RepositoryID, GroupID: row.GroupID, ArtifactIDCoord: row.ArtifactIDCoord,
			Version: row.Version, Classifier: row.Classifier, Extension: row.Extension,
			SnapshotTimestamp: row.SnapshotTimestamp, SnapshotBuildNumber: row.SnapshotBuildNumber,
			IsSnapshot: row.IsSnapshot, RepoPath: row.RepoPath, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
		art.BlobSHA256, art.BlobSizeBytes = row.BlobSha256, row.BlobSizeBytes

		return art
	})
}
