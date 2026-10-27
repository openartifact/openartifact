-- storage_backends.is_default is no longer read or written: the primary store
-- comes from the server configuration, and the column is dropped by a later
-- contract migration once no 0.12 pod runs.

-- name: CreateStorageBackend :exec
INSERT INTO storage_backends (id, name, kind, config, status, status_message, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListStorageBackends :many
SELECT id, name, kind, config, status, status_message, last_checked_at, created_at, updated_at
FROM storage_backends ORDER BY name ASC;

-- name: GetStorageBackend :one
SELECT id, name, kind, config, status, status_message, last_checked_at, created_at, updated_at
FROM storage_backends WHERE name = $1;

-- name: UpdateStorageBackend :exec
UPDATE storage_backends SET name = $1, kind = $2, config = $3, updated_at = $4 WHERE id = $5;

-- name: DeleteStorageBackend :exec
DELETE FROM storage_backends WHERE id = $1;

-- name: UpdateStorageBackendStatus :exec
UPDATE storage_backends SET status = $1, status_message = $2, last_checked_at = now() WHERE id = $3;
