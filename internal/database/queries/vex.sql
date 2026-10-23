-- name: UpsertVEXStatement :one
INSERT INTO vex_statements (repository_id, artifact_id, vuln_id, status, justification,
                            impact_statement, action_statement, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (repository_id, artifact_id, vuln_id) DO UPDATE SET
    status = EXCLUDED.status,
    justification = EXCLUDED.justification,
    impact_statement = EXCLUDED.impact_statement,
    action_statement = EXCLUDED.action_statement,
    created_by = EXCLUDED.created_by,
    updated_at = now()
RETURNING id, repository_id, artifact_id, vuln_id, status, justification,
          impact_statement, action_statement, created_by, created_at, updated_at;

-- name: ListVEXStatements :many
SELECT id, repository_id, artifact_id, vuln_id, status, justification,
       impact_statement, action_statement, created_by, created_at, updated_at
FROM vex_statements
WHERE repository_id = $1 AND (sqlc.arg(artifact_id)::text = '' OR artifact_id = sqlc.arg(artifact_id)::text)
ORDER BY artifact_id, vuln_id;

-- name: DeleteVEXStatement :execrows
DELETE FROM vex_statements WHERE repository_id = $1 AND id = $2;
