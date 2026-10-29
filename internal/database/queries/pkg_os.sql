-- Helpers shared by the deb, rpm, apk and pacman stores.

-- Hard delete of live files (the quota triggers release their usage). The
-- repository and the format list are optional guards: a NULL repository
-- matches any, an empty format list any format. Trashed files are not touched.
-- The versions and packages they leave empty are pruned by the caller.
-- name: DeleteLivePackageFiles :many
DELETE FROM package_files f
WHERE f.id = ANY(sqlc.arg(ids)::uuid[])
  AND f.deleted_at IS NULL
  AND (sqlc.narg(repository_id)::uuid IS NULL OR f.repository_id = sqlc.narg(repository_id)::uuid)
  AND (cardinality(sqlc.arg(formats)::text[]) = 0 OR EXISTS (
        SELECT 1 FROM package_versions v JOIN packages p ON p.id = v.package_id
        WHERE v.id = f.version_id AND p.format = ANY(sqlc.arg(formats)::text[])))
RETURNING f.id, f.version_id;
