-- Generic package tables (migration 098, docs/design/format-plugins.md). These
-- are the write primitives every format store composes in one transaction;
-- format-specific reads live in the per-format query files.

-- The package of a repository by its identity. Every write to a package bumps
-- updated_at (the npm packument ETag), and the attrs of a later write are
-- merged over the stored ones.
-- name: UpsertPackage :one
INSERT INTO packages AS p (repository_id, format, name, name_key, attrs)
VALUES (sqlc.arg(repository_id), sqlc.arg(format), sqlc.arg(name), sqlc.arg(name_key), sqlc.arg(attrs)::jsonb)
ON CONFLICT (repository_id, name_key) DO UPDATE
   SET attrs = p.attrs || EXCLUDED.attrs, updated_at = now()
RETURNING p.id;

-- The live version of a package by its version_key.
-- name: GetLiveVersionID :one
SELECT id FROM package_versions WHERE package_id = $1 AND version_key = $2 AND deleted_at IS NULL;

-- A new live version; no row comes back when a concurrent writer created it
-- first.
-- name: InsertVersion :one
INSERT INTO package_versions (package_id, repository_id, version, version_key, metadata, unit_key, created_at)
VALUES (sqlc.arg(package_id), sqlc.arg(repository_id), sqlc.arg(version), sqlc.arg(version_key),
        sqlc.arg(metadata)::jsonb, sqlc.narg(unit_key), COALESCE(sqlc.narg(created_at)::timestamptz, now()))
ON CONFLICT (package_id, version_key) WHERE deleted_at IS NULL DO NOTHING
RETURNING id;

-- name: SetVersionMetadata :exec
UPDATE package_versions SET metadata = sqlc.arg(metadata)::jsonb, unit_key = sqlc.narg(unit_key), updated_at = now()
WHERE id = sqlc.arg(id);

-- name: VersionHasLiveFiles :one
SELECT EXISTS (SELECT 1 FROM package_files WHERE version_id = $1 AND deleted_at IS NULL)::boolean;

-- One file of a version. A live file with the same path in the repository is
-- a duplicate: no row comes back.
-- name: InsertPackageFile :one
INSERT INTO package_files (version_id, repository_id, path, filename, blob_id, size_bytes, role, listed, arch, part,
                           attrs, checksums, unit_key, download_count, created_at)
VALUES (sqlc.arg(version_id), sqlc.arg(repository_id), sqlc.arg(path), sqlc.arg(filename), sqlc.narg(blob_id),
        sqlc.narg(size_bytes), sqlc.arg(role), sqlc.arg(listed), sqlc.arg(arch), sqlc.arg(part),
        sqlc.arg(attrs)::jsonb, sqlc.arg(checksums)::jsonb, sqlc.narg(unit_key), sqlc.arg(download_count),
        COALESCE(sqlc.narg(created_at)::timestamptz, now()))
ON CONFLICT (repository_id, path) WHERE deleted_at IS NULL DO NOTHING
RETURNING id, created_at, updated_at;

-- Hard delete of files (the quota trigger releases their usage). The versions
-- they leave empty are pruned by PruneEmptyVersions.
-- name: DeletePackageFilesByID :many
DELETE FROM package_files WHERE repository_id = sqlc.arg(repository_id) AND id = ANY(sqlc.arg(ids)::uuid[])
RETURNING id, version_id, blob_id;

-- name: PruneEmptyVersions :many
DELETE FROM package_versions v
WHERE v.id = ANY(sqlc.arg(ids)::uuid[])
  AND NOT EXISTS (SELECT 1 FROM package_files f WHERE f.version_id = v.id)
RETURNING v.package_id;

-- name: PruneEmptyPackages :exec
DELETE FROM packages p
WHERE p.id = ANY(sqlc.arg(ids)::uuid[])
  AND NOT EXISTS (SELECT 1 FROM package_versions v WHERE v.package_id = p.id);

-- name: IncrementFileDownloads :exec
UPDATE package_files SET download_count = download_count + 1, updated_at = now() WHERE id = $1;

-- The packument-style validator of a package: every write that changes what a
-- package page shows bumps it.
-- name: TouchPackage :exec
UPDATE packages SET updated_at = now() WHERE id = $1;
