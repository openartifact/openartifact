-- Swift Package Registry releases on the generic package tables (mapping spec in
-- pkg_swift.go). One published release is one package_versions row with one
-- package_files row (the source archive). FilterLivePackageFileIDs is in ansible.sql.

-- name: GetSwiftRelease :one
SELECT f.id, f.repository_id, COALESCE(p.attrs ->> 'scope', '')::text AS scope, COALESCE(p.attrs ->> 'name', '')::text AS name,
       v.version, f.filename, b.blob_key, b.size_bytes, b.sha256, v.metadata, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.version_key = sqlc.arg(version) AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key);

-- name: ListSwiftReleaseVersions :many
-- The live releases of one package, without the manifests; the caller orders
-- them by version.
SELECT v.version, f.created_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key)
ORDER BY f.created_at, f.id;

-- name: ListSwiftIdentifiers :many
-- The packages with a live release that declares the normalized repository URL.
SELECT DISTINCT p.name::text AS name
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
WHERE p.repository_id = sqlc.arg(repository_id)
  AND v.metadata -> 'repository_urls' @> jsonb_build_array(sqlc.arg(url)::text)
ORDER BY 1
LIMIT 100;
