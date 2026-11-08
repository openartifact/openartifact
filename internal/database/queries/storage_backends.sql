-- name: CreateStorageBackend :exec
INSERT INTO storage_backends (id, name, kind, config, status, status_message, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListStorageBackends :many
SELECT id, name, kind, config, status, status_message, last_checked_at, created_at, updated_at
FROM storage_backends ORDER BY name ASC;

-- name: GetStorageBackend :one
SELECT id, name, kind, config, status, status_message, last_checked_at, created_at, updated_at
FROM storage_backends WHERE name = $1;

-- name: DeleteStorageBackend :exec
DELETE FROM storage_backends WHERE id = $1;

-- name: UpdateStorageBackendStatus :exec
UPDATE storage_backends SET status = $1, status_message = $2, last_checked_at = now() WHERE id = $3;
