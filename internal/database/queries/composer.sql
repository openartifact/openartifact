-- name: CreateComposerPackage :one
INSERT INTO composer_packages (
    repository_id, name, version, version_normalized, blob_id, sha1, composer_json, created_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, COALESCE(sqlc.narg(created_at)::timestamptz, NOW()))
RETURNING id, created_at, updated_at;

-- name: GetComposerPackage :one
SELECT cp.id, cp.repository_id, cp.name, cp.version, cp.version_normalized, b.blob_key, b.size_bytes, b.sha256,
       cp.sha1, cp.composer_json, cp.created_at, cp.updated_at
FROM composer_packages cp
JOIN blobs b ON b.id = cp.blob_id
WHERE cp.repository_id = $1 AND cp.name = $2 AND cp.version_normalized = $3 AND cp.deleted_at IS NULL;

-- name: GetComposerPackageByID :one
SELECT cp.id, cp.repository_id, cp.name, cp.version, cp.version_normalized, b.blob_key, b.size_bytes, b.sha256,
       cp.sha1, cp.composer_json, cp.created_at, cp.updated_at
FROM composer_packages cp
JOIN blobs b ON b.id = cp.blob_id
WHERE cp.repository_id = $1 AND cp.id = $2 AND cp.deleted_at IS NULL;

-- name: ListComposerPackageVersions :many
-- Every live version of one package, oldest upload first.
SELECT cp.id, cp.repository_id, cp.name, cp.version, cp.version_normalized, b.blob_key, b.size_bytes, b.sha256,
       cp.sha1, cp.composer_json, cp.created_at, cp.updated_at
FROM composer_packages cp
JOIN blobs b ON b.id = cp.blob_id
WHERE cp.repository_id = $1 AND cp.name = $2 AND cp.deleted_at IS NULL
ORDER BY cp.created_at, cp.id;

-- name: ListComposerPackages :many
-- Every live version of every package of the repository (backup export).
SELECT cp.id, cp.repository_id, cp.name, cp.version, cp.version_normalized, b.blob_key, b.size_bytes, b.sha256,
       cp.sha1, cp.composer_json, cp.created_at, cp.updated_at
FROM composer_packages cp
JOIN blobs b ON b.id = cp.blob_id
WHERE cp.repository_id = $1 AND cp.deleted_at IS NULL
ORDER BY cp.name, cp.created_at, cp.id;

-- name: ListComposerPackageNames :many
-- One row per package of the repository in name order; the description is the
-- newest live version's composer.json description.
SELECT DISTINCT ON (cp.name) cp.name,
       COALESCE(cp.composer_json->>'description', '')::text AS description,
       MAX(cp.updated_at) OVER (PARTITION BY cp.name)::timestamptz AS updated_at
FROM composer_packages cp
WHERE cp.repository_id = $1 AND cp.deleted_at IS NULL
ORDER BY cp.name, cp.created_at DESC, cp.id DESC;

-- name: DeleteComposerPackage :exec
DELETE FROM composer_packages WHERE id = $1 AND repository_id = $2 AND deleted_at IS NULL;
