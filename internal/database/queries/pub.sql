-- Dart and Flutter pub packages on the generic package tables (mapping spec in pkg_pub.go). One
-- published package version is one package_versions row with one package_files row.

-- name: GetPubPackage :one
SELECT f.id, f.repository_id, p.name, v.version, f.filename, b.blob_key, b.size_bytes, b.sha256, v.metadata, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.version_key = sqlc.arg(version) AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name);

-- name: ListPubVersions :many
-- Every live version of one package; the caller orders them by version.
SELECT f.id, f.repository_id, p.name, v.version, f.filename, b.blob_key, b.size_bytes, b.sha256, v.metadata, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name)
ORDER BY f.created_at, f.id;
