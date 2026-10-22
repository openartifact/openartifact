-- name: CreateCargoCrate :one
INSERT INTO cargo_crates (repository_id, name, version, description, homepage, documentation, repository, license, keywords, categories, authors, readme, metadata_json, blob_id, yanked, download_count)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
RETURNING id, created_at, updated_at;

-- Crate names are case-insensitive: cargo lowercases them in index paths, so
-- every lookup by name compares lower(name). A row whose name matches exactly
-- wins when legacy data holds two crates that differ only in case.
-- name: GetCargoCrate :one
SELECT cc.id, cc.repository_id, cc.name, cc.version, cc.description, cc.homepage, cc.documentation, cc.repository, cc.license, cc.keywords, cc.categories, cc.authors, cc.readme, cc.metadata_json, b.blob_key, b.sha256 AS checksum, b.size_bytes, cc.yanked, cc.download_count, cc.created_at, cc.updated_at
FROM cargo_crates cc
JOIN blobs b ON b.id = cc.blob_id
WHERE cc.repository_id = sqlc.arg(repository_id) AND cc.deleted_at IS NULL AND lower(cc.name) = lower(sqlc.arg(name)::text) AND cc.version = sqlc.arg(version)
ORDER BY (cc.name = sqlc.arg(name)::text) DESC
LIMIT 1;

-- name: DeleteCargoCrate :exec
DELETE FROM cargo_crates WHERE id = $1 AND deleted_at IS NULL;

-- name: ListCargoCrateVersions :many
SELECT cc.id, cc.repository_id, cc.name, cc.version, cc.description, cc.homepage, cc.documentation, cc.repository, cc.license, cc.keywords, cc.categories, cc.authors, cc.readme, cc.metadata_json, b.blob_key, b.sha256 AS checksum, cc.yanked, cc.download_count, cc.created_at, cc.updated_at
FROM cargo_crates cc
JOIN blobs b ON b.id = cc.blob_id
WHERE cc.repository_id = sqlc.arg(repository_id) AND cc.deleted_at IS NULL AND lower(cc.name) = lower(sqlc.arg(name)::text)
ORDER BY cc.created_at, cc.id;

-- Per-version rows. Used by backup export (an empty query lists everything);
-- the search API uses SearchCargoCrateSummaries.
-- name: SearchCargoCrates :many
SELECT cc.id, cc.repository_id, cc.name, cc.version, cc.description, cc.homepage, cc.documentation, cc.repository, cc.license, cc.keywords, cc.categories, cc.authors, cc.readme, cc.metadata_json, b.blob_key, b.sha256 AS checksum, cc.yanked, cc.download_count, cc.created_at, cc.updated_at
FROM cargo_crates cc
JOIN blobs b ON b.id = cc.blob_id
WHERE cc.repository_id = $1 AND cc.deleted_at IS NULL AND (cc.name ILIKE $2 OR cc.description ILIKE $2)
ORDER BY cc.name ASC LIMIT $3;

-- One row per crate: versions are listed oldest first for the caller to rank
-- by semver; description comes from the newest version; total is the number
-- of matching crates, not of returned rows.
-- name: SearchCargoCrateSummaries :many
SELECT cc.name,
       COALESCE((ARRAY_AGG(cc.description ORDER BY cc.created_at DESC))[1], '')::text AS description,
       COALESCE(SUM(cc.download_count), 0)::bigint AS downloads,
       ARRAY_AGG(cc.version ORDER BY cc.created_at, cc.id)::text[] AS versions,
       (ARRAY_AGG(cc.version ORDER BY cc.created_at, cc.id) FILTER (WHERE NOT cc.yanked))::text[] AS live_versions,
       (COUNT(*) OVER ())::bigint AS total
FROM cargo_crates cc
WHERE cc.repository_id = $1 AND cc.deleted_at IS NULL AND (cc.name ILIKE $2 OR cc.description ILIKE $2)
GROUP BY cc.name
ORDER BY cc.name ASC
LIMIT $3;

-- name: YankCargoCrate :exec
UPDATE cargo_crates SET yanked = sqlc.arg(yanked), updated_at = NOW() WHERE repository_id = sqlc.arg(repository_id) AND lower(name) = lower(sqlc.arg(name)::text) AND version = sqlc.arg(version) AND deleted_at IS NULL;

-- name: IncrementCargoDownloadCount :exec
UPDATE cargo_crates SET download_count = download_count + 1, updated_at = NOW()
WHERE repository_id = sqlc.arg(repository_id) AND lower(name) = lower(sqlc.arg(name)::text) AND version = sqlc.arg(version) AND deleted_at IS NULL;

-- name: CargoIndexStamp :one
-- Fingerprint of the sparse index entry of one crate: its live versions and
-- their yanked flags, the only mutable state the entry shows. (updated_at is
-- no validator: every download bumps it.)
SELECT (count(*)::text || ':' || COALESCE(sum(hashtextextended(cc.id::text || cc.yanked::text, 0)), 0)::text)::text AS stamp
FROM cargo_crates cc
WHERE cc.repository_id = sqlc.arg(repository_id) AND cc.deleted_at IS NULL AND lower(cc.name) = lower(sqlc.arg(name)::text);

-- name: ListCargoIndexVersions :many
-- The columns a sparse index entry is built from (no readme, description or
-- authors).
SELECT cc.name, cc.version, cc.metadata_json, b.sha256 AS checksum, cc.yanked
FROM cargo_crates cc
JOIN blobs b ON b.id = cc.blob_id
WHERE cc.repository_id = sqlc.arg(repository_id) AND cc.deleted_at IS NULL AND lower(cc.name) = lower(sqlc.arg(name)::text)
ORDER BY cc.created_at, cc.id;
