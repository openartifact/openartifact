-- name: CreateNixPath :one
INSERT INTO nix_paths (repository_id, store_hash, name, nar_hash, nar_size, nar_file, compression, refs, deriver, ca, signatures,
                       blob_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, COALESCE(sqlc.narg(created_at)::timestamptz, NOW()))
RETURNING id, created_at, updated_at;

-- name: GetNixPathByHash :one
SELECT np.id, np.repository_id, np.store_hash, np.name, np.nar_hash, np.nar_size, np.nar_file, np.compression, np.refs, np.deriver, np.ca,
       np.signatures, np.download_count, b.blob_key, b.sha256 AS blob_sha256, b.size_bytes, np.created_at, np.updated_at
FROM nix_paths np
JOIN blobs b ON b.id = np.blob_id
WHERE np.repository_id = $1 AND np.store_hash = $2 AND np.deleted_at IS NULL;

-- name: GetNixPathByNarFile :one
SELECT np.id, np.repository_id, np.store_hash, np.name, np.nar_hash, np.nar_size, np.nar_file, np.compression, np.refs, np.deriver, np.ca,
       np.signatures, np.download_count, b.blob_key, b.sha256 AS blob_sha256, b.size_bytes, np.created_at, np.updated_at
FROM nix_paths np
JOIN blobs b ON b.id = np.blob_id
WHERE np.repository_id = $1 AND np.nar_file = $2 AND np.deleted_at IS NULL
ORDER BY np.created_at, np.id
LIMIT 1;

-- name: ListNixPaths :many
SELECT np.id, np.repository_id, np.store_hash, np.name, np.nar_hash, np.nar_size, np.nar_file, np.compression, np.refs, np.deriver, np.ca,
       np.signatures, np.download_count, b.blob_key, b.sha256 AS blob_sha256, b.size_bytes, np.created_at, np.updated_at
FROM nix_paths np
JOIN blobs b ON b.id = np.blob_id
WHERE np.repository_id = $1 AND np.deleted_at IS NULL
ORDER BY np.store_hash;

-- name: DeleteNixPath :exec
DELETE FROM nix_paths WHERE id = $1 AND repository_id = $2 AND deleted_at IS NULL;

-- name: IncrementNixDownloadCount :exec
UPDATE nix_paths SET download_count = download_count + 1
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
