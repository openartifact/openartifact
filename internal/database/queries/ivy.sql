-- name: CreateIvyArtifact :one
INSERT INTO ivy_artifacts (repository_id, organisation, module, revision, artifact_name, artifact_type, artifact_ext, blob_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, COALESCE(sqlc.narg(created_at)::timestamptz, NOW()))
ON CONFLICT (repository_id, organisation, module, revision, artifact_name, artifact_ext) WHERE deleted_at IS NULL
DO UPDATE SET artifact_type = EXCLUDED.artifact_type, blob_id = EXCLUDED.blob_id, updated_at = now()
RETURNING id, created_at, updated_at;

-- name: GetIvyArtifact :one
SELECT ia.id, ia.repository_id, ia.organisation, ia.module, ia.revision, ia.artifact_name, ia.artifact_type, ia.artifact_ext, b.blob_key, b.size_bytes, b.sha256, ia.created_at, ia.updated_at
FROM ivy_artifacts ia
JOIN blobs b ON b.id = ia.blob_id
WHERE ia.repository_id = $1 AND ia.organisation = $2 AND ia.module = $3 AND ia.revision = $4 AND ia.artifact_name = $5 AND ia.artifact_ext = $6 AND ia.deleted_at IS NULL;

-- name: DeleteIvyArtifact :exec
DELETE FROM ivy_artifacts WHERE id = $1 AND deleted_at IS NULL;

-- name: ListIvyArtifacts :many
SELECT ia.id, ia.repository_id, ia.organisation, ia.module, ia.revision, ia.artifact_name, ia.artifact_type, ia.artifact_ext, b.blob_key, b.size_bytes, b.sha256, ia.created_at, ia.updated_at
FROM ivy_artifacts ia
JOIN blobs b ON b.id = ia.blob_id
WHERE ia.repository_id = $1 AND ia.deleted_at IS NULL ORDER BY ia.organisation, ia.module, ia.revision;
