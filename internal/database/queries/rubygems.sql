-- rubygems on the generic package tables (the mapping is documented on
-- RubyGemsStore in pkg_rubygems.go): one package per gem name, one version and
-- one .gem file per (version, platform). The writes go through writePackage;
-- these are the typed reads and updates.

-- name: GetRubyGem :one
SELECT f.id, f.repository_id, p.name, v.version, f.arch AS platform,
       COALESCE(v.metadata->>'description', '')::text AS description,
       COALESCE(v.metadata->>'summary', '')::text AS summary,
       COALESCE(v.metadata->>'homepage', '')::text AS homepage,
       COALESCE(v.metadata->'authors', '[]'::jsonb)::jsonb AS authors,
       COALESCE(v.metadata->'licenses', '[]'::jsonb)::jsonb AS licenses,
       COALESCE(v.metadata->'dependencies', '[]'::jsonb)::jsonb AS dependencies,
       COALESCE(v.metadata->'metadata_json', '{}'::jsonb)::jsonb AS metadata_json,
       b.blob_key, b.sha256 AS checksum_sha256,
       COALESCE((v.metadata->>'yanked')::boolean, false)::boolean AS yanked,
       f.download_count, COALESCE(f.size_bytes, 0)::bigint AS size, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.version_key = sqlc.arg(version_key) AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name);

-- name: ListRubyGems :many
SELECT f.id, f.repository_id, p.name, v.version, f.arch AS platform,
       COALESCE(v.metadata->>'description', '')::text AS description,
       COALESCE(v.metadata->>'summary', '')::text AS summary,
       COALESCE(v.metadata->>'homepage', '')::text AS homepage,
       COALESCE(v.metadata->'authors', '[]'::jsonb)::jsonb AS authors,
       COALESCE(v.metadata->'licenses', '[]'::jsonb)::jsonb AS licenses,
       COALESCE(v.metadata->'dependencies', '[]'::jsonb)::jsonb AS dependencies,
       COALESCE(v.metadata->'metadata_json', '{}'::jsonb)::jsonb AS metadata_json,
       b.blob_key, b.sha256 AS checksum_sha256,
       COALESCE((v.metadata->>'yanked')::boolean, false)::boolean AS yanked,
       f.download_count, COALESCE(f.size_bytes, 0)::bigint AS size, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL AND v.deleted_at IS NULL
ORDER BY p.name ASC, v.version DESC;

-- name: ListRubyGemVersions :many
SELECT f.id, f.repository_id, p.name, v.version, f.arch AS platform,
       COALESCE(v.metadata->>'description', '')::text AS description,
       COALESCE(v.metadata->>'summary', '')::text AS summary,
       COALESCE(v.metadata->>'homepage', '')::text AS homepage,
       COALESCE(v.metadata->'authors', '[]'::jsonb)::jsonb AS authors,
       COALESCE(v.metadata->'licenses', '[]'::jsonb)::jsonb AS licenses,
       COALESCE(v.metadata->'dependencies', '[]'::jsonb)::jsonb AS dependencies,
       COALESCE(v.metadata->'metadata_json', '{}'::jsonb)::jsonb AS metadata_json,
       b.blob_key, b.sha256 AS checksum_sha256,
       COALESCE((v.metadata->>'yanked')::boolean, false)::boolean AS yanked,
       f.download_count, COALESCE(f.size_bytes, 0)::bigint AS size, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name)
ORDER BY v.version DESC, f.arch;

-- name: SearchRubyGems :many
SELECT f.id, f.repository_id, p.name, v.version, f.arch AS platform,
       COALESCE(v.metadata->>'description', '')::text AS description,
       COALESCE(v.metadata->>'summary', '')::text AS summary,
       COALESCE(v.metadata->>'homepage', '')::text AS homepage,
       COALESCE(v.metadata->'authors', '[]'::jsonb)::jsonb AS authors,
       COALESCE(v.metadata->'licenses', '[]'::jsonb)::jsonb AS licenses,
       COALESCE(v.metadata->'dependencies', '[]'::jsonb)::jsonb AS dependencies,
       COALESCE(v.metadata->'metadata_json', '{}'::jsonb)::jsonb AS metadata_json,
       b.blob_key, b.sha256 AS checksum_sha256,
       COALESCE((v.metadata->>'yanked')::boolean, false)::boolean AS yanked,
       f.download_count, COALESCE(f.size_bytes, 0)::bigint AS size, f.created_at, f.updated_at
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL AND v.deleted_at IS NULL
  AND (p.name ILIKE $2 OR v.metadata->>'description' ILIKE $2 OR v.metadata->>'summary' ILIKE $2)
ORDER BY p.name ASC, v.version DESC
LIMIT 1000;

-- The yanked flag lives in the version metadata; a yank applies to the platform
-- builds of the version it names (every build when the platform is empty).
-- name: YankRubyGem :exec
UPDATE package_versions v
SET metadata = jsonb_set(v.metadata, '{yanked}', to_jsonb(sqlc.arg(yanked)::boolean)), updated_at = now()
FROM packages p
WHERE p.id = v.package_id AND p.repository_id = sqlc.arg(repository_id) AND p.name_key = sqlc.arg(name)
  AND v.version = sqlc.arg(version) AND v.deleted_at IS NULL
  AND (sqlc.arg(platform)::text = '' OR EXISTS (
        SELECT 1 FROM package_files f WHERE f.version_id = v.id AND f.arch = sqlc.arg(platform)::text AND f.deleted_at IS NULL));

-- name: RubyGemsSpecsStamp :one
-- Fingerprint of the listed (not yanked) gems of a repository: every specs
-- index is derived from exactly this set, and a yank removes a row from it.
SELECT (count(*)::text || ':' || COALESCE(sum(hashtextextended(f.id::text, 0)), 0)::text)::text AS stamp
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL AND f.listed AND v.deleted_at IS NULL
  AND NOT COALESCE((v.metadata->>'yanked')::boolean, false);

-- name: ListRubyGemSpecs :many
-- The three columns the specs indexes are built from, for listed gems only.
SELECT p.name, v.version, f.arch AS platform
FROM package_files f
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL AND f.listed AND v.deleted_at IS NULL
  AND NOT COALESCE((v.metadata->>'yanked')::boolean, false)
ORDER BY p.name ASC, v.version DESC;
