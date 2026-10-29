-- name: GetRPMRepo :one
SELECT id, repository_id, base_arch, metadata_stale,
       created_at, updated_at
FROM rpm_repos
WHERE repository_id = $1;

-- name: CreateRPMRepo :one
INSERT INTO rpm_repos (repository_id, base_arch)
VALUES ($1, $2)
RETURNING id, created_at, updated_at;

-- name: MarkRepoStale :exec
UPDATE rpm_repos SET metadata_stale = true WHERE repository_id = $1;

-- name: GetRepoMetadataByPath :one
SELECT id, repository_id, path, blob_sha256, blob_size, last_generated
FROM repository_metadata
WHERE repository_id = $1 AND path = $2;

-- name: GetRepoMetadataByRepo :many
SELECT id, repository_id, path, blob_sha256, blob_size, last_generated
FROM repository_metadata
WHERE repository_id = $1;

-- name: UpsertRepoMetadata :one
INSERT INTO repository_metadata (repository_id, path, blob_sha256, blob_size)
VALUES ($1, $2, $3, $4)
ON CONFLICT (repository_id, path) DO UPDATE
    SET blob_sha256 = $3, blob_size = $4, last_generated = now()
RETURNING id, last_generated;

-- RPM packages on the generic tables (see pkg_rpm.go for the mapping).

-- name: InsertRpmFile :exec
INSERT INTO rpm_files (id, header_start, header_end, rpm_extra)
VALUES ($1, $2, $3, sqlc.arg(rpm_extra)::jsonb);

-- name: GetRpmPackageByID :one
SELECT f.id, f.repository_id, f.blob_id,
       p.name, v.version,
       COALESCE(f.attrs ->> 'release', '')::text AS release,
       COALESCE((f.attrs ->> 'epoch')::int, 0)::int AS epoch,
       f.arch,
       COALESCE(v.metadata ->> 'summary', '')::text AS summary,
       COALESCE(v.metadata ->> 'description', '')::text AS description,
       COALESCE(v.metadata ->> 'license', '')::text AS license,
       COALESCE(v.metadata ->> 'url', '')::text AS url,
       COALESCE(v.metadata ->> 'vendor', '')::text AS vendor,
       COALESCE(v.metadata ->> 'group_name', '')::text AS group_name,
       COALESCE(v.metadata ->> 'packager', '')::text AS packager,
       COALESCE(v.metadata ->> 'source_rpm', '')::text AS source_rpm,
       COALESCE(v.metadata ->> 'build_host', '')::text AS build_host,
       COALESCE((f.attrs ->> 'build_time')::bigint, 0)::bigint AS build_time,
       f.filename,
       COALESCE(f.size_bytes, 0)::bigint AS package_size,
       COALESCE((f.attrs ->> 'installed_size')::bigint, 0)::bigint AS installed_size,
       COALESCE((f.attrs ->> 'archive_size')::bigint, 0)::bigint AS archive_size,
       f.created_at, f.updated_at
FROM package_files f
JOIN rpm_files r ON r.id = f.id
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
WHERE f.id = $1 AND f.deleted_at IS NULL;

-- A live version of the NEVRA.arch: the package by its (repository, name_key)
-- unique index, then the live version key.
-- name: RpmPackageExists :one
SELECT EXISTS (
    SELECT 1 FROM packages p
    JOIN package_versions v ON v.package_id = p.id
    WHERE p.repository_id = $1 AND p.name_key = $2 AND v.version_key = $3 AND v.deleted_at IS NULL
)::boolean;

-- Replaces rpm_extra of the live packages of a repository backed by the blob
-- with the given sha256. Extra data that carries sizes (sizes_parsed) corrects
-- installed_size/archive_size of the file too: rows stored before the header
-- size tags were read properly hold 0 (a zero size is an absent key).
-- name: UpdateRpmPackageExtra :exec
WITH target AS (
    SELECT f.id FROM package_files f
    WHERE f.repository_id = sqlc.arg(repository_id) AND f.deleted_at IS NULL
      AND f.blob_id IN (SELECT b.id FROM blobs b WHERE b.sha256 = sqlc.arg(sha256))
), side AS (
    UPDATE rpm_files r SET rpm_extra = sqlc.arg(extra)::jsonb
    FROM target t WHERE r.id = t.id
    RETURNING r.id
)
UPDATE package_files f
SET attrs = (f.attrs - 'installed_size' - 'archive_size') || jsonb_strip_nulls(jsonb_build_object(
        'installed_size', NULLIF(COALESCE((sqlc.arg(extra)::jsonb ->> 'installed_size')::bigint, 0), 0),
        'archive_size', NULLIF(COALESCE((sqlc.arg(extra)::jsonb ->> 'archive_size')::bigint, 0), 0)))
FROM target t
WHERE f.id = t.id AND (sqlc.arg(extra)::jsonb ->> 'sizes_parsed') = 'true';
