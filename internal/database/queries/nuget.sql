-- name: CreateNuGetPackage :one
INSERT INTO nuget_packages (repository_id, package_id, version, title, description, summary, authors, owners, tags, project_url, license_url, icon_url, require_license_acceptance, is_prerelease, min_client_version, blob_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
ON CONFLICT (repository_id, package_id, version) WHERE deleted_at IS NULL DO UPDATE SET updated_at = NOW()
RETURNING id, created_at, updated_at;

-- name: GetNuGetPackage :one
SELECT np.id, np.repository_id, np.package_id, np.version, np.title, np.description, np.summary, np.authors, np.owners, np.tags, np.project_url, np.license_url, np.icon_url, np.require_license_acceptance, np.is_prerelease, np.min_client_version, b.blob_key, b.sha256 AS package_hash, b.size_bytes AS package_size, np.download_count, np.created_at, np.updated_at
FROM nuget_packages np
JOIN blobs b ON b.id = np.blob_id
WHERE np.repository_id = $1 AND np.deleted_at IS NULL AND np.package_id = $2 AND np.version = $3;

-- name: DeleteNuGetPackage :exec
DELETE FROM nuget_packages WHERE repository_id = $1 AND package_id = $2 AND version = $3 AND deleted_at IS NULL;

-- name: ListNuGetPackages :many
SELECT np.id, np.repository_id, np.package_id, np.version, np.title, np.description, np.summary, np.authors, np.owners, np.tags, np.project_url, np.license_url, np.icon_url, np.require_license_acceptance, np.is_prerelease, np.min_client_version, b.blob_key, b.sha256 AS package_hash, b.size_bytes AS package_size, np.download_count, np.created_at, np.updated_at
FROM nuget_packages np
JOIN blobs b ON b.id = np.blob_id
WHERE np.repository_id = $1 AND np.deleted_at IS NULL
ORDER BY np.package_id, np.version DESC;

-- name: ListNuGetPackageVersions :many
SELECT np.id, np.repository_id, np.package_id, np.version, np.title, np.description, np.summary, np.authors, np.owners, np.tags, np.project_url, np.license_url, np.icon_url, np.require_license_acceptance, np.is_prerelease, np.min_client_version, b.blob_key, b.sha256 AS package_hash, b.size_bytes AS package_size, np.download_count, np.created_at, np.updated_at
FROM nuget_packages np
JOIN blobs b ON b.id = np.blob_id
WHERE np.repository_id = $1 AND np.deleted_at IS NULL AND np.package_id = $2
ORDER BY np.version DESC;

-- name: SearchNuGetPackagesCount :one
SELECT COUNT(*) FROM nuget_packages
WHERE repository_id = $1 AND deleted_at IS NULL AND (lower(package_id) LIKE $2 OR lower(title) LIKE $2 OR lower(description) LIKE $2);

-- name: SearchNuGetPackages :many
SELECT np.id, np.repository_id, np.package_id, np.version, np.title, np.description, np.summary, np.authors, np.owners, np.tags, np.project_url, np.license_url, np.icon_url, np.require_license_acceptance, np.is_prerelease, np.min_client_version, b.blob_key, b.sha256 AS package_hash, b.size_bytes AS package_size, np.download_count, np.created_at, np.updated_at
FROM nuget_packages np
JOIN blobs b ON b.id = np.blob_id
WHERE np.repository_id = $1 AND np.deleted_at IS NULL AND (lower(np.package_id) LIKE $2 OR lower(np.title) LIKE $2 OR lower(np.description) LIKE $2)
ORDER BY np.package_id, np.version DESC
OFFSET $3 LIMIT $4;

-- name: IncrementNuGetDownloadCount :exec
UPDATE nuget_packages SET download_count = download_count + 1, updated_at = NOW()
WHERE repository_id = $1 AND package_id = $2 AND version = $3 AND deleted_at IS NULL;
