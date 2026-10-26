-- name: CreateStorageBackend :exec
INSERT INTO storage_backends (id, name, kind, is_default, config, status, status_message, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: ListStorageBackends :many
SELECT id, name, kind, is_default, config, status, status_message, last_checked_at, created_at, updated_at
FROM storage_backends ORDER BY is_default DESC, name ASC;

-- name: GetStorageBackend :one
SELECT id, name, kind, is_default, config, status, status_message, last_checked_at, created_at, updated_at
FROM storage_backends WHERE name = $1;

-- name: UpdateStorageBackend :exec
UPDATE storage_backends SET name = $1, kind = $2, is_default = $3, config = $4, updated_at = $5 WHERE id = $6;

-- name: DeleteStorageBackend :exec
DELETE FROM storage_backends WHERE id = $1;

-- name: UpdateStorageBackendStatus :exec
UPDATE storage_backends SET status = $1, status_message = $2, last_checked_at = now() WHERE id = $3;
