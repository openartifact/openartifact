-- name: ListCurationRules :many
SELECT id, repository_id, action, package_pattern, version_pattern, reason, created_by, created_at
FROM curation_rules WHERE repository_id = $1 ORDER BY created_at, id;

-- name: GetCurationRule :one
SELECT id, repository_id, action, package_pattern, version_pattern, reason, created_by, created_at
FROM curation_rules WHERE repository_id = $1 AND id = $2;

-- name: CountCurationRules :one
SELECT COUNT(*) FROM curation_rules WHERE repository_id = $1;

-- name: CreateCurationRule :one
INSERT INTO curation_rules (repository_id, action, package_pattern, version_pattern, reason, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, repository_id, action, package_pattern, version_pattern, reason, created_by, created_at;

-- name: UpdateCurationRule :one
UPDATE curation_rules
SET action = $3, package_pattern = $4, version_pattern = $5, reason = $6, updated_at = now()
WHERE repository_id = $1 AND id = $2
RETURNING id, repository_id, action, package_pattern, version_pattern, reason, created_by, created_at;

-- name: DeleteCurationRule :execrows
DELETE FROM curation_rules WHERE repository_id = $1 AND id = $2;
