-- name: CreateRubyGem :one
INSERT INTO rubygems_gems (repository_id, name, version, platform, description, summary, homepage, authors, licenses, dependencies, metadata_json, blob_id, yanked, download_count, size, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, COALESCE(sqlc.narg(created_at)::timestamptz, NOW()))
RETURNING id, created_at, updated_at;

-- name: GetRubyGem :one
SELECT rg.id, rg.repository_id, rg.name, rg.version, rg.platform, rg.description, rg.summary, rg.homepage, rg.authors, rg.licenses, rg.dependencies, rg.metadata_json, b.blob_key, b.sha256 AS checksum_sha256, rg.yanked, rg.download_count, rg.size, rg.created_at, rg.updated_at
FROM rubygems_gems rg
JOIN blobs b ON b.id = rg.blob_id
WHERE rg.repository_id = $1 AND rg.deleted_at IS NULL AND rg.name = $2 AND rg.version = $3 AND rg.platform = $4;

-- name: DeleteRubyGem :exec
DELETE FROM rubygems_gems WHERE id = $1 AND deleted_at IS NULL;

-- name: ListRubyGems :many
SELECT rg.id, rg.repository_id, rg.name, rg.version, rg.platform, rg.description, rg.summary, rg.homepage, rg.authors, rg.licenses, rg.dependencies, rg.metadata_json, b.blob_key, b.sha256 AS checksum_sha256, rg.yanked, rg.download_count, rg.size, rg.created_at, rg.updated_at
FROM rubygems_gems rg
JOIN blobs b ON b.id = rg.blob_id
WHERE rg.repository_id = $1 AND rg.deleted_at IS NULL ORDER BY rg.name ASC, rg.version DESC;

-- name: ListRubyGemVersions :many
SELECT rg.id, rg.repository_id, rg.name, rg.version, rg.platform, rg.description, rg.summary, rg.homepage, rg.authors, rg.licenses, rg.dependencies, rg.metadata_json, b.blob_key, b.sha256 AS checksum_sha256, rg.yanked, rg.download_count, rg.size, rg.created_at, rg.updated_at
FROM rubygems_gems rg
JOIN blobs b ON b.id = rg.blob_id
WHERE rg.repository_id = $1 AND rg.deleted_at IS NULL AND rg.name = $2 ORDER BY rg.version DESC;

-- name: SearchRubyGems :many
SELECT rg.id, rg.repository_id, rg.name, rg.version, rg.platform, rg.description, rg.summary, rg.homepage, rg.authors, rg.licenses, rg.dependencies, rg.metadata_json, b.blob_key, b.sha256 AS checksum_sha256, rg.yanked, rg.download_count, rg.size, rg.created_at, rg.updated_at
FROM rubygems_gems rg
JOIN blobs b ON b.id = rg.blob_id
WHERE rg.repository_id = $1 AND rg.deleted_at IS NULL AND (rg.name ILIKE $2 OR rg.description ILIKE $2 OR rg.summary ILIKE $2)
ORDER BY rg.name ASC, rg.version DESC
LIMIT 1000;

-- name: YankRubyGem :exec
UPDATE rubygems_gems SET yanked = $1, updated_at = NOW() WHERE repository_id = $2 AND name = $3 AND version = $4 AND ($5::text = '' OR platform = $5) AND deleted_at IS NULL;

-- name: RubyGemsSpecsStamp :one
-- Fingerprint of the listed (not yanked) gems of a repository: every specs
-- index is derived from exactly this set, and a yank removes a row from it.
SELECT (count(*)::text || ':' || COALESCE(sum(hashtextextended(rg.id::text, 0)), 0)::text)::text AS stamp
FROM rubygems_gems rg
WHERE rg.repository_id = $1 AND rg.deleted_at IS NULL AND NOT rg.yanked;

-- name: ListRubyGemSpecs :many
-- The three columns the specs indexes are built from, for listed gems only.
SELECT rg.name, rg.version, rg.platform
FROM rubygems_gems rg
WHERE rg.repository_id = $1 AND rg.deleted_at IS NULL AND NOT rg.yanked
ORDER BY rg.name ASC, rg.version DESC;
