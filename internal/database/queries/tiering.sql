-- name: ListStorageTiers :many
SELECT t.id, t.name, t.backend_id, t.rank, t.promote_on_read, t.created_at, t.updated_at,
       COALESCE(b.name, '')::text AS backend_name,
       COALESCE(b.kind, '')::text AS backend_kind,
       COALESCE(b.status, '')::text AS backend_status
FROM storage_tiers t
LEFT JOIN storage_backends b ON b.id = t.backend_id
ORDER BY t.rank ASC, t.name ASC;

-- name: GetStorageTierByName :one
SELECT t.id, t.name, t.backend_id, t.rank, t.promote_on_read, t.created_at, t.updated_at,
       COALESCE(b.name, '')::text AS backend_name,
       COALESCE(b.kind, '')::text AS backend_kind,
       COALESCE(b.status, '')::text AS backend_status
FROM storage_tiers t
LEFT JOIN storage_backends b ON b.id = t.backend_id
WHERE t.name = $1;

-- name: CreateStorageTier :exec
INSERT INTO storage_tiers (id, name, backend_id, rank, promote_on_read)
VALUES ($1, $2, sqlc.narg('backend_id'), $3, $4);

-- name: UpdateStorageTier :exec
UPDATE storage_tiers SET name = $1, rank = $2, promote_on_read = $3 WHERE id = $4;

-- name: ListStoragePolicies :many
SELECT p.id, p.name, p.enabled, p.position, p.target_tier_id, p.from_tier_id,
       p.min_age_seconds, p.min_idle_seconds, p.min_size_bytes, p.max_size_bytes,
       p.repo_glob, p.formats, p.path_glob, p.created_at, p.updated_at
FROM storage_policies p
ORDER BY p.position ASC, p.name ASC;

-- name: GetStoragePolicy :one
SELECT p.id, p.name, p.enabled, p.position, p.target_tier_id, p.from_tier_id,
       p.min_age_seconds, p.min_idle_seconds, p.min_size_bytes, p.max_size_bytes,
       p.repo_glob, p.formats, p.path_glob, p.created_at, p.updated_at
FROM storage_policies p
WHERE p.id = $1;

-- name: CreateStoragePolicy :exec
INSERT INTO storage_policies (id, name, enabled, position, target_tier_id, from_tier_id,
                              min_age_seconds, min_idle_seconds, min_size_bytes, max_size_bytes,
                              repo_glob, formats, path_glob)
VALUES ($1, $2, $3, $4, $5, sqlc.narg('from_tier_id'),
        sqlc.narg('min_age_seconds'), sqlc.narg('min_idle_seconds'), sqlc.narg('min_size_bytes'), sqlc.narg('max_size_bytes'),
        sqlc.narg('repo_glob'), $6, sqlc.narg('path_glob'));

-- name: UpdateStoragePolicy :exec
UPDATE storage_policies SET
    name = $1, enabled = $2, position = $3, target_tier_id = $4, from_tier_id = sqlc.narg('from_tier_id'),
    min_age_seconds = sqlc.narg('min_age_seconds'), min_idle_seconds = sqlc.narg('min_idle_seconds'),
    min_size_bytes = sqlc.narg('min_size_bytes'), max_size_bytes = sqlc.narg('max_size_bytes'),
    repo_glob = sqlc.narg('repo_glob'), formats = $5, path_glob = sqlc.narg('path_glob')
WHERE id = $6;

-- name: DeleteStoragePolicy :execrows
DELETE FROM storage_policies WHERE id = $1;

-- name: StorageTierUsage :many
SELECT storage_backend_id, COUNT(*)::bigint AS blob_count, COALESCE(SUM(size_bytes), 0)::bigint AS total_bytes
FROM blobs
GROUP BY storage_backend_id;

-- name: GetBlobPlacement :one
SELECT storage_backend_id, size_bytes FROM blobs WHERE sha256 = $1;

-- name: BeginStorageTierMove :execrows
INSERT INTO storage_tier_moves (sha256, from_backend_id, to_backend_id, state, reason, size_bytes)
VALUES ($1, sqlc.narg('from_backend_id'), sqlc.narg('to_backend_id'), 'copying', $2, $3)
ON CONFLICT (sha256) DO NOTHING;

-- name: GetStorageTierMove :one
SELECT sha256, from_backend_id, to_backend_id, state, reason, size_bytes, attempts, last_error, created_at, updated_at
FROM storage_tier_moves WHERE sha256 = $1;

-- name: ListRecoverableStorageTierMoves :many
SELECT sha256, from_backend_id, to_backend_id, state, reason, size_bytes, attempts, last_error, created_at, updated_at
FROM storage_tier_moves
WHERE attempts < $1 OR updated_at < now() - make_interval(secs => $2::double precision)
ORDER BY updated_at ASC
LIMIT $3;

-- name: ListStorageTierMoves :many
SELECT sha256, from_backend_id, to_backend_id, state, reason, size_bytes, attempts, last_error, created_at, updated_at
FROM storage_tier_moves
ORDER BY updated_at DESC
LIMIT $1;

-- name: CountStorageTierMoves :one
SELECT COUNT(*) FROM storage_tier_moves;

-- name: FailStorageTierMove :exec
UPDATE storage_tier_moves SET attempts = attempts + 1, last_error = $2, updated_at = now() WHERE sha256 = $1;

-- name: FinishStorageTierMove :exec
DELETE FROM storage_tier_moves WHERE sha256 = $1;
