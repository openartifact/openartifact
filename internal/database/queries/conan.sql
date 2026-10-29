-- conan on the generic package tables; the mapping is documented on ConanStore
-- (internal/database/pkg_conan.go). A conan "file" is one package_files row, its
-- coordinates are P.name, V.version, V.metadata (user_channel, recipe_revision)
-- and the conan_files side row.

-- name: UpsertConanFile :one
-- Upserts one stored file: re-uploading a file replaces its blob and digests (a
-- Conan client does this for metadata and when a failed upload is retried). The
-- path encodes every coordinate of the file, so a conflict is the same file.
INSERT INTO package_files (version_id, repository_id, path, filename, blob_id, role, listed, attrs, checksums, created_at)
VALUES (sqlc.arg(version_id), sqlc.arg(repository_id), sqlc.arg(path), sqlc.arg(filename), sqlc.narg(blob_id),
        'primary', sqlc.arg(listed), sqlc.arg(attrs)::jsonb, sqlc.arg(checksums)::jsonb,
        COALESCE(sqlc.narg(created_at)::timestamptz, now()))
ON CONFLICT (repository_id, path) WHERE deleted_at IS NULL DO UPDATE SET
    blob_id = EXCLUDED.blob_id,
    checksums = EXCLUDED.checksums,
    updated_at = now()
RETURNING id, created_at, updated_at;

-- name: UpsertConanFileCoords :exec
INSERT INTO conan_files (id, package_id, package_revision)
VALUES ($1, $2, $3)
ON CONFLICT (id) DO NOTHING;

-- name: GetConanPackage :one
SELECT f.id, f.repository_id, p.name, v.version,
       COALESCE(v.metadata ->> 'user_channel', '')::text AS user_channel,
       COALESCE(v.metadata ->> 'recipe_revision', '')::text AS recipe_revision,
       cf.package_id, cf.package_revision,
       COALESCE(f.attrs ->> 'filename', '')::text AS filename,
       COALESCE(b.blob_key, '') AS blob_key, COALESCE(b.sha256, '') AS sha256, COALESCE(b.size_bytes, 0)::bigint AS size_bytes,
       COALESCE(f.checksums ->> 'md5', '')::text AS md5, COALESCE(f.checksums ->> 'sha1', '')::text AS sha1,
       f.created_at, f.updated_at
FROM package_files f
JOIN conan_files cf ON cf.id = f.id
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
LEFT JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.path = $2 AND f.deleted_at IS NULL;

-- name: ListConanRecipeFiles :many
-- Every live file of one recipe: all its recipe revisions and package revisions.
-- A live file never sits in a trashed version; the v.deleted_at test is what lets
-- the planner use idx_package_versions_pkg (a partial index).
SELECT f.id, f.repository_id, p.name, v.version,
       COALESCE(v.metadata ->> 'user_channel', '')::text AS user_channel,
       COALESCE(v.metadata ->> 'recipe_revision', '')::text AS recipe_revision,
       cf.package_id, cf.package_revision,
       COALESCE(f.attrs ->> 'filename', '')::text AS filename,
       COALESCE(b.blob_key, '') AS blob_key, COALESCE(b.sha256, '') AS sha256, COALESCE(b.size_bytes, 0)::bigint AS size_bytes,
       COALESCE(f.checksums ->> 'md5', '')::text AS md5, COALESCE(f.checksums ->> 'sha1', '')::text AS sha1,
       f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id
JOIN package_files f ON f.version_id = v.id
JOIN conan_files cf ON cf.id = f.id
LEFT JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = $1 AND p.name_key = $2 AND v.version = $3 AND v.metadata ->> 'user_channel' = sqlc.arg(user_channel)::text
  AND v.deleted_at IS NULL AND f.deleted_at IS NULL
ORDER BY f.created_at, f.id;

-- name: ListConanRecipes :many
SELECT DISTINCT p.name, v.version, COALESCE(v.metadata ->> 'user_channel', '')::text AS user_channel
FROM packages p
JOIN package_versions v ON v.package_id = p.id
JOIN package_files f ON f.version_id = v.id
WHERE p.repository_id = $1 AND f.deleted_at IS NULL AND COALESCE(f.attrs ->> 'filename', '') <> ''
ORDER BY p.name, v.version, user_channel;

-- name: DeleteConanFile :one
-- Hard delete of one live file (the quota trigger releases its usage, conan_files
-- goes with it); the version it leaves empty is pruned by the caller.
DELETE FROM package_files WHERE id = $1 AND deleted_at IS NULL RETURNING version_id;

-- name: FindConanPackageBySHA1 :one
-- Checksum deploy: a live file of the repository whose content has this SHA-1.
-- Scoped to the repository, so a client can only ever reuse content it could
-- already address there. The `?` test is the predicate of idx_package_files_sha1.
SELECT f.id, f.repository_id, p.name, v.version,
       COALESCE(v.metadata ->> 'user_channel', '')::text AS user_channel,
       COALESCE(v.metadata ->> 'recipe_revision', '')::text AS recipe_revision,
       cf.package_id, cf.package_revision,
       COALESCE(f.attrs ->> 'filename', '')::text AS filename,
       COALESCE(b.blob_key, '') AS blob_key, COALESCE(b.sha256, '') AS sha256, COALESCE(b.size_bytes, 0)::bigint AS size_bytes,
       COALESCE(f.checksums ->> 'md5', '')::text AS md5, COALESCE(f.checksums ->> 'sha1', '')::text AS sha1,
       f.created_at, f.updated_at
FROM package_files f
JOIN blobs b ON b.id = f.blob_id
JOIN conan_files cf ON cf.id = f.id
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
WHERE (f.checksums ->> 'sha1') = sqlc.arg(sha1)::text AND f.checksums ? 'sha1' AND f.deleted_at IS NULL
  AND f.repository_id = sqlc.arg(repository_id) AND sqlc.arg(sha1)::text <> ''
ORDER BY f.created_at, f.id
LIMIT 1;

-- name: SetConanPackageDigests :exec
-- Records the digests computed for a file stored before they were tracked; an
-- empty digest is absent from checksums.
UPDATE package_files
SET checksums = (checksums - 'md5' - 'sha1')
    || jsonb_strip_nulls(jsonb_build_object('md5', NULLIF(sqlc.arg(md5)::text, ''), 'sha1', NULLIF(sqlc.arg(sha1)::text, '')))
WHERE id = sqlc.arg(id) AND deleted_at IS NULL;
