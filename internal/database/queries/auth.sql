-- name: CreateUser :one
INSERT INTO auth_users (username, email, password_hash, full_name, disabled, must_change_password, external_id, provider_slug, sso_managed_groups)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, created_at, updated_at;

-- name: GetUserByID :one
SELECT id, username, email, password_hash, full_name, disabled, must_change_password, last_login_at, created_at, updated_at, external_id, provider_slug, sso_managed_groups
FROM auth_users WHERE id = $1;

-- name: GetUserByUsername :one
SELECT id, username, email, password_hash, full_name, disabled, must_change_password, last_login_at, created_at, updated_at, external_id, provider_slug, sso_managed_groups
FROM auth_users WHERE username = $1;

-- name: GetUserByEmail :one
SELECT id, username, email, password_hash, full_name, disabled, must_change_password, last_login_at, created_at, updated_at, external_id, provider_slug, sso_managed_groups
FROM auth_users WHERE email = $1;

-- name: GetUserByExternalID :one
SELECT id, username, email, password_hash, full_name, disabled, must_change_password, last_login_at, created_at, updated_at, external_id, provider_slug, sso_managed_groups
FROM auth_users WHERE external_id = $1 AND provider_slug = $2;

-- name: UpdateUser :exec
UPDATE auth_users
SET username = $2, email = $3, password_hash = $4, full_name = $5, disabled = $6,
    must_change_password = $7, last_login_at = $8, external_id = $9, provider_slug = $10, sso_managed_groups = $11
WHERE id = $1;

-- name: CountUsers :one
SELECT COUNT(*) FROM auth_users;

-- name: ListUsers :many
SELECT id, username, email, password_hash, full_name, disabled, must_change_password, last_login_at, created_at, updated_at, external_id, provider_slug, sso_managed_groups
FROM auth_users
ORDER BY created_at ASC
LIMIT $1 OFFSET $2;

-- name: CreateGroup :one
INSERT INTO auth_groups (name, description)
VALUES ($1, $2)
RETURNING id, created_at, updated_at;

-- name: GetGroupByID :one
SELECT id, name, description, created_at, updated_at FROM auth_groups WHERE id = $1;

-- name: GetGroupByName :one
SELECT id, name, description, created_at, updated_at FROM auth_groups WHERE name = $1;

-- name: UpdateGroup :exec
UPDATE auth_groups SET name = $2, description = $3 WHERE id = $1;

-- name: DeleteGroup :exec
DELETE FROM auth_groups WHERE id = $1;

-- name: ListGroups :many
SELECT id, name, description, created_at, updated_at FROM auth_groups ORDER BY name ASC;

-- name: AddUserToGroup :exec
INSERT INTO auth_user_groups (user_id, group_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: RemoveUserFromGroup :exec
DELETE FROM auth_user_groups WHERE user_id = $1 AND group_id = $2;

-- name: ListGroupsByUser :many
SELECT g.id, g.name, g.description, g.created_at, g.updated_at
FROM auth_groups g
JOIN auth_user_groups ug ON ug.group_id = g.id
WHERE ug.user_id = $1
ORDER BY g.name ASC;

-- name: ListUsersByGroup :many
SELECT u.id, u.username, u.email, u.password_hash, u.full_name, u.disabled, u.must_change_password, u.last_login_at, u.created_at, u.updated_at, u.external_id, u.provider_slug, u.sso_managed_groups
FROM auth_users u
JOIN auth_user_groups ug ON ug.user_id = u.id
WHERE ug.group_id = $1
ORDER BY u.username ASC;

-- name: CreateSession :one
INSERT INTO auth_sessions (user_id, token_hash, ip_address, user_agent, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, created_at, last_seen_at;

-- name: GetSessionByTokenHash :one
SELECT id, user_id, token_hash, ip_address, user_agent, expires_at, created_at, last_seen_at
FROM auth_sessions WHERE token_hash = $1;

-- name: DeleteSession :exec
DELETE FROM auth_sessions WHERE token_hash = $1;

-- name: UpdateSessionLastSeen :exec
UPDATE auth_sessions SET last_seen_at = now(), expires_at = $2 WHERE id = $1;

-- name: CreateToken :one
INSERT INTO auth_tokens (user_id, name, kind, token_hash, scopes, expires_at)
VALUES ($1, $2, $3, $4, $5::jsonb, $6)
RETURNING id, created_at;

-- name: GetTokenByHash :one
SELECT id, user_id, name, kind, token_hash, scopes, expires_at, last_used_at, created_at, revoked_at
FROM auth_tokens WHERE token_hash = $1;

-- name: GetTokenByID :one
SELECT id, user_id, name, kind, token_hash, scopes, expires_at, last_used_at, created_at, revoked_at
FROM auth_tokens WHERE id = $1;

-- name: RevokeToken :exec
UPDATE auth_tokens SET revoked_at = now() WHERE id = $1;

-- name: UpdateTokenLastUsed :exec
UPDATE auth_tokens SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '60 seconds');

-- name: ListTokensByUser :many
SELECT id, user_id, name, kind, token_hash, scopes, expires_at, last_used_at, created_at, revoked_at
FROM auth_tokens WHERE user_id = $1 ORDER BY created_at DESC;

-- name: CreatePermissionTarget :one
INSERT INTO permission_targets (name, repository_filter, path_pattern, actions)
VALUES ($1, $2, $3, $4)
RETURNING id, created_at, updated_at;

-- name: GetPermissionTargetByID :one
SELECT id, name, repository_filter, path_pattern, actions, created_at, updated_at
FROM permission_targets WHERE id = $1;

-- name: GetPermissionTargetByName :one
SELECT id, name, repository_filter, path_pattern, actions, created_at, updated_at
FROM permission_targets WHERE name = $1;

-- name: UpdatePermissionTarget :exec
UPDATE permission_targets SET name = $2, repository_filter = $3, path_pattern = $4, actions = $5 WHERE id = $1;

-- name: DeletePermissionTarget :exec
DELETE FROM permission_targets WHERE id = $1;

-- name: ListPermissionTargets :many
SELECT id, name, repository_filter, path_pattern, actions, created_at, updated_at
FROM permission_targets ORDER BY name ASC;

-- name: CreatePermissionAssignment :exec
INSERT INTO permission_assignments (permission_target_id, principal_type, principal_id)
VALUES ($1, $2, $3)
ON CONFLICT (permission_target_id, principal_type, principal_id) DO NOTHING;

-- name: DeletePermissionAssignment :exec
DELETE FROM permission_assignments
WHERE permission_target_id = sqlc.arg(permission_target_id)::uuid AND principal_type = sqlc.arg(principal_type) AND principal_id = sqlc.arg(principal_id)::uuid;

-- name: GetPermissionTargetsForPrincipal :many
SELECT DISTINCT pt.id, pt.name, pt.repository_filter, pt.path_pattern, pt.actions, pt.created_at, pt.updated_at
FROM permission_targets pt
JOIN permission_assignments pa ON pa.permission_target_id = pt.id
WHERE (pa.principal_type = 'user' AND pa.principal_id = sqlc.arg(principal_id)::uuid)
   OR (pa.principal_type = 'group' AND pa.principal_id = ANY(sqlc.arg(group_ids)::uuid[]))
ORDER BY pt.name ASC;
