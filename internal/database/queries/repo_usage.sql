-- Repository usage summary: how many versions and distinct packages a
-- repository holds and when something was last published. One generic
-- branch over the listed live package files plus the proxy cache and docker
-- tags, mirroring CountRepoComponents (same rows, same name
-- expressions as the package listing). Scans the repository's package rows, so
-- it is an on-demand per-repository read, never a listing of all repositories.
-- last_published_at is the Unix epoch for an empty repository (an aggregate
-- over no rows would be NULL, which the generated non-null type cannot scan).
-- Keep in sync with CountRepoComponents.

-- name: RepoUsageStats :one
SELECT COUNT(*)::bigint AS version_count,
       COUNT(DISTINCT u.name)::bigint AS package_count,
       COALESCE(MAX(u.created_at), 'epoch'::timestamptz)::timestamptz AS last_published_at
FROM (
    -- generic files (every format but docker): the listed live files
    SELECT pk.name::text AS name, f.created_at::timestamptz AS created_at
    FROM package_files f
    JOIN package_versions v ON v.id = f.version_id
    JOIN packages pk ON pk.id = v.package_id
    WHERE f.deleted_at IS NULL AND f.listed AND f.repository_id = sqlc.arg(repo_id)::uuid
    UNION ALL
    -- proxy cache
    SELECT t.cache_key::text AS name, t.created_at::timestamptz AS created_at FROM proxy_cache_entries t JOIN repositories r ON r.id = t.repository_id
    WHERE r.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.status_code = 200 AND t.blob_key IS NOT NULL
    UNION ALL
    -- docker
    SELECT (CASE WHEN t.image = '' THEN (SELECT r.name FROM repositories r WHERE r.id = t.repository_id) ELSE (SELECT r.name FROM repositories r WHERE r.id = t.repository_id) || '/' || t.image END)::text AS name, t.created_at::timestamptz AS created_at FROM docker_tags t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid
) AS u;
