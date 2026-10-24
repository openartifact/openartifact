-- name: InsertAuditLog :exec
INSERT INTO audit_logs (action, actor_id, actor_type, details_json, resource_type, resource_id, repository_id, project_id, ip_address)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

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
