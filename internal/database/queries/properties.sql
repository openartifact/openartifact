-- name: DeleteArtifactProperty :exec
DELETE FROM artifact_properties ap
USING artifact_refs ar
WHERE ap.artifact_ref_id = ar.id AND ap.repository_id = $1 AND ar.artifact_path = $2 AND ap.key = $3;

-- name: ListArtifactProperties :many
SELECT ap.key, ap.value FROM artifact_properties ap
JOIN artifact_refs ar ON ar.id = ap.artifact_ref_id
WHERE ap.repository_id = $1 AND ar.artifact_path = $2 ORDER BY ap.key;

-- name: SearchByProperty :many
-- The ids of the artifacts whose property key equals value, each with the stored artifact
-- it names, if any. The web UI and oa store properties under the listing item id: the id
-- of a version's first file (package_files.id), or of the tag for a docker image
-- (docker_tags.id). An id is resolved against the live files and tags of the same
-- repository; PropertyStore.SearchByProperty names a tag as the package listing does. Only
-- text shaped like a UUID is cast, so external ids ("build-482") never reach the cast and
-- stay unresolved; the cast keeps the lookups on the primary keys.
SELECT m.artifact_id::text AS artifact_id,
       p.name AS package_name, pv.version AS package_version, p.format AS package_format,
       r.name AS tag_repository, dt.image AS tag_image, dt.name AS tag_name
FROM (
    SELECT DISTINCT ar.artifact_path AS artifact_id,
           CASE WHEN ar.artifact_path ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
                THEN ar.artifact_path::uuid END AS ref_id
    FROM artifact_properties ap
    JOIN artifact_refs ar ON ar.id = ap.artifact_ref_id
    WHERE ap.repository_id = @repository_id AND ap.key = @key AND ap.value = @value
) m
LEFT JOIN package_files pf ON pf.id = m.ref_id AND pf.repository_id = @repository_id AND pf.deleted_at IS NULL
LEFT JOIN package_versions pv ON pv.id = pf.version_id AND pv.deleted_at IS NULL
LEFT JOIN packages p ON p.id = pv.package_id
LEFT JOIN docker_tags dt ON dt.id = m.ref_id AND dt.repository_id = @repository_id AND dt.deleted_at IS NULL
LEFT JOIN repositories r ON r.id = dt.repository_id
ORDER BY m.artifact_id;
