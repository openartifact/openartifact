-- name: AddGroupMember :exec
INSERT INTO repository_group_members (group_repo_id, member_repo_id, priority)
VALUES ($1, $2, $3)
ON CONFLICT (group_repo_id, member_repo_id) DO UPDATE SET priority = $3;

-- name: ListGroupMembers :many
SELECT m.group_repo_id, m.member_repo_id, m.priority, m.created_at
FROM repository_group_members m
JOIN repositories r ON r.id = m.member_repo_id AND r.deleted_at IS NULL
WHERE m.group_repo_id = $1
ORDER BY m.priority ASC, m.created_at, m.member_repo_id;

-- name: GetProxyCacheEntry :one
SELECT id, repository_id, cache_key,
       COALESCE(blob_key, '') AS blob_key,
       COALESCE(content_type, '') AS content_type,
       status_code, size_bytes, hit_count, last_hit_at, expires_at, created_at, updated_at
FROM proxy_cache_entries
WHERE repository_id = $1 AND cache_key = $2;

-- name: UpsertProxyCacheEntry :one
INSERT INTO proxy_cache_entries (repository_id, cache_key, blob_key, content_type, status_code, size_bytes, hit_count, last_hit_at, expires_at)
VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, $6, $7, $8, $9)
ON CONFLICT (repository_id, cache_key) DO UPDATE SET
    blob_key = NULLIF($3, ''),
    content_type = NULLIF($4, ''),
    status_code = $5,
    size_bytes = $6,
    hit_count = proxy_cache_entries.hit_count + 1,
    last_hit_at = now(),
    expires_at = $9,
    updated_at = now()
RETURNING id, created_at, updated_at;

-- name: UpsertNegativeProxyCacheEntry :exec
-- Records a 404 for a missing upstream path, but never lets one repository hold
-- more than $4 live negative rows: a public proxy would otherwise store one
-- row per unique path a reader probes. An existing row is always refreshed.
INSERT INTO proxy_cache_entries (repository_id, cache_key, status_code, size_bytes, hit_count, last_hit_at, expires_at)
SELECT $1, $2, 404, 0, 0, now(), $3
WHERE EXISTS (SELECT 1 FROM proxy_cache_entries WHERE repository_id = $1 AND cache_key = $2)
   OR (SELECT count(*) FROM (SELECT 1 FROM proxy_cache_entries
                             WHERE repository_id = $1 AND status_code = 404 AND expires_at > now()
                             LIMIT $4) n) < $4
ON CONFLICT (repository_id, cache_key) DO UPDATE SET
    blob_key = NULL,
    content_type = NULL,
    status_code = 404,
    size_bytes = 0,
    expires_at = $3,
    updated_at = now();

-- name: DeleteProxyCacheEntry :exec
DELETE FROM proxy_cache_entries WHERE repository_id = $1 AND cache_key = $2;

-- name: DeleteExpiredProxyCacheEntries :execrows
-- Cached files stay past their expiry for the repository's max_stale_seconds so
-- they can still be served when the upstream is down; negative entries do not.
-- The plain expires_at < now() is the sargable bound every deletable row meets
-- (the CASE only subtracts more): it lets the planner use idx_proxy_cache_expires
-- instead of scanning the whole table.
DELETE FROM proxy_cache_entries e
USING repositories r
WHERE r.id = e.repository_id
  AND r.deleted_at IS NULL
  AND e.expires_at < now()
  AND e.expires_at < now() - CASE
      WHEN e.blob_key IS NOT NULL THEN COALESCE(r.max_stale_seconds, 0) * interval '1 second'
      ELSE interval '0'
  END;
