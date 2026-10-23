-- name: ListGPGKeys :many
SELECT id, name, fingerprint, key_id, algorithm, user_id, public_key, ''::text AS private_key_enc,
       is_global_active, source, created_by, created_at, expires_at
FROM gpg_keys
ORDER BY created_at, id;

-- name: GetGPGKey :one
SELECT id, name, fingerprint, key_id, algorithm, user_id, public_key, private_key_enc,
       is_global_active, source, created_by, created_at, expires_at
FROM gpg_keys
WHERE id = $1;

-- name: GetGPGKeyByFingerprint :one
SELECT id, name, fingerprint, key_id, algorithm, user_id, public_key, private_key_enc,
       is_global_active, source, created_by, created_at, expires_at
FROM gpg_keys
WHERE fingerprint = $1;

-- name: DeleteGPGKey :execrows
DELETE FROM gpg_keys WHERE id = $1;

-- name: ClearGlobalGPGKey :exec
UPDATE gpg_keys SET is_global_active = false WHERE is_global_active;

-- name: SetRepositoryGPGKey :exec
INSERT INTO gpg_key_repositories (repository_id, gpg_key_id, assigned_by)
VALUES ($1, $2, $3)
ON CONFLICT (repository_id) DO UPDATE
SET gpg_key_id = EXCLUDED.gpg_key_id, assigned_by = EXCLUDED.assigned_by, assigned_at = now();

-- name: ClearRepositoryGPGKey :execrows
DELETE FROM gpg_key_repositories WHERE repository_id = $1;

-- name: ListGPGKeyAssignments :many
SELECT a.gpg_key_id, a.repository_id, r.name AS repository_name, r.format AS repository_format
FROM gpg_key_repositories a
JOIN repositories r ON r.id = a.repository_id
ORDER BY r.name;

-- name: ListRepositoriesForGPGKey :many
SELECT r.id, r.format
FROM gpg_key_repositories a
JOIN repositories r ON r.id = a.repository_id
WHERE a.gpg_key_id = $1;

-- name: ResolveGPGKeyForRepository :one
-- The repository's own assignment wins; otherwise the global active key.
SELECT k.id, k.name, k.fingerprint, k.key_id, k.algorithm, k.user_id, k.public_key, k.private_key_enc,
       k.is_global_active, k.source, k.created_by, k.created_at, k.expires_at
FROM gpg_keys k
WHERE k.id = (SELECT a.gpg_key_id FROM gpg_key_repositories a WHERE a.repository_id = $1)
   OR (k.is_global_active AND NOT EXISTS (SELECT 1 FROM gpg_key_repositories b WHERE b.repository_id = $1))
LIMIT 1;

-- name: ListSigningRepositoriesWithoutKey :many
-- deb and rpm repositories that have no key of their own (they follow the global key).
SELECT r.id, r.format
FROM repositories r
WHERE r.format IN ('deb', 'rpm') AND r.repo_type = 'hosted'
  AND NOT EXISTS (SELECT 1 FROM gpg_key_repositories a WHERE a.repository_id = r.id);
