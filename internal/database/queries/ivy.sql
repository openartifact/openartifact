-- Ivy on the generic package tables (mapping in pkg_ivy.go): P per
-- organisation:module, V per revision, F per file (path = the rendered
-- organisation/module/revision/filename).

-- name: GetIvyArtifact :one
SELECT f.id, f.repository_id,
       (p.attrs->>'organisation')::text AS organisation, (p.attrs->>'module')::text AS module,
       v.version AS revision,
       (f.attrs->>'artifact_name')::text AS artifact_name,
       COALESCE(f.attrs->>'artifact_type', 'jar')::text AS artifact_type,
       (f.attrs->>'artifact_ext')::text AS artifact_ext,
       b.blob_key, b.size_bytes, b.sha256, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = sqlc.arg(repository_id) AND f.path = sqlc.arg(path) AND f.deleted_at IS NULL;

-- name: IvyModuleExists :one
-- Whether the repository holds a live version of the module: a group asks it to
-- tell a module its hosted members own from one only a proxy has.
SELECT EXISTS (
    SELECT 1 FROM packages p
    JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
    WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key)::text
);
