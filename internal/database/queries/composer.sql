-- composer on the generic package tables (the mapping is documented on
-- ComposerStore in pkg_composer.go): one package per vendor/package, one
-- version and one zip file per published version. The writes go through
-- writePackage and deleteFiles; these are the typed reads.

-- name: GetComposerPackage :one
SELECT f.id, f.repository_id, p.name, v.version, v.version_key AS version_normalized, b.blob_key, b.size_bytes, b.sha256,
       COALESCE(f.attrs->>'sha1', '')::text AS sha1,
       COALESCE(v.metadata->'composer_json', '{}'::jsonb)::jsonb AS composer_json, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.version_key = sqlc.arg(version_normalized) AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name);

-- name: GetComposerPackageByID :one
SELECT f.id, f.repository_id, p.name, v.version, v.version_key AS version_normalized, b.blob_key, b.size_bytes, b.sha256,
       COALESCE(f.attrs->>'sha1', '')::text AS sha1,
       COALESCE(v.metadata->'composer_json', '{}'::jsonb)::jsonb AS composer_json, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = sqlc.arg(repository_id) AND f.id = sqlc.arg(id) AND f.deleted_at IS NULL;

-- name: ListComposerPackageVersions :many
-- Every live version of one package, oldest upload first.
SELECT f.id, f.repository_id, p.name, v.version, v.version_key AS version_normalized, b.blob_key, b.size_bytes, b.sha256,
       COALESCE(f.attrs->>'sha1', '')::text AS sha1,
       COALESCE(v.metadata->'composer_json', '{}'::jsonb)::jsonb AS composer_json, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name)
ORDER BY v.created_at, v.id;

-- name: ListComposerPackageNames :many
-- One row per package of the repository in name order; the description is the
-- newest live version's composer.json description.
SELECT p.name,
       COALESCE((ARRAY_AGG(v.metadata->'composer_json'->>'description' ORDER BY v.created_at DESC, v.id DESC))[1], '')::text AS description,
       MAX(v.updated_at)::timestamptz AS updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
WHERE p.repository_id = $1
GROUP BY p.id, p.name
ORDER BY p.name;
