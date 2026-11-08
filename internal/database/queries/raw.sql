-- Raw on the generic package tables (see the mapping in pkg_raw.go). One
-- package, one version ('') and one file per path.

-- name: UpsertRawFile :one
-- The file of a path: a live row at the path keeps its id and created_at and
-- takes the new blob and attributes. A row of a package of another format is
-- never rewritten (no row comes back): its checksums and format metadata would
-- no longer describe the bytes.
INSERT INTO package_files (id, version_id, repository_id, path, filename, blob_id, role, listed, attrs, unit_key, created_at)
VALUES (sqlc.arg(id), sqlc.arg(version_id), sqlc.arg(repository_id), sqlc.arg(path), sqlc.arg(path), sqlc.arg(blob_id),
        'primary', true, sqlc.arg(attrs)::jsonb, sqlc.arg(path), sqlc.arg(created_at)::timestamptz)
ON CONFLICT (repository_id, path) WHERE deleted_at IS NULL DO UPDATE SET
    blob_id = EXCLUDED.blob_id,
    attrs = EXCLUDED.attrs,
    updated_at = now()
WHERE EXISTS (
    SELECT 1 FROM package_versions v JOIN packages p ON p.id = v.package_id
    WHERE v.id = package_files.version_id AND p.format = 'raw')
RETURNING id;

-- name: InsertRawFileIfAbsent :execrows
INSERT INTO package_files (id, version_id, repository_id, path, filename, blob_id, role, listed, attrs, unit_key, created_at)
VALUES (sqlc.arg(id), sqlc.arg(version_id), sqlc.arg(repository_id), sqlc.arg(path), sqlc.arg(path), sqlc.arg(blob_id),
        'primary', true, sqlc.arg(attrs)::jsonb, sqlc.arg(path), sqlc.arg(created_at)::timestamptz)
ON CONFLICT (repository_id, path) WHERE deleted_at IS NULL DO NOTHING;

-- name: GetRawArtifact :one
SELECT f.id, f.repository_id, f.path, f.blob_id, f.attrs,
       b.size_bytes, b.sha256, b.blob_key, f.created_at, f.updated_at
FROM package_files f
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.path = $2 AND f.deleted_at IS NULL;

-- name: GetRawFileID :one
SELECT id FROM package_files WHERE repository_id = $1 AND path = $2 AND deleted_at IS NULL;

-- name: ListRawArtifacts :many
SELECT f.id, f.repository_id, f.path, f.blob_id, f.attrs,
       b.size_bytes, b.sha256, b.blob_key, f.created_at, f.updated_at
FROM package_files f
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL ORDER BY f.path ASC;

-- name: ListRawArtifactsByPrefix :many
SELECT f.id, f.repository_id, f.path, f.blob_id, f.attrs,
       b.size_bytes, b.sha256, b.blob_key, f.created_at, f.updated_at
FROM package_files f
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.path LIKE $2 AND f.deleted_at IS NULL ORDER BY f.path ASC;
