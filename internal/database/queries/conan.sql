-- name: CreateConanPackage :one
-- Upserts one stored file: re-uploading a file replaces its blob (a Conan client
-- does this for metadata and when a failed upload is retried).
INSERT INTO conan_packages (repository_id, name, version, user_channel, recipe_revision, package_id, package_revision, filename, blob_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (repository_id, name, version, user_channel, recipe_revision, package_id, package_revision, filename) WHERE deleted_at IS NULL DO UPDATE SET
    blob_id = EXCLUDED.blob_id,
    updated_at = now()
RETURNING id, created_at, updated_at;

-- name: GetConanPackage :one
SELECT cp.id, cp.repository_id, cp.name, cp.version, cp.user_channel, cp.recipe_revision, cp.package_id, cp.package_revision, cp.filename,
       COALESCE(b.blob_key, '') AS blob_key, COALESCE(b.sha256, '') AS sha256, COALESCE(b.size_bytes, 0)::bigint AS size_bytes,
       cp.created_at, cp.updated_at
FROM conan_packages cp
LEFT JOIN blobs b ON b.id = cp.blob_id
WHERE cp.repository_id = $1 AND cp.name = $2 AND cp.version = $3 AND cp.user_channel = $4 AND cp.recipe_revision = $5
  AND cp.package_id = $6 AND cp.package_revision = $7 AND cp.filename = $8 AND cp.deleted_at IS NULL;

-- name: ListConanRecipeFiles :many
-- Every live file of one recipe: all its recipe revisions and package revisions.
SELECT cp.id, cp.repository_id, cp.name, cp.version, cp.user_channel, cp.recipe_revision, cp.package_id, cp.package_revision, cp.filename,
       COALESCE(b.blob_key, '') AS blob_key, COALESCE(b.sha256, '') AS sha256, COALESCE(b.size_bytes, 0)::bigint AS size_bytes,
       cp.created_at, cp.updated_at
FROM conan_packages cp
LEFT JOIN blobs b ON b.id = cp.blob_id
WHERE cp.repository_id = $1 AND cp.name = $2 AND cp.version = $3 AND cp.user_channel = $4 AND cp.deleted_at IS NULL
ORDER BY cp.created_at, cp.id;

-- name: ListConanRecipes :many
SELECT DISTINCT cp.name, cp.version, cp.user_channel
FROM conan_packages cp
WHERE cp.repository_id = $1 AND cp.deleted_at IS NULL AND cp.filename <> ''
ORDER BY cp.name, cp.version, cp.user_channel;

-- name: DeleteConanPackage :exec
DELETE FROM conan_packages WHERE id = $1 AND deleted_at IS NULL;

-- name: ListConanPackages :many
SELECT cp.id, cp.repository_id, cp.name, cp.version, cp.user_channel, cp.recipe_revision, cp.package_id, cp.package_revision, cp.filename,
       COALESCE(b.blob_key, '') AS blob_key, COALESCE(b.sha256, '') AS sha256, COALESCE(b.size_bytes, 0)::bigint AS size_bytes,
       cp.created_at, cp.updated_at
FROM conan_packages cp
LEFT JOIN blobs b ON b.id = cp.blob_id
WHERE cp.repository_id = $1 AND cp.deleted_at IS NULL ORDER BY cp.name ASC, cp.version DESC, cp.recipe_revision, cp.package_id, cp.package_revision, cp.filename;
