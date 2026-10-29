-- PyPI on the generic package tables (see the mapping in pkg_pypi.go). One
-- package per normalised project name, one version per release, one file per
-- distribution file.

-- name: GetPyPIPackagesByFilename :many
-- At most two rows: enough to tell a unique file from an ambiguous filename.
-- Served by idx_package_files_filename.
SELECT f.id, f.repository_id, p.name, v.version, f.filename, f.attrs, v.metadata,
       b.blob_key, b.sha256 AS sha256_digest, b.size_bytes, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL AND f.filename = $2
ORDER BY f.created_at, f.id
LIMIT 2;

-- name: GetPyPIFileID :one
SELECT id FROM package_files WHERE repository_id = $1 AND path = $2 AND deleted_at IS NULL;

-- name: ListPyPIPackageNames :many
-- Project names of a repository that have a live release, for the /simple/
-- root. No blob join and no per-file columns: the page needs the names only.
SELECT p.name::text AS name
FROM packages p
WHERE p.repository_id = $1
  AND EXISTS (SELECT 1 FROM package_versions v WHERE v.package_id = p.id AND v.deleted_at IS NULL)
ORDER BY p.name;

-- name: ListPyPIPackageVersions :many
-- The files of one project by its normalised name (uq_packages_name), newest
-- version first.
SELECT f.id, f.repository_id, p.name, v.version, f.filename, f.attrs, v.metadata,
       b.blob_key, b.sha256 AS sha256_digest, b.size_bytes, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = $1 AND p.name_key = $2
ORDER BY v.version DESC, f.created_at, f.id;
