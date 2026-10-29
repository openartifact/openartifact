-- npm on the generic package tables; the mapping is documented in
-- internal/database/pkg_npm.go. Every statement that changes what the packument
-- shows also bumps packages.updated_at (the packument's ETag and render-cache
-- key) in the same statement, so no writer has to remember MarkPackumentStale
-- for the validator to move.

-- name: CreateNPMPackage :one
INSERT INTO packages (repository_id, format, name, name_key, attrs)
VALUES (sqlc.arg(repository_id), 'npm', sqlc.arg(full_name)::text, sqlc.arg(full_name)::text,
        jsonb_build_object('scope', sqlc.arg(scope)::text, 'name', sqlc.arg(name)::text,
                           'latest_version', sqlc.arg(latest_version)::text,
                           'packument_stale', sqlc.arg(packument_stale)::boolean))
ON CONFLICT (repository_id, name_key) DO UPDATE SET updated_at = now()
RETURNING id, created_at, updated_at;

-- name: GetNPMPackageByName :one
-- A package with no live version (all trashed) does not exist.
SELECT p.id, p.repository_id,
       COALESCE(p.attrs ->> 'scope', '')::text AS scope,
       COALESCE(p.attrs ->> 'name', '')::text AS name,
       p.name AS full_name,
       COALESCE(p.attrs ->> 'latest_version', '')::text AS latest_version,
       COALESCE((p.attrs ->> 'packument_stale')::boolean, true)::boolean AS packument_stale,
       p.created_at, p.updated_at
FROM packages p
WHERE p.repository_id = $1 AND p.name_key = $2
  AND EXISTS (SELECT 1 FROM package_versions v WHERE v.package_id = p.id AND v.deleted_at IS NULL);

-- name: GetNPMPackageIdentity :one
SELECT repository_id, name FROM packages WHERE id = $1;

-- name: DeleteNPMPackage :exec
-- Only removes a package with no version row left at all (trashed ones
-- included), so the cascade never takes trashed versions with it.
DELETE FROM packages p
WHERE p.id = $1
  AND NOT EXISTS (SELECT 1 FROM package_versions v WHERE v.package_id = p.id);

-- name: MarkPackumentStale :exec
UPDATE packages SET attrs = attrs || '{"packument_stale": true}'::jsonb, updated_at = now() WHERE id = $1;

-- name: UpdateNPMPackageLatestVersion :exec
UPDATE packages
SET attrs = attrs || jsonb_build_object('latest_version', sqlc.arg(latest_version)::text, 'packument_stale', false),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: GetNPMVersion :one
SELECT f.id, v.package_id, v.version,
       COALESCE(v.metadata -> 'manifest_json', '{}'::jsonb)::jsonb AS manifest_json,
       COALESCE(f.checksums ->> 'integrity', '')::text AS integrity,
       COALESCE(f.checksums ->> 'shasum', '')::text AS shasum,
       COALESCE(f.blob_id::text, '')::text AS blob_id,
       f.filename AS tarball_filename,
       COALESCE(b.sha256, '')::text AS blob_sha256,
       COALESCE(b.size_bytes, 0)::bigint AS blob_size_bytes,
       v.created_at
FROM package_versions v
JOIN package_files f ON f.version_id = v.id AND f.role = 'primary' AND f.deleted_at IS NULL
LEFT JOIN blobs b ON b.id = f.blob_id
WHERE v.package_id = $1 AND v.version_key = $2 AND v.deleted_at IS NULL;

-- name: GetNPMVersionsByPackage :many
SELECT f.id, v.package_id, v.version,
       COALESCE(v.metadata -> 'manifest_json', '{}'::jsonb)::jsonb AS manifest_json,
       COALESCE(f.checksums ->> 'integrity', '')::text AS integrity,
       COALESCE(f.checksums ->> 'shasum', '')::text AS shasum,
       COALESCE(f.blob_id::text, '')::text AS blob_id,
       f.filename AS tarball_filename,
       COALESCE(b.sha256, '')::text AS blob_sha256,
       COALESCE(b.size_bytes, 0)::bigint AS blob_size_bytes,
       v.created_at
FROM package_versions v
JOIN package_files f ON f.version_id = v.id AND f.role = 'primary' AND f.deleted_at IS NULL
LEFT JOIN blobs b ON b.id = f.blob_id
WHERE v.package_id = $1 AND v.deleted_at IS NULL;

-- name: GetNPMVersionStates :many
-- What the latest-version computation needs of every live version: its number
-- and whether its manifest carries a deprecation message. The manifest itself
-- stays in the database, so regenerating a package with hundreds of versions
-- does not ship megabytes of manifests to the worker.
SELECT v.version,
       COALESCE(jsonb_typeof(v.metadata -> 'manifest_json' -> 'deprecated') = 'string'
                AND v.metadata -> 'manifest_json' ->> 'deprecated' <> '', false)::boolean AS deprecated
FROM package_versions v
WHERE v.package_id = $1 AND v.deleted_at IS NULL;

-- name: UpdateNPMVersionManifest :exec
WITH changed AS (
    UPDATE package_versions
    SET metadata = jsonb_set(metadata, '{manifest_json}', sqlc.arg(manifest)::jsonb)
    WHERE package_id = sqlc.arg(package_id) AND version_key = sqlc.arg(version) AND deleted_at IS NULL
    RETURNING package_id
)
UPDATE packages SET updated_at = now() WHERE id IN (SELECT package_id FROM changed);

-- name: DeleteNPMLiveVersionFiles :many
-- Hard delete of the file of the live version (the quota trigger releases its
-- usage); the version itself goes in PruneEmptyVersions.
DELETE FROM package_files f
USING package_versions v
WHERE f.version_id = v.id AND v.package_id = $1 AND v.version_key = $2 AND v.deleted_at IS NULL
RETURNING f.version_id;

-- name: DeleteNPMTagsOfVersion :exec
-- Tags are keyed by the version string, so they do not follow the version row.
-- A trashed incarnation of the same version keeps them (a restore brings them
-- back).
DELETE FROM package_tags t
WHERE t.package_id = $1 AND t.version = $2
  AND NOT EXISTS (SELECT 1 FROM package_versions v WHERE v.package_id = t.package_id AND v.version_key = t.version);

-- name: UpsertNPMDistTag :one
WITH touch AS (
    UPDATE packages SET updated_at = now() WHERE id = sqlc.arg(package_id)
)
INSERT INTO package_tags (package_id, tag, version, unit_key)
SELECT p.id, sqlc.arg(tag)::text, sqlc.arg(version)::text, p.name || '#' || sqlc.arg(tag)::text
FROM packages p WHERE p.id = sqlc.arg(package_id)
ON CONFLICT (package_id, tag) DO UPDATE SET version = EXCLUDED.version, updated_at = now()
RETURNING id, updated_at;

-- name: GetNPMDistTags :many
-- Tags pointing at a trashed version are hidden but kept, so a restore brings
-- them back.
SELECT t.tag, t.version FROM package_tags t
WHERE t.package_id = $1
  AND EXISTS (SELECT 1 FROM package_versions v
              WHERE v.package_id = t.package_id AND v.version_key = t.version AND v.deleted_at IS NULL);

-- name: DeleteNPMDistTag :exec
WITH removed AS (
    DELETE FROM package_tags WHERE package_id = $1 AND tag = $2
    RETURNING package_id
)
UPDATE packages SET updated_at = now() WHERE id IN (SELECT package_id FROM removed);
