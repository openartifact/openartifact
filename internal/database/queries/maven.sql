-- name: CreateMavenArtifact :one
INSERT INTO maven_artifacts (repository_id, blob_id, group_id, artifact_id_coord, version, classifier, extension, snapshot_timestamp, snapshot_build_number, is_snapshot, repo_path, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, COALESCE(sqlc.narg(created_at)::timestamptz, now()))
ON CONFLICT (repository_id, repo_path) WHERE deleted_at IS NULL DO UPDATE SET
    blob_id = EXCLUDED.blob_id,
    updated_at = now()
RETURNING id;

-- name: GetMavenArtifactByPath :one
SELECT id, repository_id,
       blob_id,
       group_id, artifact_id_coord, version, classifier, extension,
       snapshot_timestamp, snapshot_build_number, is_snapshot, repo_path,
       created_at, updated_at
FROM maven_artifacts
WHERE repository_id = $1 AND repo_path = $2 AND deleted_at IS NULL;

-- name: GetMavenArtifactsByGAV :many
SELECT id, repository_id,
       blob_id,
       group_id, artifact_id_coord, version, classifier, extension,
       snapshot_timestamp, snapshot_build_number, is_snapshot, repo_path,
       created_at, updated_at
FROM maven_artifacts
WHERE repository_id = $1 AND group_id = $2 AND artifact_id_coord = $3 AND version = $4 AND deleted_at IS NULL;

-- name: ListMavenGARefs :many
-- Everything maven-metadata.xml regeneration needs for one group:artifact,
-- without the per-row ids, paths and checksum join of the full artifact load:
-- one row per distinct live version (snapshot_row = false) plus one row per
-- file of every -SNAPSHOT version (snapshot_row = true).
SELECT v.version::text AS version,
       ''::text AS classifier,
       ''::text AS extension,
       ''::text AS snapshot_timestamp,
       0::int AS snapshot_build_number,
       false AS snapshot_row
FROM (
    SELECT DISTINCT d.version
    FROM maven_artifacts d
    WHERE d.repository_id = $1 AND d.group_id = $2 AND d.artifact_id_coord = $3 AND d.deleted_at IS NULL
) v
UNION ALL
SELECT s.version::text, s.classifier::text, s.extension::text,
       COALESCE(s.snapshot_timestamp, '')::text, s.snapshot_build_number::int, true
FROM maven_artifacts s
WHERE s.repository_id = $1 AND s.group_id = $2 AND s.artifact_id_coord = $3 AND s.deleted_at IS NULL
  AND s.version LIKE '%-SNAPSHOT'
ORDER BY snapshot_row, version, classifier, extension, snapshot_timestamp, snapshot_build_number;

-- name: GetLatestSnapshot :one
SELECT id, repository_id,
       blob_id,
       group_id, artifact_id_coord, version, classifier, extension,
       snapshot_timestamp, snapshot_build_number, is_snapshot, repo_path,
       created_at, updated_at
FROM maven_artifacts
WHERE repository_id = $1 AND group_id = $2 AND artifact_id_coord = $3 AND version = $4 AND is_snapshot = true AND deleted_at IS NULL
ORDER BY snapshot_timestamp DESC, snapshot_build_number DESC
LIMIT 1;

-- name: GetLatestSnapshotFile :one
-- The newest timestamped snapshot build of one file (classifier + extension),
-- for the legacy "foo-1.0-SNAPSHOT.<ext>" lookup.
SELECT id, repository_id,
       blob_id,
       group_id, artifact_id_coord, version, classifier, extension,
       snapshot_timestamp, snapshot_build_number, is_snapshot, repo_path,
       created_at, updated_at
FROM maven_artifacts
WHERE repository_id = $1 AND group_id = $2 AND artifact_id_coord = $3 AND version = $4
  AND classifier = $5 AND extension = $6
  AND is_snapshot = true AND deleted_at IS NULL
ORDER BY snapshot_timestamp DESC NULLS LAST, snapshot_build_number DESC
LIMIT 1;

-- name: DeleteMavenArtifact :exec
DELETE FROM maven_artifacts WHERE id = $1 AND deleted_at IS NULL;

-- name: ListMavenArtifactsByRepo :many
SELECT ma.id, ma.repository_id, b.sha256 AS blob_sha256, b.size_bytes AS blob_size_bytes,
       ma.group_id, ma.artifact_id_coord, ma.version, ma.classifier, ma.extension,
       ma.snapshot_timestamp, ma.snapshot_build_number, ma.is_snapshot, ma.repo_path,
       ma.created_at, ma.updated_at
FROM maven_artifacts ma
JOIN blobs b ON b.id = ma.blob_id
WHERE ma.repository_id = $1 AND ma.deleted_at IS NULL;
