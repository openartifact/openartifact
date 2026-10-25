-- name: GetRepositoryByName :one
SELECT id, name, format, description,
       COALESCE(public, false) AS public,
       COALESCE(allow_full_unpublish, false) AS allow_full_unpublish,
       COALESCE(repo_type, 'hosted') AS repo_type,
       COALESCE(upstream_url, '') AS upstream_url,
       COALESCE(upstream_username, '') AS upstream_username,
       COALESCE(upstream_password_enc, '') AS upstream_password_enc,
       COALESCE(cache_ttl_seconds, 3600) AS cache_ttl_seconds,
       COALESCE(negative_cache_ttl_seconds, 60) AS negative_cache_ttl_seconds,
       COALESCE(blocked, false) AS blocked,
       project_id, created_at, updated_at
FROM repositories
WHERE name = $1 AND deleted_at IS NULL;

-- name: GetRepositoryByID :one
SELECT id, name, format, description,
       COALESCE(public, false) AS public,
       COALESCE(allow_full_unpublish, false) AS allow_full_unpublish,
       COALESCE(repo_type, 'hosted') AS repo_type,
       COALESCE(upstream_url, '') AS upstream_url,
       COALESCE(upstream_username, '') AS upstream_username,
       COALESCE(upstream_password_enc, '') AS upstream_password_enc,
       COALESCE(cache_ttl_seconds, 3600) AS cache_ttl_seconds,
       COALESCE(negative_cache_ttl_seconds, 60) AS negative_cache_ttl_seconds,
       COALESCE(blocked, false) AS blocked,
       project_id, created_at, updated_at
FROM repositories
WHERE id = $1 AND deleted_at IS NULL;

-- name: ListRepositories :many
SELECT id, name, format, description,
       COALESCE(public, false) AS public,
       COALESCE(allow_full_unpublish, false) AS allow_full_unpublish,
       COALESCE(repo_type, 'hosted') AS repo_type,
       COALESCE(upstream_url, '') AS upstream_url,
       COALESCE(upstream_username, '') AS upstream_username,
       COALESCE(upstream_password_enc, '') AS upstream_password_enc,
       COALESCE(cache_ttl_seconds, 3600) AS cache_ttl_seconds,
       COALESCE(negative_cache_ttl_seconds, 60) AS negative_cache_ttl_seconds,
       COALESCE(blocked, false) AS blocked,
       project_id, created_at, updated_at
FROM repositories
WHERE deleted_at IS NULL
ORDER BY created_at DESC, id
LIMIT $1 OFFSET $2;

-- name: CountRepositories :one
SELECT COUNT(*) FROM repositories WHERE deleted_at IS NULL;

-- name: CreateRepository :one
INSERT INTO repositories (id, name, format, description, repo_type, upstream_url, upstream_username, upstream_password_enc, cache_ttl_seconds, negative_cache_ttl_seconds, blocked, project_id)
VALUES (gen_random_uuid(), $1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, ''), $8, $9, $10, $11)
RETURNING id, created_at, updated_at;

-- name: UpdateRepository :exec
UPDATE repositories
SET description = $1,
    public = $2,
    repo_type = $3,
    upstream_url = NULLIF($4, ''),
    upstream_username = NULLIF($5, ''),
    upstream_password_enc = NULLIF($6, ''),
    cache_ttl_seconds = $7,
    negative_cache_ttl_seconds = $8,
    blocked = $9,
    project_id = $10,
    updated_at = now()
WHERE id = $11 AND deleted_at IS NULL;

-- name: DeleteRepository :exec
DELETE FROM repositories WHERE id = $1;
