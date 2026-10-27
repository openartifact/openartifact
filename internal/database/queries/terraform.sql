-- name: CreateTerraformModule :one
INSERT INTO terraform_modules (repository_id, name, system, version, archive_ext, blob_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, COALESCE(sqlc.narg(created_at)::timestamptz, NOW()))
RETURNING id, created_at, updated_at;

-- name: GetTerraformModule :one
SELECT tm.id, tm.repository_id, tm.name, tm.system, tm.version, tm.archive_ext, b.blob_key, b.sha256 AS checksum, b.size_bytes, tm.download_count, tm.created_at, tm.updated_at
FROM terraform_modules tm
JOIN blobs b ON b.id = tm.blob_id
WHERE tm.repository_id = $1 AND tm.name = $2 AND tm.system = $3 AND tm.version = $4 AND tm.deleted_at IS NULL;

-- name: ListTerraformModuleVersions :many
SELECT tm.id, tm.repository_id, tm.name, tm.system, tm.version, tm.archive_ext, b.blob_key, b.sha256 AS checksum, b.size_bytes, tm.download_count, tm.created_at, tm.updated_at
FROM terraform_modules tm
JOIN blobs b ON b.id = tm.blob_id
WHERE tm.repository_id = $1 AND tm.name = $2 AND tm.system = $3 AND tm.deleted_at IS NULL
ORDER BY tm.created_at, tm.id;

-- name: ListTerraformModules :many
SELECT tm.id, tm.repository_id, tm.name, tm.system, tm.version, tm.archive_ext, b.blob_key, b.sha256 AS checksum, b.size_bytes, tm.download_count, tm.created_at, tm.updated_at
FROM terraform_modules tm
JOIN blobs b ON b.id = tm.blob_id
WHERE tm.repository_id = $1 AND tm.deleted_at IS NULL
ORDER BY tm.name, tm.system, tm.created_at, tm.id;

-- name: DeleteTerraformModule :exec
DELETE FROM terraform_modules WHERE id = $1 AND repository_id = $2 AND deleted_at IS NULL;

-- name: IncrementTerraformModuleDownloadCount :exec
UPDATE terraform_modules SET download_count = download_count + 1
WHERE id = $1 AND repository_id = $2 AND deleted_at IS NULL;

-- name: TerraformModuleStamp :one
-- Fingerprint of the live versions of one module: their count and an
-- order-independent hash of their IDs. Rows are immutable apart from the
-- download counter (updated_at moves with every download, so it is no
-- validator), and trash/restore changes membership.
SELECT (count(*)::text || ':' || COALESCE(sum(hashtextextended(tm.id::text, 0)), 0)::text)::text AS stamp
FROM terraform_modules tm
WHERE tm.repository_id = $1 AND tm.name = $2 AND tm.system = $3 AND tm.deleted_at IS NULL;

-- name: CreateTerraformProvider :one
INSERT INTO terraform_providers (repository_id, type, version, os, arch, protocols, blob_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, COALESCE(sqlc.narg(created_at)::timestamptz, NOW()))
RETURNING id, created_at, updated_at;

-- name: GetTerraformProvider :one
SELECT tp.id, tp.repository_id, tp.type, tp.version, tp.os, tp.arch, tp.protocols, b.blob_key, b.sha256 AS checksum, b.size_bytes, tp.download_count, tp.created_at, tp.updated_at
FROM terraform_providers tp
JOIN blobs b ON b.id = tp.blob_id
WHERE tp.repository_id = $1 AND tp.type = $2 AND tp.version = $3 AND tp.os = $4 AND tp.arch = $5 AND tp.deleted_at IS NULL;

-- name: ListTerraformProviderPlatforms :many
-- Every live platform zip of one provider type, all versions.
SELECT tp.id, tp.repository_id, tp.type, tp.version, tp.os, tp.arch, tp.protocols, b.blob_key, b.sha256 AS checksum, b.size_bytes, tp.download_count, tp.created_at, tp.updated_at
FROM terraform_providers tp
JOIN blobs b ON b.id = tp.blob_id
WHERE tp.repository_id = $1 AND tp.type = $2 AND tp.deleted_at IS NULL
ORDER BY tp.version, tp.os, tp.arch;

-- name: ListTerraformProviderVersionPlatforms :many
-- Every live platform zip of one provider version.
SELECT tp.id, tp.repository_id, tp.type, tp.version, tp.os, tp.arch, tp.protocols, b.blob_key, b.sha256 AS checksum, b.size_bytes, tp.download_count, tp.created_at, tp.updated_at
FROM terraform_providers tp
JOIN blobs b ON b.id = tp.blob_id
WHERE tp.repository_id = $1 AND tp.type = $2 AND tp.version = $3 AND tp.deleted_at IS NULL
ORDER BY tp.os, tp.arch;

-- name: ListTerraformProviders :many
SELECT tp.id, tp.repository_id, tp.type, tp.version, tp.os, tp.arch, tp.protocols, b.blob_key, b.sha256 AS checksum, b.size_bytes, tp.download_count, tp.created_at, tp.updated_at
FROM terraform_providers tp
JOIN blobs b ON b.id = tp.blob_id
WHERE tp.repository_id = $1 AND tp.deleted_at IS NULL
ORDER BY tp.type, tp.version, tp.os, tp.arch;

-- name: DeleteTerraformProvider :exec
DELETE FROM terraform_providers WHERE id = $1 AND repository_id = $2 AND deleted_at IS NULL;

-- name: IncrementTerraformProviderDownloadCount :exec
UPDATE terraform_providers SET download_count = download_count + 1
WHERE id = $1 AND repository_id = $2 AND deleted_at IS NULL;

-- name: TerraformProviderStamp :one
-- Fingerprint of the live platforms of one provider type (see
-- TerraformModuleStamp).
SELECT (count(*)::text || ':' || COALESCE(sum(hashtextextended(tp.id::text, 0)), 0)::text)::text AS stamp
FROM terraform_providers tp
WHERE tp.repository_id = $1 AND tp.type = $2 AND tp.deleted_at IS NULL;
