-- name: CreateCleanupPolicy :one
INSERT INTO cleanup_policies (repository_id, project_id, name, enabled, keep_last_n, keep_since_days, keep_downloaded_since_days, name_pattern, schedule_cron)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, created_at, updated_at;

-- name: GetCleanupPolicy :one
SELECT id, repository_id, name, enabled, keep_last_n, keep_since_days, keep_downloaded_since_days, name_pattern, schedule_cron, last_run_at, last_run_deleted, last_run_error, created_at, updated_at, project_id
FROM cleanup_policies WHERE id = $1;

-- name: UpdateCleanupPolicy :one
UPDATE cleanup_policies
SET name = $2, enabled = $3, keep_last_n = $4, keep_since_days = $5,
    keep_downloaded_since_days = $6, name_pattern = $7, schedule_cron = $8, updated_at = now()
WHERE id = $1
RETURNING updated_at;

-- name: DeleteCleanupPolicy :exec
DELETE FROM cleanup_policies WHERE id = $1;

-- name: ListCleanupPolicies :many
SELECT id, repository_id, name, enabled, keep_last_n, keep_since_days, keep_downloaded_since_days, name_pattern, schedule_cron, last_run_at, last_run_deleted, last_run_error, created_at, updated_at, project_id
FROM cleanup_policies WHERE repository_id = $1 ORDER BY created_at DESC, id;

-- name: ListEnabledCleanupPolicies :many
SELECT cp.id, cp.repository_id, cp.name, cp.enabled, cp.keep_last_n, cp.keep_since_days, cp.keep_downloaded_since_days, cp.name_pattern, cp.schedule_cron, cp.last_run_at, cp.last_run_deleted, cp.last_run_error, cp.created_at, cp.updated_at, cp.project_id
FROM cleanup_policies cp
LEFT JOIN repositories r ON r.id = cp.repository_id
WHERE cp.enabled = true AND (cp.project_id IS NOT NULL OR r.deleted_at IS NULL) ORDER BY cp.created_at DESC, cp.id;

-- name: UpdateCleanupPolicyRunResult :exec
UPDATE cleanup_policies SET last_run_at = now(), last_run_deleted = $2, last_run_error = $3 WHERE id = $1;

-- name: ListProjectCleanupPolicies :many
SELECT id, repository_id, name, enabled, keep_last_n, keep_since_days, keep_downloaded_since_days, name_pattern, schedule_cron, last_run_at, last_run_deleted, last_run_error, created_at, updated_at, project_id
FROM cleanup_policies WHERE project_id = $1 ORDER BY created_at DESC, id;

-- name: ListProjectCleanupRepositories :many
-- Live hosted repositories of the project that a project default policy
-- applies to: those with no enabled repository-level policy of their own.
SELECT r.id FROM repositories r
WHERE r.project_id = $1 AND r.deleted_at IS NULL AND r.repo_type = 'hosted'
  AND NOT EXISTS (SELECT 1 FROM cleanup_policies cp WHERE cp.repository_id = r.id AND cp.enabled)
ORDER BY r.name;
