-- name: UpsertAIAnalysis :exec
WITH ref AS (
    INSERT INTO artifact_refs (repository_id, artifact_path, format)
    VALUES ($1, $2, COALESCE($3, (SELECT format FROM repositories WHERE id = $1), 'unknown'))
    ON CONFLICT (repository_id, artifact_path) DO UPDATE SET artifact_path = EXCLUDED.artifact_path
    RETURNING id
), job AS (
    INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, error_message)
    SELECT ref.id, 'ai', '', $4, NULLIF($5, '') FROM ref
    ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET
        status = EXCLUDED.status, error_message = EXCLUDED.error_message, updated_at = now()
    RETURNING id
)
INSERT INTO ai_results (scan_job_id, summary, tags, license_status, license_name, security_notes, quarantine_recommended, model_used, prompt_tokens, completion_tokens)
SELECT job.id, $6, $7, $8, $9, $10, $11, $12, $13, $14 FROM job
ON CONFLICT (scan_job_id) DO UPDATE SET
    summary = EXCLUDED.summary, tags = EXCLUDED.tags, license_status = EXCLUDED.license_status,
    license_name = EXCLUDED.license_name, security_notes = EXCLUDED.security_notes,
    quarantine_recommended = EXCLUDED.quarantine_recommended, model_used = EXCLUDED.model_used,
    prompt_tokens = EXCLUDED.prompt_tokens, completion_tokens = EXCLUDED.completion_tokens;

-- name: GetAIAnalysis :one
SELECT air.id, ref.repository_id, ref.artifact_path, ref.format,
       air.summary, air.tags, air.license_status, air.license_name, air.security_notes,
       air.quarantine_recommended, sj.status, sj.error_message, air.model_used,
       air.prompt_tokens, air.completion_tokens, sj.created_at, sj.updated_at
FROM ai_results air
JOIN scan_jobs sj ON sj.id = air.scan_job_id
JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
WHERE sj.scan_type = 'ai' AND ref.repository_id = $1 AND ref.artifact_path = $2;

-- name: ListAIAnalyses :many
SELECT air.id, ref.repository_id, ref.artifact_path, ref.format,
       air.summary, air.tags, air.license_status, air.license_name, air.security_notes,
       air.quarantine_recommended, sj.status, sj.error_message, air.model_used,
       air.prompt_tokens, air.completion_tokens, sj.created_at, sj.updated_at
FROM ai_results air
JOIN scan_jobs sj ON sj.id = air.scan_job_id
JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
WHERE sj.scan_type = 'ai' AND ref.repository_id = $1
ORDER BY sj.updated_at DESC LIMIT $2 OFFSET $3;
