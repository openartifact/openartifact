-- name: UpsertRawArtifact :exec
INSERT INTO raw_artifacts (id, repository_id, path, content_type, blob_id, metadata, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (repository_id, path) WHERE deleted_at IS NULL DO UPDATE SET
    content_type = EXCLUDED.content_type,
    blob_id = EXCLUDED.blob_id,
    metadata = EXCLUDED.metadata,
    updated_at = NOW();

-- name: GetRawArtifact :one
SELECT ra.id, ra.repository_id, ra.path, ra.content_type, ra.blob_id,
       b.size_bytes, b.sha256, b.blob_key, ra.metadata, ra.created_at, ra.updated_at
FROM raw_artifacts ra
JOIN blobs b ON b.id = ra.blob_id
WHERE ra.repository_id = $1 AND ra.path = $2 AND ra.deleted_at IS NULL;

-- name: DeleteRawArtifact :exec
DELETE FROM raw_artifacts WHERE repository_id = $1 AND path = $2 AND deleted_at IS NULL;

-- name: ListRawArtifacts :many
SELECT ra.id, ra.repository_id, ra.path, ra.content_type, ra.blob_id,
       b.size_bytes, b.sha256, b.blob_key, ra.metadata, ra.created_at, ra.updated_at
FROM raw_artifacts ra
JOIN blobs b ON b.id = ra.blob_id
WHERE ra.repository_id = $1 AND ra.deleted_at IS NULL ORDER BY ra.path ASC;

-- name: ListRawArtifactsByPrefix :many
SELECT ra.id, ra.repository_id, ra.path, ra.content_type, ra.blob_id,
       b.size_bytes, b.sha256, b.blob_key, ra.metadata, ra.created_at, ra.updated_at
FROM raw_artifacts ra
JOIN blobs b ON b.id = ra.blob_id
WHERE ra.repository_id = $1 AND ra.path LIKE $2 AND ra.deleted_at IS NULL ORDER BY ra.path ASC;
