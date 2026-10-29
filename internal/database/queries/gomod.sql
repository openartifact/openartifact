-- Go modules on the generic package tables (see the mapping in pkg_gomod.go): one
-- package per module path, one version per module version, two files per version
-- (the listed zip with role 'primary' and the unlisted go.mod with role 'mod').
-- The writes go through writePackage; these are the typed reads. Every read
-- starts at the zip file: a version without a live zip is not served.

-- name: GetGoModule :one
SELECT zf.id, zf.repository_id, p.name AS module_path, v.version,
       COALESCE(mf.attrs->>'go_mod_content', '')::text AS go_mod_content,
       COALESCE(zb.blob_key, '')::text AS zip_blob_key, COALESCE(mb.blob_key, '')::text AS mod_blob_key,
       COALESCE(zb.size_bytes, 0)::bigint AS zip_size_bytes, COALESCE(zb.sha256, '')::text AS zip_hash,
       v.created_at, zf.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.version_key = sqlc.arg(version) AND v.deleted_at IS NULL
JOIN package_files zf ON zf.version_id = v.id AND zf.role = 'primary' AND zf.deleted_at IS NULL
LEFT JOIN package_files mf ON mf.version_id = v.id AND mf.role = 'mod' AND mf.deleted_at IS NULL
LEFT JOIN blobs zb ON zb.id = zf.blob_id
LEFT JOIN blobs mb ON mb.id = mf.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(module_path);

-- name: ListGoModuleVersions :many
SELECT zf.id, zf.repository_id, p.name AS module_path, v.version,
       COALESCE(mf.attrs->>'go_mod_content', '')::text AS go_mod_content,
       COALESCE(zb.blob_key, '')::text AS zip_blob_key, COALESCE(mb.blob_key, '')::text AS mod_blob_key,
       COALESCE(zb.size_bytes, 0)::bigint AS zip_size_bytes, COALESCE(zb.sha256, '')::text AS zip_hash,
       v.created_at, zf.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files zf ON zf.version_id = v.id AND zf.role = 'primary' AND zf.deleted_at IS NULL
LEFT JOIN package_files mf ON mf.version_id = v.id AND mf.role = 'mod' AND mf.deleted_at IS NULL
LEFT JOIN blobs zb ON zb.id = zf.blob_id
LEFT JOIN blobs mb ON mb.id = mf.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(module_path)
ORDER BY v.created_at, zf.id;
