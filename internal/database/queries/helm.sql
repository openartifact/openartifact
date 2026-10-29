-- Helm charts on the generic package tables (mapping spec in pkg_helm.go). One chart
-- version is one package_versions row with one package_files row.

-- name: GetHelmChart :one
SELECT f.id, f.repository_id, p.name, v.version, v.metadata, b.blob_key, b.sha256 AS digest, b.size_bytes, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.version_key = sqlc.arg(version) AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name);

-- The live files of one chart version, for DeleteHelmChart.
-- name: ListHelmChartFileIDs :many
SELECT f.id
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.version_key = sqlc.arg(version) AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name);

-- name: ListHelmCharts :many
SELECT f.id, f.repository_id, p.name, v.version, v.metadata, b.blob_key, b.sha256 AS digest, b.size_bytes, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL
ORDER BY p.name, v.version DESC;

-- name: HelmIndexStamp :one
-- Fingerprint of the live charts of a repository: their count and an
-- order-independent hash of their file IDs. A chart's file is immutable (a
-- republish is a new file) and trash/restore changes membership, so the stamp
-- changes exactly when index.yaml would. It reads one narrow index range
-- (idx_package_files_listing) instead of every chart joined to its blob.
SELECT (count(*)::text || ':' || COALESCE(sum(hashtextextended(f.id::text, 0)), 0)::text)::text AS stamp
FROM package_files f
WHERE f.repository_id = $1 AND f.deleted_at IS NULL;
