-- Terraform modules and providers on the generic package tables (mapping spec in
-- pkg_terraform.go). Both share the packages of a repository and are told apart by
-- packages.attrs->>'kind'.

-- name: GetTerraformModule :one
SELECT f.id, f.repository_id, COALESCE(p.attrs ->> 'name', '')::text AS name, COALESCE(p.attrs ->> 'system', '')::text AS system,
       v.version, COALESCE(f.attrs ->> 'archive_ext', '')::text AS archive_ext, b.blob_key, b.sha256 AS checksum, b.size_bytes,
       f.download_count, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.version_key = sqlc.arg(version) AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key);

-- name: ListTerraformModuleVersions :many
SELECT f.id, f.repository_id, COALESCE(p.attrs ->> 'name', '')::text AS name, COALESCE(p.attrs ->> 'system', '')::text AS system,
       v.version, COALESCE(f.attrs ->> 'archive_ext', '')::text AS archive_ext, b.blob_key, b.sha256 AS checksum, b.size_bytes,
       f.download_count, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key)
ORDER BY f.created_at, f.id;

-- name: ListTerraformModules :many
SELECT f.id, f.repository_id, COALESCE(p.attrs ->> 'name', '')::text AS name, COALESCE(p.attrs ->> 'system', '')::text AS system,
       v.version, COALESCE(f.attrs ->> 'archive_ext', '')::text AS archive_ext, b.blob_key, b.sha256 AS checksum, b.size_bytes,
       f.download_count, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL AND p.attrs ->> 'kind' = 'module'
ORDER BY p.attrs ->> 'name', p.attrs ->> 'system', f.created_at, f.id;

-- name: IncrementTerraformDownloadCount :exec
-- One download of a module archive or a provider zip.
UPDATE package_files SET download_count = download_count + 1
WHERE id = $1 AND repository_id = $2 AND deleted_at IS NULL;

-- name: TerraformPackageStamp :one
-- Fingerprint of the live files of one module or provider type: their count
-- and an order-independent hash of their IDs. Files are immutable apart from
-- the download counter (updated_at moves with every download, so it is no
-- validator), and trash/restore changes membership.
SELECT (count(*)::text || ':' || COALESCE(sum(hashtextextended(f.id::text, 0)), 0)::text)::text AS stamp
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
WHERE p.repository_id = $1 AND p.name_key = $2;

-- name: GetTerraformProvider :one
-- The platform zip by its path (uq_package_files_path).
SELECT f.id, f.repository_id, COALESCE(p.attrs ->> 'type', '')::text AS type, v.version,
       COALESCE(f.attrs ->> 'os', '')::text AS os, COALESCE(f.attrs ->> 'arch', '')::text AS arch, v.metadata,
       b.blob_key, b.sha256 AS checksum, b.size_bytes, f.download_count, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.path = $2 AND f.deleted_at IS NULL;

-- name: ListTerraformProviderPlatforms :many
-- Every live platform zip of one provider type, all versions.
SELECT f.id, f.repository_id, COALESCE(p.attrs ->> 'type', '')::text AS type, v.version,
       COALESCE(f.attrs ->> 'os', '')::text AS os, COALESCE(f.attrs ->> 'arch', '')::text AS arch, v.metadata,
       b.blob_key, b.sha256 AS checksum, b.size_bytes, f.download_count, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key)
ORDER BY v.version, f.arch;

-- name: ListTerraformProviderVersionPlatforms :many
-- Every live platform zip of one provider version.
SELECT f.id, f.repository_id, COALESCE(p.attrs ->> 'type', '')::text AS type, v.version,
       COALESCE(f.attrs ->> 'os', '')::text AS os, COALESCE(f.attrs ->> 'arch', '')::text AS arch, v.metadata,
       b.blob_key, b.sha256 AS checksum, b.size_bytes, f.download_count, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.version_key = sqlc.arg(version) AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key)
ORDER BY f.arch;

-- name: ListTerraformProviders :many
SELECT f.id, f.repository_id, COALESCE(p.attrs ->> 'type', '')::text AS type, v.version,
       COALESCE(f.attrs ->> 'os', '')::text AS os, COALESCE(f.attrs ->> 'arch', '')::text AS arch, v.metadata,
       b.blob_key, b.sha256 AS checksum, b.size_bytes, f.download_count, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL AND p.attrs ->> 'kind' = 'provider'
ORDER BY p.attrs ->> 'type', v.version, f.arch;
