-- name: ListDeclarativeObjects :many
SELECT kind, name, object_id, applied_hash, applied_at
FROM declarative_objects
ORDER BY kind ASC, name ASC;

-- name: UpsertDeclarativeObject :one
INSERT INTO declarative_objects (kind, name, object_id, applied_hash)
VALUES ($1, $2, $3, $4)
ON CONFLICT (kind, name) DO UPDATE
SET object_id = EXCLUDED.object_id,
    applied_hash = EXCLUDED.applied_hash,
    applied_at = now()
RETURNING kind, name, object_id, applied_hash, applied_at;

-- name: DeleteDeclarativeObject :execrows
DELETE FROM declarative_objects WHERE kind = $1 AND name = $2;

-- name: ListPermissionAssignmentsForTarget :many
SELECT pa.principal_type, pa.principal_id::text AS principal_id,
       COALESCE(u.username, g.name, '')::text AS principal_name
FROM permission_assignments pa
LEFT JOIN auth_users u ON pa.principal_type = 'user' AND u.id = pa.principal_id
LEFT JOIN auth_groups g ON pa.principal_type = 'group' AND g.id = pa.principal_id
WHERE pa.permission_target_id = $1
ORDER BY pa.principal_type ASC, COALESCE(u.username, g.name, '') ASC;
