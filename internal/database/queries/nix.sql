-- Nix store paths on the generic package tables (mapping spec in pkg_nix.go). One
-- store path (narinfo plus the NAR it points at) is one package_versions row
-- with one package_files row; the store path name is the package and the store
-- hash is the version. nix_pending_nars and nix_signing_keys are not package
-- data and stay as they are.

-- name: GetNixPathByHash :one
-- By the path of the narinfo file (uq_package_files_path).
SELECT f.id, f.repository_id, v.version AS store_hash, p.name, v.metadata, f.download_count,
       b.blob_key, b.sha256 AS blob_sha256, b.size_bytes, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.path = $2 AND f.deleted_at IS NULL;

-- name: GetNixPathByNarFile :one
-- The oldest live path whose NAR file is nar_file. The metadata ? predicate is
-- what makes idx_package_versions_nix_nar_file (a partial index) applicable.
SELECT f.id, f.repository_id, v.version AS store_hash, p.name, v.metadata, f.download_count,
       b.blob_key, b.sha256 AS blob_sha256, b.size_bytes, f.created_at, f.updated_at
FROM (
    SELECT v0.id, v0.package_id, v0.version, v0.metadata, v0.created_at
    FROM package_versions v0
    WHERE v0.repository_id = sqlc.arg(repository_id) AND v0.metadata ->> 'nar_file' = sqlc.arg(nar_file)::text
      AND v0.deleted_at IS NULL AND v0.metadata ? 'nar_file'
    ORDER BY v0.created_at, v0.id
    LIMIT 1
) v
JOIN packages p ON p.id = v.package_id
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
ORDER BY f.created_at, f.id
LIMIT 1;

-- name: ListNixPaths :many
SELECT f.id, f.repository_id, v.version AS store_hash, p.name, v.metadata, f.download_count,
       b.blob_key, b.sha256 AS blob_sha256, b.size_bytes, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL
ORDER BY f.path;

-- name: IncrementNixDownloadCount :exec
UPDATE package_files SET download_count = download_count + 1
WHERE id = $1 AND repository_id = $2 AND deleted_at IS NULL;

-- name: PutNixPendingNar :exec
INSERT INTO nix_pending_nars (repository_id, sha256, nar_file, blob_id)
VALUES ($1, $2, $3, $4)
ON CONFLICT (repository_id, sha256) DO NOTHING;

-- name: GetNixPendingNar :one
SELECT pn.repository_id, pn.sha256, pn.nar_file, b.blob_key, b.size_bytes, pn.created_at
FROM nix_pending_nars pn
JOIN blobs b ON b.id = pn.blob_id
WHERE pn.repository_id = $1 AND pn.sha256 = $2;

-- name: DeleteNixPendingNar :exec
DELETE FROM nix_pending_nars WHERE repository_id = $1 AND sha256 = $2;

-- name: DeleteStaleNixPendingNars :many
WITH stale AS (
    SELECT s.repository_id, s.sha256 FROM nix_pending_nars s
    WHERE s.created_at < sqlc.arg(before)::timestamptz
    ORDER BY s.created_at
    LIMIT sqlc.arg(batch_limit)::int
)
DELETE FROM nix_pending_nars pn
USING stale
WHERE pn.repository_id = stale.repository_id AND pn.sha256 = stale.sha256
RETURNING pn.sha256;

-- name: GetNixSigningKey :one
SELECT repository_id, key_name, public_key, private_key, created_by, created_at, updated_at
FROM nix_signing_keys WHERE repository_id = $1;

-- name: CreateNixSigningKeyIfAbsent :execrows
INSERT INTO nix_signing_keys (repository_id, key_name, public_key, private_key, created_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (repository_id) DO NOTHING;

-- name: ReplaceNixSigningKey :exec
INSERT INTO nix_signing_keys (repository_id, key_name, public_key, private_key, created_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (repository_id) DO UPDATE
SET key_name = EXCLUDED.key_name, public_key = EXCLUDED.public_key,
    private_key = EXCLUDED.private_key, created_by = EXCLUDED.created_by;
