-- cargo on the generic package tables (the mapping is documented on CargoStore in
-- pkg_cargo.go): one package per lower-cased crate name, one version and one
-- .crate file per crate version. Crate names are case-insensitive: cargo
-- lowercases them in index paths, so packages.name_key is lower(name) and every
-- lookup by name compares it with lower($name).
--
-- The writes go through writePackage; these are the typed reads and updates.

-- One crate version of the repository: the shared column list of the Get and
-- List queries below. name is the spelling this version was published with.
-- name: GetCargoCrate :one
SELECT f.id, f.repository_id, COALESCE(v.metadata->>'name', p.name)::text AS name, v.version,
       COALESCE(v.metadata->>'description', '')::text AS description,
       COALESCE(v.metadata->>'homepage', '')::text AS homepage,
       COALESCE(v.metadata->>'documentation', '')::text AS documentation,
       COALESCE(v.metadata->>'repository', '')::text AS repository,
       COALESCE(v.metadata->>'license', '')::text AS license,
       COALESCE(v.metadata->'keywords', '[]'::jsonb)::jsonb AS keywords,
       COALESCE(v.metadata->'categories', '[]'::jsonb)::jsonb AS categories,
       COALESCE(v.metadata->'authors', '[]'::jsonb)::jsonb AS authors,
       COALESCE(v.metadata->>'readme', '')::text AS readme,
       COALESCE(v.metadata->'metadata_json', '{}'::jsonb)::jsonb AS metadata_json,
       b.blob_key, b.sha256 AS checksum, b.size_bytes,
       COALESCE((v.metadata->>'yanked')::boolean, false)::boolean AS yanked,
       f.download_count, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.version_key = sqlc.arg(version) AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = lower(sqlc.arg(name)::text);

-- name: ListCargoCrateVersions :many
SELECT f.id, f.repository_id, COALESCE(v.metadata->>'name', p.name)::text AS name, v.version,
       COALESCE(v.metadata->>'description', '')::text AS description,
       COALESCE(v.metadata->>'homepage', '')::text AS homepage,
       COALESCE(v.metadata->>'documentation', '')::text AS documentation,
       COALESCE(v.metadata->>'repository', '')::text AS repository,
       COALESCE(v.metadata->>'license', '')::text AS license,
       COALESCE(v.metadata->'keywords', '[]'::jsonb)::jsonb AS keywords,
       COALESCE(v.metadata->'categories', '[]'::jsonb)::jsonb AS categories,
       COALESCE(v.metadata->'authors', '[]'::jsonb)::jsonb AS authors,
       COALESCE(v.metadata->>'readme', '')::text AS readme,
       COALESCE(v.metadata->'metadata_json', '{}'::jsonb)::jsonb AS metadata_json,
       b.blob_key, b.sha256 AS checksum, b.size_bytes,
       COALESCE((v.metadata->>'yanked')::boolean, false)::boolean AS yanked,
       f.download_count, f.created_at, f.updated_at
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = lower(sqlc.arg(name)::text)
ORDER BY v.created_at, v.id;

-- One row per crate: versions are listed oldest first for the caller to rank
-- by semver; description comes from the newest version; total is the number
-- of matching crates, not of returned rows.
-- name: SearchCargoCrateSummaries :many
SELECT p.name::text AS name,
       COALESCE((ARRAY_AGG(v.metadata->>'description' ORDER BY v.created_at DESC))[1], '')::text AS description,
       COALESCE(SUM(f.download_count), 0)::bigint AS downloads,
       ARRAY_AGG(v.version ORDER BY v.created_at, v.id)::text[] AS versions,
       (ARRAY_AGG(v.version ORDER BY v.created_at, v.id)
          FILTER (WHERE NOT COALESCE((v.metadata->>'yanked')::boolean, false)))::text[] AS live_versions,
       (COUNT(*) OVER ())::bigint AS total
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
WHERE p.repository_id = $1 AND (p.name ILIKE $2 OR v.metadata->>'description' ILIKE $2)
GROUP BY p.id, p.name
ORDER BY p.name ASC
LIMIT $3;

-- The yanked flag lives in the version metadata; the version's updated_at
-- moves, the index stamp never reads it.
-- name: YankCargoCrate :exec
UPDATE package_versions v
SET metadata = jsonb_set(v.metadata, '{yanked}', to_jsonb(sqlc.arg(yanked)::boolean)), updated_at = now()
FROM packages p
WHERE p.id = v.package_id AND p.repository_id = sqlc.arg(repository_id) AND p.name_key = lower(sqlc.arg(name)::text)
  AND v.version_key = sqlc.arg(version) AND v.deleted_at IS NULL;

-- name: IncrementCargoDownloadCount :exec
UPDATE package_files f SET download_count = f.download_count + 1, updated_at = now()
FROM package_versions v
JOIN packages p ON p.id = v.package_id
WHERE f.version_id = v.id AND f.deleted_at IS NULL AND v.deleted_at IS NULL
  AND p.repository_id = sqlc.arg(repository_id) AND p.name_key = lower(sqlc.arg(name)::text)
  AND v.version_key = sqlc.arg(version);

-- name: CargoIndexStamp :one
-- Fingerprint of the sparse index entry of one crate: its live versions and
-- their yanked flags, the only mutable state the entry shows. (updated_at is
-- no validator: every download bumps it.)
SELECT (count(*)::text || ':' ||
        COALESCE(sum(hashtextextended(f.id::text || COALESCE(v.metadata->>'yanked', 'false'), 0)), 0)::text)::text AS stamp
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = lower(sqlc.arg(name)::text);

-- name: ListCargoIndexVersions :many
-- The columns a sparse index entry is built from (no readme, description or
-- authors).
SELECT COALESCE(v.metadata->>'name', p.name)::text AS name, v.version,
       COALESCE(v.metadata->'metadata_json', '{}'::jsonb)::jsonb AS metadata_json,
       b.sha256 AS checksum, COALESCE((v.metadata->>'yanked')::boolean, false)::boolean AS yanked
FROM packages p
JOIN package_versions v ON v.package_id = p.id AND v.deleted_at IS NULL
JOIN package_files f ON f.version_id = v.id AND f.deleted_at IS NULL
JOIN blobs b ON b.id = f.blob_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.name_key = lower(sqlc.arg(name)::text)
ORDER BY v.created_at, v.id;
