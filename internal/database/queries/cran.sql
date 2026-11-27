-- R packages (CRAN) on the generic package tables (mapping spec in pkg_cran.go).
-- One package file (a source tarball, a Windows zip or a macOS tgz) is one
-- package_files row; the files of one package version share its
-- package_versions row.

-- name: GetCRANFile :one
SELECT f.id, f.repository_id, p.name, v.version, v.metadata, f.path, f.filename, f.arch, f.attrs, f.checksums,
       b.blob_key, b.sha256 AS digest, b.size_bytes, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = sqlc.arg(repository_id) AND f.path = sqlc.arg(path) AND f.deleted_at IS NULL AND p.format = 'cran';

-- The live files of one tree (f.arch is the tree's directory), for its PACKAGES.
-- name: ListCRANTreeFiles :many
SELECT f.id, f.repository_id, p.name, v.version, v.metadata, f.path, f.filename, f.arch, f.attrs, f.checksums,
       b.blob_key, b.sha256 AS digest, b.size_bytes, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = sqlc.arg(repository_id) AND f.arch = sqlc.arg(arch) AND f.deleted_at IS NULL AND p.format = 'cran'
ORDER BY p.name, f.path;

-- The live files of one package in every tree, for the Archive listing and the
-- hosted-wins probe.
-- name: ListCRANPackageFiles :many
SELECT f.id, f.repository_id, p.name, v.version, v.metadata, f.path, f.filename, f.arch, f.attrs, f.checksums,
       b.blob_key, b.sha256 AS digest, b.size_bytes, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name) AND p.format = 'cran'
ORDER BY f.path;

-- name: ListCRANPackageNames :many
SELECT DISTINCT p.name
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL AND p.format = 'cran'
ORDER BY p.name;

-- name: CRANIndexStamp :one
-- Fingerprint of the live files of a repository: their count and an
-- order-independent hash of their ids. A file is immutable (a re-upload of a
-- path is a conflict, and trash/restore changes membership), so the stamp
-- changes exactly when a PACKAGES index or the Archive listing would. It reads
-- one narrow index range (idx_package_files_listing).
SELECT (count(*)::text || ':' || COALESCE(sum(hashtextextended(f.id::text, 0)), 0)::text)::text AS stamp
FROM package_files f
WHERE f.repository_id = $1 AND f.deleted_at IS NULL;
