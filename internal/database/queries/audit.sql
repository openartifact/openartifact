-- name: InsertAuditLog :exec
-- The audit writer passes the id and created_at it stamped when the event was
-- recorded, so the row and the OTLP audit event carry the same values; a
-- caller that leaves them NULL gets the column defaults.
INSERT INTO audit_logs (
    id, created_at, action, actor_id, actor_type, details_json,
    resource_type, resource_id, repository_id, project_id, ip_address
)
VALUES (
    COALESCE(sqlc.narg(id)::uuid, gen_random_uuid()),
    COALESCE(sqlc.narg(created_at)::timestamptz, CURRENT_TIMESTAMP),
    @action, @actor_id, @actor_type, @details_json,
    @resource_type, @resource_id, @repository_id, @project_id, @ip_address
);

-- name: PurgeAuditLogsBefore :execrows
-- Deletes the oldest audit entries created before @cutoff, at most @batch of
-- them (the retention worker calls it until it returns fewer). Oldest first
-- by the (created_at, id) keyset, so idx_audit_logs_created_at drives the
-- scan. SKIP LOCKED keeps a row another transaction is touching out of the
-- batch instead of stalling on it. Rows without created_at are never purged.
WITH doomed AS (
    SELECT id FROM audit_logs
    WHERE created_at < @cutoff::timestamptz
    ORDER BY created_at, id
    LIMIT @batch::int
    FOR UPDATE SKIP LOCKED
)
DELETE FROM audit_logs a
USING doomed d
WHERE a.id = d.id;

-- name: ResolveAuditNames :many
-- Display names of the entities one audit page refers to, one primary-key
-- lookup arm per kind so a page costs a single round trip however many rows it
-- has. Entities that no longer exist simply yield no row.
SELECT 'user'::text AS kind, id::text AS id, username::text AS name
FROM auth_users WHERE id = ANY(sqlc.arg(user_ids)::uuid[])
UNION ALL
SELECT 'group', id::text, name::text FROM auth_groups WHERE id = ANY(sqlc.arg(group_ids)::uuid[])
UNION ALL
SELECT 'repository', id::text, name::text FROM repositories WHERE id = ANY(sqlc.arg(repository_ids)::uuid[])
UNION ALL
SELECT 'project', id::text, name::text FROM projects WHERE id = ANY(sqlc.arg(project_ids)::uuid[])
UNION ALL
SELECT 'permission_target', id::text, name::text FROM permission_targets WHERE id = ANY(sqlc.arg(permission_target_ids)::uuid[])
UNION ALL
SELECT 'gpg_key', id::text, name::text FROM gpg_keys WHERE id = ANY(sqlc.arg(gpg_key_ids)::uuid[])
UNION ALL
SELECT 'sso_provider', id::text, slug::text FROM auth_sso_providers WHERE id = ANY(sqlc.arg(sso_provider_ids)::uuid[])
UNION ALL
SELECT 'bundle', id::text, (name || ' ' || version)::text FROM release_bundles WHERE id = ANY(sqlc.arg(bundle_ids)::uuid[])
UNION ALL
SELECT 'token', id::text, name::text FROM auth_tokens WHERE id = ANY(sqlc.arg(token_ids)::uuid[]);
