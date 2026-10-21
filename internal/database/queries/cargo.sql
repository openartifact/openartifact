-- name: CreateCargoCrate :one
INSERT INTO cargo_crates (repository_id, name, version, description, homepage, documentation, repository, license, keywords, categories, authors, readme, metadata_json, blob_id, yanked, download_count)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
RETURNING id, created_at, updated_at;

-- name: GetCargoCrate :one
SELECT cc.id, cc.repository_id, cc.name, cc.version, cc.description, cc.homepage, cc.documentation, cc.repository, cc.license, cc.keywords, cc.categories, cc.authors, cc.readme, cc.metadata_json, b.blob_key, b.sha256 AS checksum, cc.yanked, cc.download_count, cc.created_at, cc.updated_at
FROM cargo_crates cc
JOIN blobs b ON b.id = cc.blob_id
WHERE cc.repository_id = $1 AND cc.deleted_at IS NULL AND cc.name = $2 AND cc.version = $3;

-- name: DeleteCargoCrate :exec
DELETE FROM cargo_crates WHERE id = $1 AND deleted_at IS NULL;

-- name: ListCargoCrateVersions :many
SELECT cc.id, cc.repository_id, cc.name, cc.version, cc.description, cc.homepage, cc.documentation, cc.repository, cc.license, cc.keywords, cc.categories, cc.authors, cc.readme, cc.metadata_json, b.blob_key, b.sha256 AS checksum, cc.yanked, cc.download_count, cc.created_at, cc.updated_at
FROM cargo_crates cc
JOIN blobs b ON b.id = cc.blob_id
WHERE cc.repository_id = $1 AND cc.deleted_at IS NULL AND cc.name = $2 ORDER BY cc.version DESC;

-- name: SearchCargoCrates :many
SELECT cc.id, cc.repository_id, cc.name, cc.version, cc.description, cc.homepage, cc.documentation, cc.repository, cc.license, cc.keywords, cc.categories, cc.authors, cc.readme, cc.metadata_json, b.blob_key, b.sha256 AS checksum, cc.yanked, cc.download_count, cc.created_at, cc.updated_at
FROM cargo_crates cc
JOIN blobs b ON b.id = cc.blob_id
WHERE cc.repository_id = $1 AND cc.deleted_at IS NULL AND (cc.name ILIKE $2 OR cc.description ILIKE $2)
ORDER BY cc.name ASC LIMIT $3;

-- name: YankCargoCrate :exec
UPDATE cargo_crates SET yanked = $1, updated_at = NOW() WHERE repository_id = $2 AND name = $3 AND version = $4 AND deleted_at IS NULL;

-- name: IncrementCargoDownloadCount :exec
UPDATE cargo_crates SET download_count = download_count + 1, updated_at = NOW()
WHERE repository_id = $1 AND name = $2 AND version = $3 AND deleted_at IS NULL;
