-- name: CreateApkPackage :one
INSERT INTO apk_packages (repository_id, branch, repo, arch, pkg_arch, name, version, checksum, data_sha256, description, url, license, origin,
                          maintainer, commit, build_time, installed_size, depends, provides, install_if, provider_priority,
                          blob_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22,
        COALESCE(sqlc.narg(created_at)::timestamptz, NOW()))
RETURNING id, created_at, updated_at;

-- name: GetApkPackageFile :one
SELECT ap.id, ap.repository_id, ap.branch, ap.repo, ap.arch, ap.pkg_arch, ap.name, ap.version, ap.checksum, ap.data_sha256, ap.description, ap.url,
       ap.license, ap.origin, ap.maintainer, ap.commit, ap.build_time, ap.installed_size, ap.depends, ap.provides, ap.install_if,
       ap.provider_priority, b.blob_key, b.sha256 AS blob_sha256, b.size_bytes, ap.download_count, ap.created_at, ap.updated_at
FROM apk_packages ap
JOIN blobs b ON b.id = ap.blob_id
WHERE ap.repository_id = $1 AND ap.branch = $2 AND ap.repo = $3 AND ap.arch = $4 AND ap.name = $5 AND ap.version = $6
  AND ap.deleted_at IS NULL;

-- name: ListApkPackages :many
SELECT ap.id, ap.repository_id, ap.branch, ap.repo, ap.arch, ap.pkg_arch, ap.name, ap.version, ap.checksum, ap.data_sha256, ap.description, ap.url,
       ap.license, ap.origin, ap.maintainer, ap.commit, ap.build_time, ap.installed_size, ap.depends, ap.provides, ap.install_if,
       ap.provider_priority, b.blob_key, b.sha256 AS blob_sha256, b.size_bytes, ap.download_count, ap.created_at, ap.updated_at
FROM apk_packages ap
JOIN blobs b ON b.id = ap.blob_id
WHERE ap.repository_id = $1 AND ap.deleted_at IS NULL
ORDER BY ap.branch, ap.repo, ap.arch, ap.name, ap.version;

-- name: DeleteApkPackage :exec
DELETE FROM apk_packages WHERE id = $1 AND repository_id = $2 AND deleted_at IS NULL;

-- name: IncrementApkDownloadCount :exec
UPDATE apk_packages SET download_count = download_count + 1
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
