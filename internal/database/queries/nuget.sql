-- name: CreateNuGetPackage :one
INSERT INTO nuget_packages (repository_id, package_id, version, title, description, summary, authors, owners, tags, project_url, license_url, icon_url, require_license_acceptance, is_prerelease, min_client_version, blob_id, dependency_groups, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, sqlc.narg(dependency_groups)::jsonb, COALESCE(sqlc.narg(created_at)::timestamptz, NOW()))
ON CONFLICT (repository_id, package_id, version) WHERE deleted_at IS NULL DO NOTHING
RETURNING id, created_at, updated_at;

-- name: GetNuGetPackage :one
SELECT np.id, np.repository_id, np.package_id, np.version, np.title, np.description, np.summary, np.authors, np.owners, np.tags, np.project_url, np.license_url, np.icon_url, np.require_license_acceptance, np.is_prerelease, np.min_client_version, np.dependency_groups, b.blob_key, b.sha256 AS package_hash, b.size_bytes AS package_size, np.download_count, np.created_at, np.updated_at
FROM nuget_packages np
JOIN blobs b ON b.id = np.blob_id
WHERE np.repository_id = $1 AND np.deleted_at IS NULL AND np.package_id = $2 AND np.version = $3;

-- name: DeleteNuGetPackage :exec
DELETE FROM nuget_packages WHERE repository_id = $1 AND package_id = $2 AND version = $3 AND deleted_at IS NULL;

-- name: ListNuGetPackages :many
SELECT np.id, np.repository_id, np.package_id, np.version, np.title, np.description, np.summary, np.authors, np.owners, np.tags, np.project_url, np.license_url, np.icon_url, np.require_license_acceptance, np.is_prerelease, np.min_client_version, np.dependency_groups, b.blob_key, b.sha256 AS package_hash, b.size_bytes AS package_size, np.download_count, np.created_at, np.updated_at
FROM nuget_packages np
JOIN blobs b ON b.id = np.blob_id
WHERE np.repository_id = $1 AND np.deleted_at IS NULL
ORDER BY np.package_id, np.version DESC;

-- name: ListNuGetPackageVersions :many
SELECT np.id, np.repository_id, np.package_id, np.version, np.title, np.description, np.summary, np.authors, np.owners, np.tags, np.project_url, np.license_url, np.icon_url, np.require_license_acceptance, np.is_prerelease, np.min_client_version, np.dependency_groups, b.blob_key, b.sha256 AS package_hash, b.size_bytes AS package_size, np.download_count, np.created_at, np.updated_at
FROM nuget_packages np
JOIN blobs b ON b.id = np.blob_id
WHERE np.repository_id = $1 AND np.deleted_at IS NULL AND np.package_id = $2
ORDER BY np.version DESC;

-- name: SearchNuGetPackagesCount :one
-- Counts matching packages (not versions).
SELECT COUNT(DISTINCT package_id) FROM nuget_packages
WHERE repository_id = $1 AND deleted_at IS NULL AND (lower(package_id) LIKE $2 OR lower(title) LIKE $2 OR lower(description) LIKE $2);

-- name: SearchNuGetPackages :many
-- OFFSET/LIMIT page over matching packages, not versions: every live version of
-- each package on the page is returned so callers can group them per package.
WITH matched AS (
    SELECT m.package_id
    FROM nuget_packages m
    WHERE m.repository_id = $1 AND m.deleted_at IS NULL AND (lower(m.package_id) LIKE $2 OR lower(m.title) LIKE $2 OR lower(m.description) LIKE $2)
    GROUP BY m.package_id
    ORDER BY m.package_id
    OFFSET $3 LIMIT $4
)
SELECT np.id, np.repository_id, np.package_id, np.version, np.title, np.description, np.summary, np.authors, np.owners, np.tags, np.project_url, np.license_url, np.icon_url, np.require_license_acceptance, np.is_prerelease, np.min_client_version, np.dependency_groups, b.blob_key, b.sha256 AS package_hash, b.size_bytes AS package_size, np.download_count, np.created_at, np.updated_at
FROM nuget_packages np
JOIN blobs b ON b.id = np.blob_id
WHERE np.repository_id = $1 AND np.deleted_at IS NULL AND np.package_id IN (SELECT package_id FROM matched)
ORDER BY np.package_id, np.created_at, np.id;

-- name: IncrementNuGetDownloadCount :exec
UPDATE nuget_packages SET download_count = download_count + 1, updated_at = NOW()
WHERE repository_id = $1 AND package_id = $2 AND version = $3 AND deleted_at IS NULL;

-- name: SetNuGetDependencyGroups :exec
-- Backfills the dependency groups of a package published before they were
-- stored. It never overwrites a value and leaves updated_at alone.
UPDATE nuget_packages SET dependency_groups = sqlc.arg(dependency_groups)::jsonb
WHERE repository_id = sqlc.arg(repository_id) AND package_id = sqlc.arg(package_id) AND version = sqlc.arg(version)
  AND deleted_at IS NULL AND dependency_groups IS NULL;
