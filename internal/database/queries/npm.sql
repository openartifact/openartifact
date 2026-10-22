-- name: CreateNPMPackage :one
INSERT INTO npm_packages (repository_id, scope, name, full_name, latest_version, packument_stale)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (repository_id, full_name) DO UPDATE SET updated_at = NOW()
RETURNING id, created_at, updated_at;

-- name: GetNPMPackageByName :one
-- A package with no live version (all trashed) does not exist.
SELECT id, repository_id,
       COALESCE(scope, '') AS scope,
       name, full_name,
       COALESCE(latest_version, '') AS latest_version,
       packument_stale, created_at, updated_at
FROM npm_packages
WHERE repository_id = $1 AND full_name = $2
  AND EXISTS (SELECT 1 FROM npm_versions v WHERE v.package_id = npm_packages.id AND v.deleted_at IS NULL);

-- name: DeleteNPMPackage :exec
-- Only removes a package with no version row left at all, so the FK cascade
-- never takes trashed versions with it.
DELETE FROM npm_packages
WHERE npm_packages.id = $1
  AND NOT EXISTS (SELECT 1 FROM npm_versions v WHERE v.package_id = npm_packages.id);

-- name: MarkPackumentStale :exec
UPDATE npm_packages SET packument_stale = true, updated_at = NOW() WHERE id = $1;

-- name: UpdateNPMPackageLatestVersion :exec
UPDATE npm_packages SET latest_version = $2, packument_stale = false, updated_at = NOW() WHERE id = $1;

-- name: CreateNPMVersion :one
INSERT INTO npm_versions (package_id, version, manifest_json, integrity, shasum, blob_id, tarball_filename)
VALUES ($1, $2, $3::jsonb, $4, $5, $6, $7)
RETURNING id, created_at;

-- name: GetNPMVersion :one
SELECT id, package_id, version, manifest_json, integrity, shasum, blob_id, tarball_filename, created_at
FROM npm_versions
WHERE package_id = $1 AND version = $2 AND deleted_at IS NULL;

-- name: GetNPMVersionsByPackage :many
SELECT id, package_id, version, manifest_json, integrity, shasum, blob_id, tarball_filename, created_at
FROM npm_versions
WHERE package_id = $1 AND deleted_at IS NULL;

-- name: GetNPMVersionStates :many
-- What the latest-version computation needs of every live version: its number
-- and whether its manifest carries a deprecation message. The manifest itself
-- stays in the database, so regenerating a package with hundreds of versions
-- does not ship megabytes of manifests to the worker.
SELECT version,
       COALESCE(jsonb_typeof(manifest_json -> 'deprecated') = 'string'
                AND manifest_json ->> 'deprecated' <> '', false)::boolean AS deprecated
FROM npm_versions
WHERE package_id = $1 AND deleted_at IS NULL;

-- name: UpdateNPMVersionManifest :exec
UPDATE npm_versions SET manifest_json = $3::jsonb WHERE package_id = $1 AND version = $2 AND deleted_at IS NULL;

-- name: DeleteNPMVersion :exec
DELETE FROM npm_versions WHERE package_id = $1 AND version = $2 AND deleted_at IS NULL;

-- name: UpsertNPMDistTag :one
INSERT INTO npm_dist_tags (package_id, tag, version)
VALUES ($1, $2, $3)
ON CONFLICT (package_id, tag) DO UPDATE SET version = EXCLUDED.version, updated_at = NOW()
RETURNING id, updated_at;

-- name: GetNPMDistTags :many
-- Tags pointing at a trashed version are hidden but kept, so a restore brings
-- them back.
SELECT d.tag, d.version FROM npm_dist_tags d
WHERE d.package_id = $1
  AND EXISTS (SELECT 1 FROM npm_versions v
              WHERE v.package_id = d.package_id AND v.version = d.version AND v.deleted_at IS NULL);

-- name: DeleteNPMDistTag :exec
DELETE FROM npm_dist_tags WHERE package_id = $1 AND tag = $2;

-- name: ListAllNPMPackages :many
SELECT id, repository_id,
       COALESCE(scope, '') AS scope,
       name, full_name,
       COALESCE(latest_version, '') AS latest_version,
       packument_stale, created_at, updated_at
FROM npm_packages
WHERE repository_id = $1
  AND EXISTS (SELECT 1 FROM npm_versions v WHERE v.package_id = npm_packages.id AND v.deleted_at IS NULL);

-- name: ListNPMVersionsByRepo :many
SELECT nv.id, nv.package_id, nv.version, nv.manifest_json, nv.integrity, nv.shasum,
       b.sha256 AS blob_sha256, b.size_bytes AS blob_size_bytes,
       nv.tarball_filename, nv.created_at
FROM npm_versions nv
JOIN npm_packages np ON np.id = nv.package_id
JOIN blobs b ON b.id = nv.blob_id
WHERE np.repository_id = $1 AND nv.deleted_at IS NULL;
