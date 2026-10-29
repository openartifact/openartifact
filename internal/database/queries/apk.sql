-- Apk packages on the generic tables (see pkg_apk.go for the mapping): the
-- package identity branch/repo/name is in packages.attrs, the APKINDEX stanza
-- fields in package_versions.metadata. Reads hide trashed files.

-- The live .apk of a mirror-tree path. uq_package_files_path serves it.
-- name: GetApkPackageFile :one
SELECT f.id, f.repository_id,
       COALESCE(p.attrs ->> 'branch', '')::text AS branch,
       COALESCE(p.attrs ->> 'repo', '')::text AS repo,
       f.arch,
       COALESCE(p.attrs ->> 'name', '')::text AS name,
       v.version, v.metadata,
       COALESCE((f.attrs ->> 'build_time')::bigint, 0)::bigint AS build_time,
       COALESCE((f.attrs ->> 'installed_size')::bigint, 0)::bigint AS installed_size,
       b.blob_key, b.sha256 AS blob_sha256, b.size_bytes, f.download_count, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.path = $2 AND f.deleted_at IS NULL;

-- name: ListApkPackages :many
SELECT f.id, f.repository_id,
       COALESCE(p.attrs ->> 'branch', '')::text AS branch,
       COALESCE(p.attrs ->> 'repo', '')::text AS repo,
       f.arch,
       COALESCE(p.attrs ->> 'name', '')::text AS name,
       v.version, v.metadata,
       COALESCE((f.attrs ->> 'build_time')::bigint, 0)::bigint AS build_time,
       COALESCE((f.attrs ->> 'installed_size')::bigint, 0)::bigint AS installed_size,
       b.blob_key, b.sha256 AS blob_sha256, b.size_bytes, f.download_count, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL
ORDER BY p.attrs ->> 'branch', p.attrs ->> 'repo', f.arch, p.attrs ->> 'name', v.version;

-- name: IncrementApkDownloadCount :exec
UPDATE package_files SET download_count = download_count + 1
WHERE id = $1 AND repository_id = $2 AND deleted_at IS NULL;

-- name: GetApkSigningKey :one
SELECT repository_id, key_name, fingerprint, public_key, private_key, created_by, created_at, updated_at
FROM apk_signing_keys WHERE repository_id = $1;

-- name: CreateApkSigningKeyIfAbsent :execrows
INSERT INTO apk_signing_keys (repository_id, key_name, fingerprint, public_key, private_key, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (repository_id) DO NOTHING;

-- name: ReplaceApkSigningKey :exec
INSERT INTO apk_signing_keys (repository_id, key_name, fingerprint, public_key, private_key, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (repository_id) DO UPDATE
SET key_name = EXCLUDED.key_name, fingerprint = EXCLUDED.fingerprint, public_key = EXCLUDED.public_key,
    private_key = EXCLUDED.private_key, created_by = EXCLUDED.created_by;
