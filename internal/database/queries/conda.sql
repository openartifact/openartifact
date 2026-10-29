-- Conda on the generic package tables (see the mapping in pkg_conda.go). One
-- package per name, one version per build (version, build string, platform),
-- one file per extension of a build.

-- name: GetCondaPackageFile :one
-- The path (platform/filename) is the unique live key; the name and version
-- conditions only disambiguate a filename that several (name, version) pairs
-- could spell.
SELECT f.id, f.repository_id, p.name, v.version, v.metadata, f.arch, f.attrs, f.checksums,
       b.blob_key, b.size_bytes, b.sha256, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.path = $2 AND f.deleted_at IS NULL
  AND p.name = sqlc.arg(name)::text AND v.version = sqlc.arg(version)::text;

-- name: GetCondaFileRepo :one
SELECT repository_id FROM package_files WHERE id = $1 AND deleted_at IS NULL;

-- name: ListCondaPackages :many
SELECT f.id, f.repository_id, p.name, v.version, v.metadata, f.arch, f.attrs, f.checksums,
       b.blob_key, b.size_bytes, b.sha256, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.arch = $2 AND f.deleted_at IS NULL
ORDER BY p.name ASC, v.version DESC, (v.metadata ->> 'build_string') ASC, (f.attrs ->> 'extension') ASC;

-- name: CondaRepodataStamp :one
-- Fingerprint of the live packages of one platform (see HelmIndexStamp):
-- package rows are immutable, so count plus an ID hash tracks repodata.json.
SELECT (count(*)::text || ':' || COALESCE(sum(hashtextextended(f.id::text, 0)), 0)::text)::text AS stamp
FROM package_files f
WHERE f.repository_id = $1 AND f.arch = $2 AND f.deleted_at IS NULL;

-- name: ListCondaPackageNames :many
-- Distinct names of the packages with a live release, in any platform, for a
-- group's hosted-wins rule (compared case-insensitively, as lowercase names).
SELECT DISTINCT lower(p.name)::text AS name
FROM packages p
WHERE p.repository_id = $1
  AND EXISTS (SELECT 1 FROM package_versions v WHERE v.package_id = p.id AND v.deleted_at IS NULL)
ORDER BY 1;

-- name: HasCondaPackageName :one
SELECT EXISTS (
    SELECT 1 FROM packages p
    WHERE p.repository_id = $1 AND lower(p.name) = lower(sqlc.arg(name)::text)
      AND EXISTS (SELECT 1 FROM package_versions v WHERE v.package_id = p.id AND v.deleted_at IS NULL)
)::boolean AS present;
