-- Ansible collections on the generic package tables (mapping spec in pkg_ansible.go). One
-- published collection version is one package_versions row with one package_files row.

-- The ids among ids that are live files of the repository: the hard deletes of
-- the version-grain formats act on live rows only.
-- name: FilterLivePackageFileIDs :many
SELECT f.id FROM package_files f
WHERE f.repository_id = sqlc.arg(repository_id) AND f.id = ANY(sqlc.arg(ids)::uuid[]) AND f.deleted_at IS NULL;

-- name: GetAnsibleCollection :one
SELECT f.id, f.repository_id, COALESCE(p.attrs ->> 'namespace', '')::text AS namespace, COALESCE(p.attrs ->> 'name', '')::text AS name,
       v.version, f.filename, b.blob_key, b.size_bytes, b.sha256, v.metadata, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.version_key = sqlc.arg(version) AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(fqcn);

-- name: GetAnsibleCollectionByID :one
SELECT f.id, f.repository_id, COALESCE(p.attrs ->> 'namespace', '')::text AS namespace, COALESCE(p.attrs ->> 'name', '')::text AS name,
       v.version, f.filename, b.blob_key, b.size_bytes, b.sha256, v.metadata, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.id = sqlc.arg(id) AND f.repository_id = sqlc.arg(repository_id) AND f.deleted_at IS NULL;

-- name: ListAnsibleCollectionVersions :many
-- Every live version of one collection; the caller orders them by version.
SELECT f.id, f.repository_id, COALESCE(p.attrs ->> 'namespace', '')::text AS namespace, COALESCE(p.attrs ->> 'name', '')::text AS name,
       v.version, f.filename, b.blob_key, b.size_bytes, b.sha256, v.metadata, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(fqcn)
ORDER BY f.created_at, f.id;

-- name: ListAnsibleCollections :many
-- Every live version of every collection of the repository (backup export).
SELECT f.id, f.repository_id, COALESCE(p.attrs ->> 'namespace', '')::text AS namespace, COALESCE(p.attrs ->> 'name', '')::text AS name,
       v.version, f.filename, b.blob_key, b.size_bytes, b.sha256, v.metadata, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL
ORDER BY p.attrs ->> 'namespace', p.attrs ->> 'name', f.created_at, f.id;

-- name: ListAnsibleCollectionNames :many
-- One row per collection of the repository that has a live version, a page of
-- them in name order.
SELECT COALESCE(p.attrs ->> 'namespace', '')::text AS namespace, COALESCE(p.attrs ->> 'name', '')::text AS name,
       MIN(v.created_at)::timestamptz AS created_at, MAX(v.updated_at)::timestamptz AS updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
WHERE p.repository_id = $1
GROUP BY p.id
ORDER BY 1, 2
LIMIT $2 OFFSET $3;

-- name: CountAnsibleCollectionNames :one
SELECT COUNT(DISTINCT p.id) FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
WHERE p.repository_id = $1;
