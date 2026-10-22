-- name: CreatePyPIPackage :one
INSERT INTO pypi_packages (repository_id, name, version, filename, package_type, python_version, requires_python, summary, author, author_email, license, keywords, classifiers, blob_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
RETURNING id, created_at, updated_at;

-- name: GetPyPIPackage :one
SELECT pp.id, pp.repository_id, pp.name, pp.version, pp.filename, pp.package_type, pp.python_version, pp.requires_python, pp.summary, pp.author, pp.author_email, pp.license, pp.keywords, pp.classifiers, b.blob_key, b.sha256 AS sha256_digest, b.size_bytes, pp.created_at, pp.updated_at
FROM pypi_packages pp
JOIN blobs b ON b.id = pp.blob_id
WHERE pp.repository_id = $1 AND pp.deleted_at IS NULL AND pp.name = $2 AND pp.version = $3 AND pp.filename = $4;

-- name: GetPyPIPackagesByFilename :many
-- At most two rows: enough to tell a unique file from an ambiguous filename.
SELECT pp.id, pp.repository_id, pp.name, pp.version, pp.filename, pp.package_type, pp.python_version, pp.requires_python, pp.summary, pp.author, pp.author_email, pp.license, pp.keywords, pp.classifiers, b.blob_key, b.sha256 AS sha256_digest, b.size_bytes, pp.created_at, pp.updated_at
FROM pypi_packages pp
JOIN blobs b ON b.id = pp.blob_id
WHERE pp.repository_id = $1 AND pp.deleted_at IS NULL AND pp.filename = $2
ORDER BY pp.created_at, pp.id
LIMIT 2;

-- name: DeletePyPIPackage :exec
DELETE FROM pypi_packages WHERE repository_id = $1 AND name = $2 AND version = $3 AND filename = $4 AND deleted_at IS NULL;

-- name: ListPyPIPackages :many
SELECT pp.id, pp.repository_id, pp.name, pp.version, pp.filename, pp.package_type, pp.python_version, pp.requires_python, pp.summary, pp.author, pp.author_email, pp.license, pp.keywords, pp.classifiers, b.blob_key, b.sha256 AS sha256_digest, b.size_bytes, pp.created_at, pp.updated_at
FROM pypi_packages pp
JOIN blobs b ON b.id = pp.blob_id
WHERE pp.repository_id = $1 AND pp.deleted_at IS NULL
ORDER BY pp.name, pp.version;

-- name: ListPyPIPackageNames :many
-- Distinct stored project names of a repository, for the /simple/ root. No blob
-- join and no per-file columns: the page needs the names only.
SELECT DISTINCT name::text AS name
FROM pypi_packages
WHERE repository_id = $1 AND deleted_at IS NULL
ORDER BY name;

-- name: ListPyPIPackageVersions :many
-- The second arm finds legacy rows stored with a "--" run in their name; it is
-- served by the partial expression index idx_pypi_packages_legacy_name, so the
-- OR resolves to a BitmapOr of two index scans, never a table scan.
SELECT pp.id, pp.repository_id, pp.name, pp.version, pp.filename, pp.package_type, pp.python_version, pp.requires_python, pp.summary, pp.author, pp.author_email, pp.license, pp.keywords, pp.classifiers, b.blob_key, b.sha256 AS sha256_digest, b.size_bytes, pp.created_at, pp.updated_at
FROM pypi_packages pp
JOIN blobs b ON b.id = pp.blob_id
WHERE pp.repository_id = $1 AND pp.deleted_at IS NULL
  AND (pp.name = $2 OR (pp.name LIKE '%--%' AND regexp_replace(pp.name, '-+', '-', 'g') = $2))
ORDER BY pp.version DESC;
