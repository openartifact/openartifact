-- name: CreateConanPackage :one
INSERT INTO conan_packages (repository_id, name, version, user_channel, recipe_revision, blob_id, conanfile, conaninfo)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, created_at, updated_at;

-- name: GetConanPackage :one
SELECT cp.id, cp.repository_id, cp.name, cp.version, cp.user_channel, cp.recipe_revision, COALESCE(b.blob_key, '') AS blob_key, cp.conanfile, cp.conaninfo, cp.created_at, cp.updated_at
FROM conan_packages cp
LEFT JOIN blobs b ON b.id = cp.blob_id
WHERE cp.repository_id = $1 AND cp.name = $2 AND cp.version = $3 AND cp.user_channel = $4 AND cp.recipe_revision = $5 AND cp.deleted_at IS NULL;

-- name: DeleteConanPackage :exec
DELETE FROM conan_packages WHERE id = $1 AND deleted_at IS NULL;

-- name: ListConanPackages :many
SELECT cp.id, cp.repository_id, cp.name, cp.version, cp.user_channel, cp.recipe_revision, COALESCE(b.blob_key, '') AS blob_key, cp.conanfile, cp.conaninfo, cp.created_at, cp.updated_at
FROM conan_packages cp
LEFT JOIN blobs b ON b.id = cp.blob_id
WHERE cp.repository_id = $1 AND cp.deleted_at IS NULL ORDER BY cp.name ASC, cp.version DESC;
