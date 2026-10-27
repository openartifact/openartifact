-- name: CreateCondaPackage :one
INSERT INTO conda_packages (repository_id, name, version, build_string, platform, depends, blob_id, extension, build_number, md5, timestamp_ms, index_extra, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, COALESCE(sqlc.narg(created_at)::timestamptz, NOW()))
ON CONFLICT (repository_id, name, version, build_string, platform, extension) WHERE deleted_at IS NULL DO NOTHING
RETURNING id, created_at, updated_at;

-- name: GetCondaPackageFile :one
SELECT cp.id, cp.repository_id, cp.name, cp.version, cp.build_string, cp.platform, cp.depends, cp.extension, cp.build_number, cp.md5, cp.timestamp_ms, cp.index_extra, b.blob_key, b.size_bytes, b.sha256, cp.created_at, cp.updated_at
FROM conda_packages cp
JOIN blobs b ON b.id = cp.blob_id
WHERE cp.repository_id = $1 AND cp.name = $2 AND cp.version = $3 AND cp.build_string = $4 AND cp.platform = $5 AND cp.extension = $6 AND cp.deleted_at IS NULL;

-- name: DeleteCondaPackage :exec
DELETE FROM conda_packages WHERE id = $1 AND deleted_at IS NULL;

-- name: ListCondaPackages :many
SELECT cp.id, cp.repository_id, cp.name, cp.version, cp.build_string, cp.platform, cp.depends, cp.extension, cp.build_number, cp.md5, cp.timestamp_ms, cp.index_extra, b.blob_key, b.size_bytes, b.sha256, cp.created_at, cp.updated_at
FROM conda_packages cp
JOIN blobs b ON b.id = cp.blob_id
WHERE cp.repository_id = $1 AND cp.platform = $2 AND cp.deleted_at IS NULL ORDER BY cp.name ASC, cp.version DESC, cp.build_string ASC, cp.extension ASC;

-- name: ListAllCondaPackages :many
SELECT cp.id, cp.repository_id, cp.name, cp.version, cp.build_string, cp.platform, cp.depends, cp.extension, cp.build_number, cp.md5, cp.timestamp_ms, cp.index_extra, b.blob_key, b.size_bytes, b.sha256, cp.created_at, cp.updated_at
FROM conda_packages cp
JOIN blobs b ON b.id = cp.blob_id
WHERE cp.repository_id = $1 AND cp.deleted_at IS NULL ORDER BY cp.name ASC, cp.version DESC, cp.platform ASC, cp.build_string ASC, cp.extension ASC;

-- name: CondaRepodataStamp :one
-- Fingerprint of the live packages of one platform (see HelmIndexStamp):
-- package rows are immutable, so count plus an ID hash tracks repodata.json.
SELECT (count(*)::text || ':' || COALESCE(sum(hashtextextended(cp.id::text, 0)), 0)::text)::text AS stamp
FROM conda_packages cp
WHERE cp.repository_id = $1 AND cp.platform = $2 AND cp.deleted_at IS NULL;

-- name: ListCondaPackageNames :many
-- Distinct names of the live packages of a repository, in any platform, for a
-- group's hosted-wins rule (compared case-insensitively, as lowercase names).
SELECT DISTINCT lower(cp.name)::text AS name
FROM conda_packages cp
WHERE cp.repository_id = $1 AND cp.deleted_at IS NULL
ORDER BY 1;

-- name: HasCondaPackageName :one
SELECT EXISTS (
    SELECT 1 FROM conda_packages cp
    WHERE cp.repository_id = $1 AND lower(cp.name) = lower(sqlc.arg(name)::text) AND cp.deleted_at IS NULL
)::boolean AS present;
