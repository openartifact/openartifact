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
WHERE f.repository_id = $1 AND f.deleted_at IS NULL AND f.filename = $2 AND p.format = 'pypi'
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

-- name: PyPIIndexStamp :one
-- Fingerprint of the /simple/ root: the count and an order-independent hash of
-- the IDs of the projects that have a live release, the set ListPyPIPackageNames
-- returns. A project's name never changes, so the stamp moves exactly when a
-- project appears or loses its last live release. No blob or file row is read.
SELECT (count(*)::text || ':' || COALESCE(sum(uuid_hash_extended(p.id, 0)), 0)::text)::text AS stamp
FROM packages p
WHERE p.repository_id = $1
  AND EXISTS (SELECT 1 FROM package_versions v WHERE v.package_id = p.id AND v.deleted_at IS NULL);

-- name: PyPIProjectStamp :one
-- Fingerprint of the project page, the legacy JSON document and the files
-- ListPyPIPackageVersions returns for one project: the number of live files
-- (0 = no such project), an order-independent hash of their IDs and the latest
-- update of their releases. A file never changes once stored (a republish is a
-- new file, trash and restore change membership), and the release metadata the
-- JSON "info" shows is rewritten only together with a new file or through
-- SetVersionMetadata, which moves the release's updated_at. Downloads touch the
-- file's updated_at, not the release's, so they leave the stamp alone.
SELECT count(*)::bigint AS files,
       (count(*)::text || ':' || COALESCE(sum(uuid_hash_extended(f.id, 0)), 0)::text || ':' ||
        COALESCE(extract(epoch FROM max(v.updated_at))::text, ''))::text AS stamp
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key);
