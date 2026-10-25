-- name: InsertStorageQuotaIfNotExists :exec
INSERT INTO storage_quotas (repository_id, max_size_bytes, max_artifact_count, warn_threshold_percent, current_size_bytes, current_artifact_count)
VALUES ($1, 0, 0, 80, 0, 0)
ON CONFLICT (repository_id) DO NOTHING;

-- name: GetStorageQuota :one
SELECT id, repository_id, max_size_bytes, max_artifact_count, warn_threshold_percent, current_size_bytes, current_artifact_count, created_at, updated_at
FROM storage_quotas WHERE repository_id = $1;

-- name: UpdateStorageQuota :exec
UPDATE storage_quotas
SET max_size_bytes = $1, max_artifact_count = $2, warn_threshold_percent = $3, updated_at = $4
WHERE repository_id = $5;
