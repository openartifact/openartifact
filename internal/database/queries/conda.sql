-- name: CreateCondaPackage :one
INSERT INTO conda_packages (repository_id, name, version, build_string, platform, depends, blob_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, created_at, updated_at;

-- name: GetCondaPackage :one
SELECT cp.id, cp.repository_id, cp.name, cp.version, cp.build_string, cp.platform, cp.depends, b.blob_key, b.size_bytes, b.sha256, cp.created_at, cp.updated_at
FROM conda_packages cp
JOIN blobs b ON b.id = cp.blob_id
WHERE cp.repository_id = $1 AND cp.name = $2 AND cp.version = $3 AND cp.build_string = $4 AND cp.platform = $5 AND cp.deleted_at IS NULL;

-- name: DeleteCondaPackage :exec
DELETE FROM conda_packages WHERE id = $1 AND deleted_at IS NULL;

-- name: ListCondaPackages :many
SELECT cp.id, cp.repository_id, cp.name, cp.version, cp.build_string, cp.platform, cp.depends, b.blob_key, b.size_bytes, b.sha256, cp.created_at, cp.updated_at
FROM conda_packages cp
JOIN blobs b ON b.id = cp.blob_id
WHERE cp.repository_id = $1 AND cp.platform = $2 AND cp.deleted_at IS NULL ORDER BY cp.name ASC, cp.version DESC;

-- name: ListAllCondaPackages :many
SELECT cp.id, cp.repository_id, cp.name, cp.version, cp.build_string, cp.platform, cp.depends, b.blob_key, b.size_bytes, b.sha256, cp.created_at, cp.updated_at
FROM conda_packages cp
JOIN blobs b ON b.id = cp.blob_id
WHERE cp.repository_id = $1 AND cp.deleted_at IS NULL ORDER BY cp.name ASC, cp.version DESC, cp.platform ASC;
