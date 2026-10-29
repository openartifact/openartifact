// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"strconv"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// MavenStore owns the Maven artifacts and the generated Maven metadata pruning.
//
// Maven on the generic package tables (the mapping spec is
// /tmp/oa-plug/schema/110_maven.sql, restated here as the maintainers'
// reference). One stored file (pom, jar, -sources.jar, one snapshot build's
// files, ...) is one package_files row; checksum sidecars and
// maven-metadata.xml are never stored.
//
//	P  packages         name = name_key = group_id ':' artifact_id
//	                    attrs = {"group_id", "artifact_id"}
//	V  package_versions version = version
//	                    version_key = version, plus '#' timestamp '-' build for a
//	                    timestamped snapshot build (every build is a version of
//	                    its own; all files of a build share it): the text the
//	                    cleanup worker ranks by
//	                    metadata = {"is_snapshot", "snapshot_timestamp" (absent
//	                    when none), "snapshot_build_number"}
//	F  package_files    path = repo_path (repository-wide live-unique), filename = repo_path,
//	                    role primary, listed, arch = classifier,
//	                    part = 'true:' version for a snapshot, else 'false',
//	                    checksums = {"md5", "sha1", "sha512"} (non-empty ones),
//	                    unit_key = repo_path (federation unit = the file)
//	side maven_files    classifier, extension, snapshot_timestamp, snapshot_build_number,
//	                    is_snapshot, md5, sha1, sha512 (the sidecar GETs read these;
//	                    written together with F.checksums in the upload transaction)
//
// The cleanup partitions maven on (P, F.part), not on F.arch: -sources and
// -javadoc files rank together with the main jar of their version.
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

// mavenPackageName is the package identity (name and name_key) of a
// group:artifact.
func mavenPackageName(groupID, artifactID string) string { return groupID + ":" + artifactID }

// mavenVersionKey is the identity of a version inside its package: the version
// itself, plus the snapshot build for a timestamped snapshot file.
func mavenVersionKey(version, snapshotTimestamp string, buildNumber int) string {
	if snapshotTimestamp == "" {
		return version
	}

	return version + "#" + snapshotTimestamp + "-" + strconv.Itoa(buildNumber)
}

// mavenFilePart is the cleanup partition component of a file.
func mavenFilePart(isSnapshot bool, version string) string {
	if isSnapshot {
		return "true:" + version
	}

	return "false"
}

// mavenDigests is F.checksums: the digests that are known.
func mavenDigests(a *models.MavenArtifact) map[string]string {
	sums := make(map[string]string, 3)

	for k, v := range map[string]string{"md5": a.MD5, "sha1": a.SHA1, "sha512": a.SHA512} {
		if v != "" {
			sums[k] = v
		}
	}

	return sums
}

// CreateMavenArtifact stores a file of an artifact (a.BlobID is a blobs id). A
// live file at the same repo path is repointed at the new blob and its digests
// are replaced (the snapshot overwrite); the digests go to maven_files and
// package_files.checksums in the same transaction.
func (p *MavenStore) CreateMavenArtifact(ctx context.Context, a *models.MavenArtifact) error {
	meta := map[string]any{"is_snapshot": a.IsSnapshot, "snapshot_build_number": a.SnapshotBuildNumber}
	if a.SnapshotTimestamp != "" {
		meta["snapshot_timestamp"] = a.SnapshotTimestamp
	}

	blobID := a.BlobID

	written, err := p.upsertPackageFile(ctx, &PackageWrite{
		RepoID:     a.RepositoryID,
		Format:     "maven",
		Name:       mavenPackageName(a.GroupID, a.ArtifactCoord),
		NameKey:    mavenPackageName(a.GroupID, a.ArtifactCoord),
		PkgAttrs:   map[string]any{"group_id": a.GroupID, "artifact_id": a.ArtifactCoord},
		Version:    a.Version,
		VersionKey: mavenVersionKey(a.Version, a.SnapshotTimestamp, a.SnapshotBuildNumber),
		// The version-level copy of the snapshot coordinates is rewritten
		// only for a new version: every file of it carries the same values.
		VersionMeta: meta,
		CreatedAt:   a.CreatedAt,
		Files: []FileWrite{{
			Path:      a.RepoPath,
			Filename:  a.RepoPath,
			Arch:      a.Classifier,
			Part:      mavenFilePart(a.IsSnapshot, a.Version),
			Checksums: mavenDigests(a),
			UnitKey:   a.RepoPath,
		}},
	}, blobID, func(ctx context.Context, q *dbsqlc.Queries, fileID string) error {
		return q.UpsertMavenFile(ctx, dbsqlc.UpsertMavenFileParams{
			ID:                  fileID,
			Classifier:          a.Classifier,
			Extension:           a.Extension,
			SnapshotTimestamp:   strPtr(a.SnapshotTimestamp),
			SnapshotBuildNumber: toInt32(a.SnapshotBuildNumber),
			IsSnapshot:          a.IsSnapshot,
			Md5:                 a.MD5,
			Sha1:                a.SHA1,
			Sha512:              a.SHA512,
		})
	})
	if err != nil {
		return fmt.Errorf("failed to create maven artifact: %w", err)
	}

	// ON CONFLICT returns the existing row id; always bind the returned id.
	a.ID = written.ID

	return nil
}

func (p *MavenStore) GetMavenArtifactByPath(ctx context.Context, repoID, path string) (*models.MavenArtifact, error) {
	row, err := p.q.GetMavenArtifactByPath(ctx, dbsqlc.GetMavenArtifactByPathParams{
		RepositoryID: repoID,
		RepoPath:     path,
	})

	return mapRow(row, err, "get maven artifact by path", mavenArtifactFromRow)
}

// mavenArtifactRow lists the sqlc row types mavenArtifactFromRow converts.
type mavenArtifactRow interface {
	dbsqlc.GetMavenArtifactByPathRow | dbsqlc.GetMavenArtifactsByGAVRow | dbsqlc.GetLatestSnapshotRow |
		dbsqlc.GetLatestSnapshotFileRow | dbsqlc.ListMavenArtifactsByRepoRow
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
		MD5:                 row.Md5,
		SHA1:                row.Sha1,
		SHA256:              row.Sha256,
		SHA512:              row.Sha512,
		BlobSizeBytes:       row.BlobSizeBytes,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}
}

func (p *MavenStore) GetMavenArtifactsByGAV(
	ctx context.Context,
	repoID, groupID, artifactID, version string,
) ([]*models.MavenArtifact, error) {
	rows, err := p.q.GetMavenArtifactsByGAV(ctx, dbsqlc.GetMavenArtifactsByGAVParams{
		RepositoryID: repoID,
		NameKey:      mavenPackageName(groupID, artifactID),
		Version:      version,
	})

	return mapRowsOrNil(rows, err, "query maven artifacts by gav", mavenArtifactFromRow)
}

// ListMavenGARefs loads the versions and snapshot files of one group:artifact
// in a single narrow query (no artifact ids, paths or checksum join).
func (p *MavenStore) ListMavenGARefs(ctx context.Context, repoID, groupID, artifactID string) (*models.MavenGARefs, error) {
	rows, err := p.q.ListMavenGARefs(ctx, dbsqlc.ListMavenGARefsParams{
		RepositoryID: repoID,
		NameKey:      mavenPackageName(groupID, artifactID),
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
		RepositoryID: repoID,
		NameKey:      mavenPackageName(groupID, artifactID),
		Version:      version,
	})

	return mapRow(row, err, "get latest snapshot", mavenArtifactFromRow)
}

// GetLatestSnapshotFile returns the newest snapshot build of the file with
// the given classifier and extension.
func (p *MavenStore) GetLatestSnapshotFile(
	ctx context.Context,
	repoID, groupID, artifactID, version, classifier, extension string,
) (*models.MavenArtifact, error) {
	row, err := p.q.GetLatestSnapshotFile(ctx, dbsqlc.GetLatestSnapshotFileParams{
		RepositoryID: repoID,
		NameKey:      mavenPackageName(groupID, artifactID),
		Version:      version,
		Classifier:   classifier,
		Extension:    extension,
	})

	return mapRow(row, err, "get latest snapshot file", mavenArtifactFromRow)
}

func (p *MavenStore) DeleteMavenArtifact(ctx context.Context, id string) error {
	if err := p.deleteLiveFile(ctx, id); err != nil {
		return fmt.Errorf("failed to delete maven artifact: %w", err)
	}

	return nil
}

// ListMavenArtifactsByRepo lists every live maven artifact of a repository.
// BlobSHA256/BlobSizeBytes carry the blob's digest and size (the restore
// handle: BlobID is a live blobs.id that does not survive a restore into a
// fresh DB).
func (p *MavenStore) ListMavenArtifactsByRepo(ctx context.Context, repoID string) ([]*models.MavenArtifact, error) {
	rows, err := p.q.ListMavenArtifactsByRepo(ctx, repoID)

	return mapRowsOrNil(rows, err, "list maven artifacts by repo", func(row dbsqlc.ListMavenArtifactsByRepoRow) *models.MavenArtifact {
		art := mavenArtifactFromRow(row)
		art.BlobSHA256 = row.Sha256

		return art
	})
}
