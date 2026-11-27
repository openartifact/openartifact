-- Git LFS objects on the generic package tables (mapping spec in pkg_gitlfs.go). One
-- object is one package named by its oid, with one version and one file.

-- name: GetGitLFSObjects :many
SELECT f.id, f.repository_id, p.name AS oid, b.blob_key, b.size_bytes, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = ANY(sqlc.arg(oids)::text[]);
