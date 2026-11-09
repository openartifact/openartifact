-- name: UpsertVEXStatement :one
-- Records the decision for a target, replacing whatever was there (an operator
-- decision, or an accepted peer suggestion, overrides every source). A feed or
-- peer that no longer exists (a restored backup) leaves the reference NULL.
INSERT INTO vex_statements (repository_id, artifact_id, vuln_id, status, justification,
                            impact_statement, action_statement, created_by,
                            source_kind, source_name, source_ref, feed_id, peer_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
        (SELECT f.id FROM vex_feeds f WHERE f.id = sqlc.narg(feed_id)::uuid),
        (SELECT p.id FROM federation_peers p WHERE p.id = sqlc.narg(peer_id)::uuid))
ON CONFLICT (repository_id, artifact_id, vuln_id) DO UPDATE SET
    status = EXCLUDED.status,
    justification = EXCLUDED.justification,
    impact_statement = EXCLUDED.impact_statement,
    action_statement = EXCLUDED.action_statement,
    created_by = EXCLUDED.created_by,
    source_kind = EXCLUDED.source_kind,
    source_name = EXCLUDED.source_name,
    source_ref = EXCLUDED.source_ref,
    feed_id = EXCLUDED.feed_id,
    peer_id = EXCLUDED.peer_id,
    updated_at = now()
RETURNING *;

-- name: ApplyPeerVEXStatement :execrows
-- A federated peer's decision applied automatically: inserted, or updated only
-- when the row is already that peer's; a statement of any other source stays.
INSERT INTO vex_statements (repository_id, artifact_id, vuln_id, status, justification,
                            impact_statement, action_statement, created_by,
                            source_kind, source_name, source_ref, peer_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'peer', $9, $10, sqlc.arg(peer_id)::uuid)
ON CONFLICT (repository_id, artifact_id, vuln_id) DO UPDATE SET
    status = EXCLUDED.status,
    justification = EXCLUDED.justification,
    impact_statement = EXCLUDED.impact_statement,
    action_statement = EXCLUDED.action_statement,
    source_name = EXCLUDED.source_name,
    source_ref = EXCLUDED.source_ref,
    updated_at = now()
WHERE vex_statements.source_kind = 'peer' AND vex_statements.peer_id = EXCLUDED.peer_id;

-- name: DeleteFeedVEXStatements :exec
DELETE FROM vex_statements WHERE feed_id = sqlc.arg(feed_id)::uuid;

-- name: InsertFeedVEXStatements :execrows
-- The statements a feed materialized; a target that already has a statement (of
-- any source) keeps it. Targets of a repository deleted meanwhile are skipped.
INSERT INTO vex_statements (repository_id, artifact_id, vuln_id, status, justification,
                            impact_statement, action_statement, source_kind, source_name, source_ref, feed_id)
SELECT u.repository_id, u.artifact_id, u.vuln_id, u.status, u.justification,
       u.impact_statement, u.action_statement, 'csaf', u.source_name, u.source_ref, sqlc.arg(feed_id)::uuid
FROM (SELECT unnest(sqlc.arg(repository_ids)::uuid[]) AS repository_id,
             unnest(sqlc.arg(artifact_ids)::text[]) AS artifact_id,
             unnest(sqlc.arg(vuln_ids)::text[]) AS vuln_id,
             unnest(sqlc.arg(statuses)::text[]) AS status,
             unnest(sqlc.arg(justifications)::text[]) AS justification,
             unnest(sqlc.arg(impact_statements)::text[]) AS impact_statement,
             unnest(sqlc.arg(action_statements)::text[]) AS action_statement,
             unnest(sqlc.arg(source_names)::text[]) AS source_name,
             unnest(sqlc.arg(source_refs)::text[]) AS source_ref) u
WHERE EXISTS (SELECT 1 FROM repositories r WHERE r.id = u.repository_id)
ON CONFLICT (repository_id, artifact_id, vuln_id) DO NOTHING;

-- name: ListVEXStatements :many
SELECT * FROM vex_statements
WHERE repository_id = $1 AND (sqlc.arg(artifact_id)::text = '' OR artifact_id = sqlc.arg(artifact_id)::text)
ORDER BY artifact_id, vuln_id;

-- name: DeleteVEXStatement :execrows
DELETE FROM vex_statements WHERE repository_id = $1 AND id = $2;

-- name: ListVEXTargets :many
-- The latest vulnerability scan of every artifact (of one repository, or of all
-- when repository_id is ''), when it finished and found something.
SELECT ref.repository_id, r.format::text AS repository_format, ref.artifact_path AS artifact_id, vr.vulnerabilities
FROM scan_jobs sj
JOIN vuln_results vr ON vr.scan_job_id = sj.id
JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
JOIN repositories r ON r.id = ref.repository_id AND r.deleted_at IS NULL
WHERE sj.scan_type = 'vuln' AND sj.is_latest AND sj.status IN ('completed', 'partial')
  AND vr.vuln_count > 0 AND jsonb_typeof(vr.vulnerabilities) = 'array'
  AND (sqlc.arg(repository_id)::text = '' OR ref.repository_id = NULLIF(sqlc.arg(repository_id)::text, '')::uuid)
ORDER BY ref.repository_id, ref.artifact_path;

-- name: UpsertVEXSuggestion :one
-- Stores a peer's decision. An unchanged decision returns no row; a changed one
-- goes back to pending.
INSERT INTO vex_suggestions (repository_id, peer_id, artifact_id, vuln_id, status, justification,
                             impact_statement, action_statement, peer_updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (peer_id, artifact_id, vuln_id) DO UPDATE SET
    repository_id = EXCLUDED.repository_id,
    status = EXCLUDED.status,
    justification = EXCLUDED.justification,
    impact_statement = EXCLUDED.impact_statement,
    action_statement = EXCLUDED.action_statement,
    peer_updated_at = EXCLUDED.peer_updated_at,
    state = 'pending',
    decided_by = '',
    decided_at = NULL,
    updated_at = now()
WHERE (vex_suggestions.repository_id, vex_suggestions.status, vex_suggestions.justification,
       vex_suggestions.impact_statement, vex_suggestions.action_statement)
      IS DISTINCT FROM
      (EXCLUDED.repository_id, EXCLUDED.status, EXCLUDED.justification,
       EXCLUDED.impact_statement, EXCLUDED.action_statement)
RETURNING id, state;

-- name: GetVEXSuggestionKey :one
SELECT id, state FROM vex_suggestions WHERE peer_id = $1 AND artifact_id = $2 AND vuln_id = $3;

-- name: PruneVEXSuggestions :execrows
DELETE FROM vex_suggestions WHERE peer_id = $1 AND NOT (id = ANY(sqlc.arg(keep_ids)::uuid[]));

-- name: DeleteVEXSuggestionsOfPeer :execrows
DELETE FROM vex_suggestions WHERE peer_id = $1;

-- name: ListVEXSuggestions :many
SELECT sqlc.embed(s), p.name AS peer_name, p.base_url AS peer_base_url,
       COALESCE(v.status, '')::text AS local_status, COALESCE(v.source_kind, '')::text AS local_source_kind
FROM vex_suggestions s
JOIN federation_peers p ON p.id = s.peer_id
LEFT JOIN vex_statements v ON v.repository_id = s.repository_id AND v.artifact_id = s.artifact_id AND v.vuln_id = s.vuln_id
WHERE s.repository_id = $1 AND (sqlc.arg(state)::text = '' OR s.state = sqlc.arg(state)::text)
ORDER BY s.artifact_id, s.vuln_id, p.name;

-- name: GetVEXSuggestion :one
SELECT sqlc.embed(s), p.name AS peer_name, p.base_url AS peer_base_url,
       COALESCE(v.status, '')::text AS local_status, COALESCE(v.source_kind, '')::text AS local_source_kind
FROM vex_suggestions s
JOIN federation_peers p ON p.id = s.peer_id
LEFT JOIN vex_statements v ON v.repository_id = s.repository_id AND v.artifact_id = s.artifact_id AND v.vuln_id = s.vuln_id
WHERE s.repository_id = $1 AND s.id = $2;

-- name: SetVEXSuggestionState :execrows
UPDATE vex_suggestions SET
    state = sqlc.arg(state)::text,
    decided_by = CASE WHEN sqlc.arg(state)::text = 'pending' THEN '' ELSE sqlc.arg(decided_by)::text END,
    decided_at = CASE WHEN sqlc.arg(state)::text = 'pending' THEN NULL ELSE now() END,
    updated_at = now()
WHERE repository_id = $1 AND id = $2;
