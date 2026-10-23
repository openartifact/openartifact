-- name: CreateSBTArtifact :one
INSERT INTO sbt_artifacts (repository_id, group_id, artifact_id, version, filename, blob_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, COALESCE(sqlc.narg(created_at)::timestamptz, NOW()))
ON CONFLICT (repository_id, group_id, artifact_id, version, filename) WHERE deleted_at IS NULL
DO UPDATE SET blob_id = EXCLUDED.blob_id, updated_at = now()
RETURNING id, created_at, updated_at;

-- name: GetSBTArtifact :one
SELECT sa.id, sa.repository_id, sa.group_id, sa.artifact_id, sa.version, sa.filename, b.blob_key, b.size_bytes, b.sha256, sa.created_at, sa.updated_at
FROM sbt_artifacts sa
JOIN blobs b ON b.id = sa.blob_id
WHERE sa.repository_id = $1 AND sa.group_id = $2 AND sa.artifact_id = $3 AND sa.version = $4 AND sa.filename = $5 AND sa.deleted_at IS NULL;

-- name: DeleteSBTArtifact :exec
DELETE FROM sbt_artifacts WHERE id = $1 AND deleted_at IS NULL;

-- name: ListSBTArtifacts :many
SELECT sa.id, sa.repository_id, sa.group_id, sa.artifact_id, sa.version, sa.filename, b.blob_key, b.size_bytes, b.sha256, sa.created_at, sa.updated_at
FROM sbt_artifacts sa
JOIN blobs b ON b.id = sa.blob_id
WHERE sa.repository_id = $1 AND sa.deleted_at IS NULL ORDER BY sa.group_id, sa.artifact_id, sa.version;
