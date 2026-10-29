-- NuGet packages on the generic package tables (mapping spec in pkg_nuget.go). One
-- .nupkg is one package_versions row with one package_files row; the package
-- id is the package, looked up by its lower-cased name_key.

-- name: GetNuGetPackage :one
SELECT f.id, f.repository_id, p.name AS package_id, v.version, v.metadata, b.blob_key, b.sha256 AS package_hash, b.size_bytes AS package_size,
       f.download_count, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.version_key = sqlc.arg(version) AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key);

-- The live files of one package version, for DeleteNuGetPackage.
-- name: ListNuGetPackageFileIDs :many
SELECT f.id
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.version_key = sqlc.arg(version) AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key);

-- name: ListNuGetPackages :many
SELECT f.id, f.repository_id, p.name AS package_id, v.version, v.metadata, b.blob_key, b.sha256 AS package_hash, b.size_bytes AS package_size,
       f.download_count, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL
ORDER BY p.name_key, v.version DESC;

-- name: ListNuGetPackageVersions :many
SELECT f.id, f.repository_id, p.name AS package_id, v.version, v.metadata, b.blob_key, b.sha256 AS package_hash, b.size_bytes AS package_size,
       f.download_count, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key)
ORDER BY v.version DESC;

-- name: SearchNuGetPackagesCount :one
-- Counts matching packages (not versions). The query is lower-cased and
-- LIKE-escaped by the caller; name_key is the lower-cased package id.
SELECT COUNT(DISTINCT p.id) FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
WHERE p.repository_id = sqlc.arg(repository_id)
  AND (p.name_key LIKE sqlc.arg(pattern) OR lower(v.metadata ->> 'title') LIKE sqlc.arg(pattern)
       OR lower(v.metadata ->> 'description') LIKE sqlc.arg(pattern));

-- name: SearchNuGetPackages :many
-- OFFSET/LIMIT page over matching packages, not versions: every live version of
-- each package on the page is returned so callers can group them per package.
WITH matched AS (
    SELECT p.id, p.name_key
    FROM packages p
    JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
    WHERE p.repository_id = sqlc.arg(repository_id)
      AND (p.name_key LIKE sqlc.arg(pattern) OR lower(v.metadata ->> 'title') LIKE sqlc.arg(pattern)
           OR lower(v.metadata ->> 'description') LIKE sqlc.arg(pattern))
    GROUP BY p.id
    ORDER BY p.name_key
    OFFSET sqlc.arg(page_offset) LIMIT sqlc.arg(page_limit)
)
SELECT f.id, f.repository_id, p.name AS package_id, v.version, v.metadata, b.blob_key, b.sha256 AS package_hash, b.size_bytes AS package_size,
       f.download_count, f.created_at, f.updated_at
FROM matched m
JOIN packages p ON p.id = m.id
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
ORDER BY m.name_key, v.created_at, v.id;

-- name: IncrementNuGetDownloadCount :exec
UPDATE package_files f SET download_count = f.download_count + 1, updated_at = NOW()
FROM package_versions v, packages p
WHERE f.version_id = v.id AND v.package_id = p.id
  AND p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key)
  AND v.version_key = sqlc.arg(version) AND v.deleted_at IS NULL AND f.deleted_at IS NULL;

-- name: SetNuGetDependencyGroups :exec
-- Backfills the dependency groups of a package published before they were
-- stored. It never overwrites a value; the file's updated_at (the one the
-- registration validators read) is left alone.
UPDATE package_versions v
SET metadata = v.metadata || jsonb_build_object('dependency_groups', sqlc.arg(dependency_groups)::jsonb)
FROM packages p
WHERE v.package_id = p.id AND p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key)
  AND v.version_key = sqlc.arg(version) AND v.deleted_at IS NULL AND NOT (v.metadata ? 'dependency_groups');
