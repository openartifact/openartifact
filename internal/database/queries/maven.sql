-- Maven on the generic package tables (migrations/001_baseline.sql; mapping
-- in pkg_maven.go). Every read joins package_files (the file, by path or
-- through its version), maven_files (coordinates and digests), the version,
-- the package (group and artifact in attrs) and the blob (sha256 and size).

-- name: GetMavenArtifactByPath :one
SELECT f.id, f.repository_id, b.id AS blob_id,
       (p.attrs->>'group_id')::text AS group_id, (p.attrs->>'artifact_id')::text AS artifact_id_coord,
       v.version, m.classifier, m.extension,
       m.snapshot_timestamp, m.snapshot_build_number, m.is_snapshot, f.path AS repo_path,
       f.created_at, f.updated_at,
       m.md5, m.sha1, m.sha512, b.sha256, b.size_bytes AS blob_size_bytes
FROM package_files f
JOIN maven_files m ON m.id = f.id
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = sqlc.arg(repository_id) AND f.path = sqlc.arg(repo_path) AND f.deleted_at IS NULL;

-- All the live files of one group:artifact:version (every snapshot build of a
-- snapshot version is a version of its own, so all of them).
-- name: GetMavenArtifactsByGAV :many
SELECT f.id, f.repository_id, b.id AS blob_id,
       (p.attrs->>'group_id')::text AS group_id, (p.attrs->>'artifact_id')::text AS artifact_id_coord,
       v.version, m.classifier, m.extension,
       m.snapshot_timestamp, m.snapshot_build_number, m.is_snapshot, f.path AS repo_path,
       f.created_at, f.updated_at,
       m.md5, m.sha1, m.sha512, b.sha256, b.size_bytes AS blob_size_bytes
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN maven_files m ON m.id = f.id
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key)::text
  AND v.version = sqlc.arg(version);

-- name: ListMavenGARefs :many
-- Everything maven-metadata.xml regeneration needs for one group:artifact,
-- without the per-row ids, paths and checksum join of the full artifact load:
-- one row per distinct live version (snapshot_row = false) plus one row per
-- file of every -SNAPSHOT version (snapshot_row = true).
SELECT v.version::text AS version,
       ''::text AS classifier,
       ''::text AS extension,
       ''::text AS snapshot_timestamp,
       0::int AS snapshot_build_number,
       false AS snapshot_row
FROM (
    SELECT DISTINCT pv.version
    FROM packages p
    JOIN package_versions pv ON pv.package_id = p.id AND pv.deleted_at IS NULL
    WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key)::text
) v
UNION ALL
SELECT sv.version::text, m.classifier::text, m.extension::text,
       COALESCE(m.snapshot_timestamp, '')::text, m.snapshot_build_number::int, true
FROM packages sp
JOIN package_versions sv ON sv.package_id = sp.id AND sv.deleted_at IS NULL
JOIN package_files sf ON sf.version_id = sv.id AND sf.deleted_at IS NULL
JOIN maven_files m ON m.id = sf.id
WHERE sp.repository_id = sqlc.arg(repository_id) AND sp.name_key = sqlc.arg(name_key)::text
  AND sv.version LIKE '%-SNAPSHOT'
ORDER BY snapshot_row, version, classifier, extension, snapshot_timestamp, snapshot_build_number;

-- name: GetLatestSnapshot :one
SELECT f.id, f.repository_id, b.id AS blob_id,
       (p.attrs->>'group_id')::text AS group_id, (p.attrs->>'artifact_id')::text AS artifact_id_coord,
       v.version, m.classifier, m.extension,
       m.snapshot_timestamp, m.snapshot_build_number, m.is_snapshot, f.path AS repo_path,
       f.created_at, f.updated_at,
       m.md5, m.sha1, m.sha512, b.sha256, b.size_bytes AS blob_size_bytes
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN maven_files m ON m.id = f.id
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key)::text
  AND v.version = sqlc.arg(version) AND m.is_snapshot
ORDER BY m.snapshot_timestamp DESC, m.snapshot_build_number DESC
LIMIT 1;

-- name: GetLatestSnapshotFile :one
-- The newest timestamped snapshot build of one file (classifier + extension),
-- for the legacy "foo-1.0-SNAPSHOT.<ext>" lookup.
SELECT f.id, f.repository_id, b.id AS blob_id,
       (p.attrs->>'group_id')::text AS group_id, (p.attrs->>'artifact_id')::text AS artifact_id_coord,
       v.version, m.classifier, m.extension,
       m.snapshot_timestamp, m.snapshot_build_number, m.is_snapshot, f.path AS repo_path,
       f.created_at, f.updated_at,
       m.md5, m.sha1, m.sha512, b.sha256, b.size_bytes AS blob_size_bytes
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN maven_files m ON m.id = f.id
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name_key)::text
  AND v.version = sqlc.arg(version)
  AND m.classifier = sqlc.arg(classifier) AND m.extension = sqlc.arg(extension)
  AND m.is_snapshot
ORDER BY m.snapshot_timestamp DESC NULLS LAST, m.snapshot_build_number DESC
LIMIT 1;

-- name: ListMavenArtifactsByRepo :many
SELECT f.id, f.repository_id, b.id AS blob_id,
       (p.attrs->>'group_id')::text AS group_id, (p.attrs->>'artifact_id')::text AS artifact_id_coord,
       v.version, m.classifier, m.extension,
       m.snapshot_timestamp, m.snapshot_build_number, m.is_snapshot, f.path AS repo_path,
       f.created_at, f.updated_at,
       m.md5, m.sha1, m.sha512, b.sha256, b.size_bytes AS blob_size_bytes
FROM package_files f
JOIN maven_files m ON m.id = f.id
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL;

-- The coordinates and digests of a stored file. On an overwrite of an existing
-- path only the digests change (the coordinates are part of the path).
-- name: UpsertMavenFile :exec
INSERT INTO maven_files (id, classifier, extension, snapshot_timestamp, snapshot_build_number, is_snapshot, md5, sha1, sha512)
VALUES (sqlc.arg(id), sqlc.arg(classifier), sqlc.arg(extension), sqlc.narg(snapshot_timestamp), sqlc.arg(snapshot_build_number),
        sqlc.arg(is_snapshot), sqlc.arg(md5), sqlc.arg(sha1), sqlc.arg(sha512))
ON CONFLICT (id) DO UPDATE SET md5 = EXCLUDED.md5, sha1 = EXCLUDED.sha1, sha512 = EXCLUDED.sha512;

-- Repoints a live file at new content (the overwrite of an upload to an
-- existing path: maven snapshots, ivy and sbt republish). No row comes back
-- when the path has no live file.
-- name: OverwritePackageFileByPath :one
UPDATE package_files
   SET blob_id = sqlc.narg(blob_id), attrs = sqlc.arg(attrs)::jsonb, checksums = sqlc.arg(checksums)::jsonb, updated_at = now()
 WHERE repository_id = sqlc.arg(repository_id) AND path = sqlc.arg(path) AND deleted_at IS NULL
RETURNING id, created_at, updated_at;

-- Hard delete of one live file by id (the unpublish of maven, ivy and sbt);
-- the version it leaves empty is pruned by the caller.
-- name: DeleteLivePackageFile :one
DELETE FROM package_files WHERE id = $1 AND deleted_at IS NULL
RETURNING version_id;
