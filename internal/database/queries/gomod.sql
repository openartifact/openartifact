-- name: CreateGoModule :one
INSERT INTO gomod_modules (repository_id, module_path, version, go_mod_content, zip_blob_id, mod_blob_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, created_at, updated_at;

-- name: GetGoModule :one
SELECT gm.id, gm.repository_id, gm.module_path, gm.version, gm.go_mod_content,
       COALESCE(zb.blob_key, '') AS zip_blob_key, COALESCE(mb.blob_key, '') AS mod_blob_key,
       COALESCE(zb.size_bytes, 0) AS zip_size_bytes, COALESCE(zb.sha256, '') AS zip_hash, gm.created_at, gm.updated_at
FROM gomod_modules gm
LEFT JOIN blobs zb ON zb.id = gm.zip_blob_id
LEFT JOIN blobs mb ON mb.id = gm.mod_blob_id
WHERE gm.repository_id = $1 AND gm.deleted_at IS NULL AND gm.module_path = $2 AND gm.version = $3;

-- name: DeleteGoModule :exec
DELETE FROM gomod_modules WHERE repository_id = $1 AND module_path = $2 AND version = $3 AND deleted_at IS NULL;

-- name: ListGoModuleVersions :many
SELECT gm.id, gm.repository_id, gm.module_path, gm.version, gm.go_mod_content,
       COALESCE(zb.blob_key, '') AS zip_blob_key, COALESCE(mb.blob_key, '') AS mod_blob_key,
       COALESCE(zb.size_bytes, 0) AS zip_size_bytes, COALESCE(zb.sha256, '') AS zip_hash, gm.created_at, gm.updated_at
FROM gomod_modules gm
LEFT JOIN blobs zb ON zb.id = gm.zip_blob_id
LEFT JOIN blobs mb ON mb.id = gm.mod_blob_id
WHERE gm.repository_id = $1 AND gm.deleted_at IS NULL AND gm.module_path = $2
ORDER BY gm.version DESC;

-- name: GetLatestGoModule :one
SELECT gm.id, gm.repository_id, gm.module_path, gm.version, gm.go_mod_content,
       COALESCE(zb.blob_key, '') AS zip_blob_key, COALESCE(mb.blob_key, '') AS mod_blob_key,
       COALESCE(zb.size_bytes, 0) AS zip_size_bytes, COALESCE(zb.sha256, '') AS zip_hash, gm.created_at, gm.updated_at
FROM gomod_modules gm
LEFT JOIN blobs zb ON zb.id = gm.zip_blob_id
LEFT JOIN blobs mb ON mb.id = gm.mod_blob_id
WHERE gm.repository_id = $1 AND gm.deleted_at IS NULL AND gm.module_path = $2
ORDER BY gm.version DESC
LIMIT 1;

-- name: ListGoModulesByRepo :many
SELECT gm.id, gm.repository_id, gm.module_path, gm.version, gm.go_mod_content,
       COALESCE(zb.blob_key, '') AS zip_blob_key, COALESCE(mb.blob_key, '') AS mod_blob_key,
       COALESCE(zb.size_bytes, 0) AS zip_size_bytes, COALESCE(zb.sha256, '') AS zip_hash, gm.created_at, gm.updated_at
FROM gomod_modules gm
LEFT JOIN blobs zb ON zb.id = gm.zip_blob_id
LEFT JOIN blobs mb ON mb.id = gm.mod_blob_id
WHERE gm.repository_id = $1 AND gm.deleted_at IS NULL
ORDER BY gm.module_path, gm.version DESC;
