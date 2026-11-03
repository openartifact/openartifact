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

-- name: ListCachedProxyKeys :many
-- The cache keys of a repository's live cached files (a body in the blob store)
-- that start with a prefix: which versions of one package are cached.
SELECT cache_key
FROM proxy_cache_entries
WHERE repository_id = sqlc.arg(repo_id)::uuid
  AND status_code = 200
  AND blob_key IS NOT NULL
  AND starts_with(cache_key, sqlc.arg(prefix)::text)
ORDER BY cache_key
LIMIT sqlc.arg(max_keys)::int;

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

-- A hosted deb repository's package files as a deb group merges them: the
-- columns of ListDebPackageEntries without the control stanza, plus the row
-- version of that stanza (xmin of the deb_files row). The group keeps the
-- Packages stanza it rendered for a file under this key and only reads the
-- controls of the files it has not seen. Same order as ListDebPackageEntries.
-- name: ListDebPackageStamps :many
SELECT f.id, p.name, v.version, f.arch, f.filename,
       d.distribution_id, dd.name AS distribution, dc.name AS component_name,
       COALESCE(b.sha256, '')::text AS sha256,
       COALESCE(b.size_bytes, 0)::bigint AS size,
       COALESCE((f.attrs ->> 'installed_size')::bigint, 0)::bigint AS installed_size,
       d.xmin::text::bigint AS control_rev
FROM package_files f
JOIN deb_files d ON d.id = f.id
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN debian_distributions dd ON dd.id = d.distribution_id
JOIN debian_components dc ON dc.id = d.component_id
LEFT JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL
ORDER BY dd.name, dc.name, f.arch, p.name, v.version;

-- The package files of ListDebPackageStamps with their control stanzas, for
-- the given file ids (those the group has no rendered stanza for).
-- name: ListDebPackagesWithControls :many
SELECT f.id, p.name, v.version, f.arch, f.filename,
       d.distribution_id, dd.name AS distribution, dc.name AS component_name,
       COALESCE(b.sha256, '')::text AS sha256,
       COALESCE(b.size_bytes, 0)::bigint AS size,
       COALESCE((f.attrs ->> 'installed_size')::bigint, 0)::bigint AS installed_size,
       d.xmin::text::bigint AS control_rev,
       d.control_fields
FROM package_files f
JOIN deb_files d ON d.id = f.id
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN debian_distributions dd ON dd.id = d.distribution_id
JOIN debian_components dc ON dc.id = d.component_id
LEFT JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = sqlc.arg(repository_id)::uuid AND f.deleted_at IS NULL
  AND f.id = ANY(sqlc.arg(ids)::uuid[]);
