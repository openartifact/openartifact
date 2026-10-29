-- sbt on the generic package tables (mapping in pkg_sbt.go): P per
-- group_id:artifact_id, V per version, F per file (path = the rendered
-- group/artifact/version/filename, arch = the scala version).

-- name: GetSBTArtifact :one
SELECT f.id, f.repository_id,
       (p.attrs->>'group_id')::text AS group_id, (p.attrs->>'artifact_id')::text AS artifact_id,
       v.version, f.arch AS scala_version, f.filename,
       b.blob_key, b.size_bytes, b.sha256, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = sqlc.arg(repository_id) AND f.path = sqlc.arg(path) AND f.deleted_at IS NULL;

-- name: SBTModuleExists :one
-- Whether the repository holds a live version of the artifact: a group asks it to
-- tell an artifact its hosted members own from one only a proxy has.
SELECT EXISTS (
    SELECT 1 FROM packages p
    JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
    WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key)::text
);
