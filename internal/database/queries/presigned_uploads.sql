-- name: CreatePresignedUpload :one
INSERT INTO presigned_uploads (repository_id, path, size_bytes, sha256, content_type, created_by, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, created_at;

-- name: GetPresignedUpload :one
SELECT id, repository_id, path, size_bytes, sha256, content_type, created_by, created_at, expires_at
FROM presigned_uploads
WHERE id = $1 AND repository_id = $2;

-- name: DeletePresignedUpload :exec
DELETE FROM presigned_uploads WHERE id = $1;

-- name: ListExpiredPresignedUploads :many
SELECT id
FROM presigned_uploads
WHERE expires_at < now()
ORDER BY expires_at
LIMIT $1;
