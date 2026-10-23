-- name: CreatePresignedUpload :one
INSERT INTO presigned_uploads (repository_id, path, size_bytes, sha256, content_type, created_by, expires_at, part_size)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, created_at;

-- name: GetPresignedUpload :one
SELECT id, repository_id, path, size_bytes, sha256, content_type, created_by, created_at, expires_at,
       completing_since, completed_at, artifact_id, part_size, multipart_id
FROM presigned_uploads
WHERE id = $1 AND repository_id = $2;

-- name: ClaimPresignedUpload :execrows
-- Takes the complete lease. It fails when the upload is already completed or
-- another complete holds an unexpired lease.
UPDATE presigned_uploads
SET completing_since = now()
WHERE id = $1
  AND repository_id = $2
  AND completed_at IS NULL
  AND (completing_since IS NULL OR completing_since < now() - make_interval(secs => sqlc.arg(lease_seconds)::float8));

-- name: ReleasePresignedUpload :exec
UPDATE presigned_uploads SET completing_since = NULL WHERE id = $1 AND completed_at IS NULL;

-- name: MarkPresignedUploadCompleted :exec
UPDATE presigned_uploads
SET completed_at = now(), completing_since = NULL, artifact_id = $2
WHERE id = $1;

-- name: ExistingPresignedUploadIDs :many
SELECT id FROM presigned_uploads WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: DeletePresignedUpload :exec
DELETE FROM presigned_uploads WHERE id = $1;

-- name: ListExpiredPresignedUploads :many
SELECT id
FROM presigned_uploads
WHERE expires_at < now()
  -- never pull the staging object from under a complete that is verifying it
  -- (the longest complete lease is 6 hours + 1 minute)
  AND (completing_since IS NULL OR completing_since < now() - interval '7 hours')
ORDER BY expires_at
LIMIT $1;

-- name: SetPresignedUploadMultipartID :exec
UPDATE presigned_uploads SET multipart_id = $2 WHERE id = $1;

-- name: ExtendPresignedUploadExpiry :exec
-- An upload that is still receiving parts stays alive: the deadline only moves forward.
UPDATE presigned_uploads SET expires_at = GREATEST(expires_at, $2) WHERE id = $1 AND completed_at IS NULL;
