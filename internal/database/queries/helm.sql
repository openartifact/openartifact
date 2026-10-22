-- name: CreateHelmChart :one
INSERT INTO helm_charts (repository_id, name, version, app_version, description, keywords, home, sources, maintainers, icon, api_version, type, blob_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING id, created_at, updated_at;

-- name: GetHelmChart :one
SELECT hc.id, hc.repository_id, hc.name, hc.version, hc.app_version, hc.description, hc.keywords, hc.home, hc.sources, hc.maintainers, hc.icon, hc.api_version, hc.type, b.blob_key, b.sha256 AS digest, b.size_bytes, hc.created_at, hc.updated_at
FROM helm_charts hc
JOIN blobs b ON b.id = hc.blob_id
WHERE hc.repository_id = $1 AND hc.name = $2 AND hc.version = $3 AND hc.deleted_at IS NULL;

-- name: DeleteHelmChart :exec
DELETE FROM helm_charts WHERE repository_id = $1 AND name = $2 AND version = $3 AND deleted_at IS NULL;

-- name: ListHelmCharts :many
SELECT hc.id, hc.repository_id, hc.name, hc.version, hc.app_version, hc.description, hc.keywords, hc.home, hc.sources, hc.maintainers, hc.icon, hc.api_version, hc.type, b.blob_key, b.sha256 AS digest, b.size_bytes, hc.created_at, hc.updated_at
FROM helm_charts hc
JOIN blobs b ON b.id = hc.blob_id
WHERE hc.repository_id = $1 AND hc.deleted_at IS NULL
ORDER BY hc.name, hc.version DESC;

-- name: ListHelmChartVersions :many
SELECT hc.id, hc.repository_id, hc.name, hc.version, hc.app_version, hc.description, hc.keywords, hc.home, hc.sources, hc.maintainers, hc.icon, hc.api_version, hc.type, b.blob_key, b.sha256 AS digest, b.size_bytes, hc.created_at, hc.updated_at
FROM helm_charts hc
JOIN blobs b ON b.id = hc.blob_id
WHERE hc.repository_id = $1 AND hc.name = $2 AND hc.deleted_at IS NULL
ORDER BY hc.version DESC;

-- name: HelmIndexStamp :one
-- Fingerprint of the live charts of a repository: their count and an
-- order-independent hash of their IDs. Chart rows are immutable (a republish
-- is a new row) and trash/restore changes membership, so the stamp changes
-- exactly when index.yaml would. It reads one narrow index range instead of
-- every chart joined to its blob.
SELECT (count(*)::text || ':' || COALESCE(sum(hashtextextended(hc.id::text, 0)), 0)::text)::text AS stamp
FROM helm_charts hc
WHERE hc.repository_id = $1 AND hc.deleted_at IS NULL;
