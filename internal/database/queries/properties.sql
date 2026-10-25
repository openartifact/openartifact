-- name: DeleteArtifactProperty :exec
DELETE FROM artifact_properties ap
USING artifact_refs ar
WHERE ap.artifact_ref_id = ar.id AND ap.repository_id = $1 AND ar.artifact_path = $2 AND ap.key = $3;

-- name: ListArtifactProperties :many
SELECT ap.key, ap.value FROM artifact_properties ap
JOIN artifact_refs ar ON ar.id = ap.artifact_ref_id
WHERE ap.repository_id = $1 AND ar.artifact_path = $2 ORDER BY ap.key;

-- name: SearchByProperty :many
SELECT DISTINCT ar.artifact_path AS artifact_id FROM artifact_properties ap
JOIN artifact_refs ar ON ar.id = ap.artifact_ref_id
WHERE ap.repository_id = $1 AND ap.key = $2 AND ap.value = $3 ORDER BY ar.artifact_path;
