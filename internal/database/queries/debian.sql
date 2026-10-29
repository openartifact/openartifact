-- name: CreateDebianDistribution :one
INSERT INTO debian_distributions (repository_id, name, codename, description)
VALUES ($1, $2, $3, $4)
RETURNING id, created_at, updated_at;

-- name: GetDebianDistributionsByRepo :many
SELECT id, repository_id, name,
       COALESCE(codename, '') AS codename,
       COALESCE(description, '') AS description,
       created_at, updated_at
FROM debian_distributions
WHERE repository_id = $1;

-- name: CreateDebianComponent :one
INSERT INTO debian_components (distribution_id, name, description)
VALUES ($1, $2, $3)
RETURNING id, created_at, updated_at;

-- name: GetDebianComponentsByDist :many
SELECT id, distribution_id, name,
       COALESCE(description, '') AS description,
       created_at, updated_at
FROM debian_components
WHERE distribution_id = $1;

-- name: CreateDebianArchitecture :one
INSERT INTO debian_architectures (distribution_id, name)
VALUES ($1, $2)
RETURNING id, created_at, updated_at;

-- name: GetDebianArchitecturesByDist :many
SELECT id, distribution_id, name, created_at, updated_at
FROM debian_architectures
WHERE distribution_id = $1;

-- Deb packages on the generic tables (see pkg_deb.go for the mapping): the
-- file is package_files, its version package_versions, its name packages, the
-- distribution/component/control stanza deb_files. Every read hides trashed
-- files (package_files.deleted_at).

-- name: GetDebianComponentName :one
SELECT name FROM debian_components WHERE id = $1;

-- name: InsertDebFile :exec
INSERT INTO deb_files (id, distribution_id, component_id) VALUES ($1, $2, $3);

-- name: GetDebPackageByID :one
SELECT f.id, f.repository_id, d.distribution_id, d.component_id, f.blob_id,
       p.name, v.version, f.arch,
       COALESCE(v.metadata ->> 'summary', '')::text AS summary,
       COALESCE(v.metadata ->> 'description', '')::text AS description,
       COALESCE(v.metadata ->> 'license', '')::text AS license,
       COALESCE(v.metadata ->> 'maintainer', '')::text AS maintainer,
       COALESCE(v.metadata ->> 'homepage', '')::text AS homepage,
       f.filename,
       COALESCE((f.attrs ->> 'installed_size')::bigint, 0)::bigint AS installed_size,
       f.created_at, f.updated_at
FROM package_files f
JOIN deb_files d ON d.id = f.id
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
WHERE f.id = $1 AND f.deleted_at IS NULL;

-- name: ListDebPackagesByRepo :many
SELECT f.id, f.repository_id, d.distribution_id, d.component_id, f.blob_id,
       p.name, v.version, f.arch,
       COALESCE(v.metadata ->> 'summary', '')::text AS summary,
       COALESCE(v.metadata ->> 'description', '')::text AS description,
       COALESCE(v.metadata ->> 'license', '')::text AS license,
       COALESCE(v.metadata ->> 'maintainer', '')::text AS maintainer,
       COALESCE(v.metadata ->> 'homepage', '')::text AS homepage,
       f.filename,
       COALESCE((f.attrs ->> 'installed_size')::bigint, 0)::bigint AS installed_size,
       f.created_at, f.updated_at
FROM package_files f
JOIN deb_files d ON d.id = f.id
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL;

-- Download by file name and the upload duplicate check: idx_package_files_filename.
-- name: GetDebianPackageByFilename :one
SELECT f.id, f.repository_id, d.distribution_id, d.component_id,
       COALESCE(b.sha256, '')::text AS sha256,
       p.name, v.version, f.arch,
       COALESCE(v.metadata ->> 'summary', '')::text AS summary,
       COALESCE(v.metadata ->> 'description', '')::text AS description,
       COALESCE(v.metadata ->> 'license', '')::text AS license,
       COALESCE(v.metadata ->> 'maintainer', '')::text AS maintainer,
       COALESCE(v.metadata ->> 'homepage', '')::text AS homepage,
       f.filename,
       COALESCE(b.size_bytes, 0)::bigint AS package_size,
       COALESCE((f.attrs ->> 'installed_size')::bigint, 0)::bigint AS installed_size,
       f.created_at, f.updated_at
FROM package_files f
JOIN deb_files d ON d.id = f.id
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
LEFT JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL AND f.filename = $2;

-- name: GetDebianPackagesByDistComp :many
SELECT f.id, f.repository_id, d.distribution_id, d.component_id,
       COALESCE(b.sha256, '')::text AS sha256,
       p.name, v.version, f.arch,
       COALESCE(v.metadata ->> 'summary', '')::text AS summary,
       COALESCE(v.metadata ->> 'description', '')::text AS description,
       COALESCE(v.metadata ->> 'license', '')::text AS license,
       COALESCE(v.metadata ->> 'maintainer', '')::text AS maintainer,
       COALESCE(v.metadata ->> 'homepage', '')::text AS homepage,
       f.filename,
       COALESCE(b.size_bytes, 0)::bigint AS package_size,
       COALESCE((f.attrs ->> 'installed_size')::bigint, 0)::bigint AS installed_size,
       f.created_at, f.updated_at
FROM package_files f
JOIN deb_files d ON d.id = f.id
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN debian_distributions dd ON dd.id = d.distribution_id
JOIN debian_components dc ON dc.id = d.component_id
LEFT JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1
  AND f.deleted_at IS NULL
  AND (sqlc.arg(dist)::text = '' OR dd.name = sqlc.arg(dist))
  AND (sqlc.arg(comp)::text = '' OR dc.name = sqlc.arg(comp))
  AND (sqlc.arg(arch)::text = '' OR f.arch = sqlc.arg(arch));

-- The control stanza and the maintainer/homepage of the version metadata, set
-- after the row is written. Only a live file is touched.
-- name: SetDebControlFields :exec
WITH target AS (
    UPDATE deb_files d SET control_fields = sqlc.arg(control_fields)::jsonb
    FROM package_files f
    WHERE d.id = sqlc.arg(id) AND f.id = d.id AND f.deleted_at IS NULL
    RETURNING f.version_id
)
UPDATE package_versions v SET metadata = v.metadata || sqlc.arg(metadata)::jsonb
WHERE v.id IN (SELECT version_id FROM target);

-- The Packages stanzas of a repository: ordered as the legacy query was
-- (distribution, component, architecture, name, version). Filtered calls
-- (streaming) use idx_deb_files_dist_comp.
-- name: ListDebPackageEntries :many
SELECT f.id, p.name, v.version, f.arch, f.filename,
       d.distribution_id, dd.name AS distribution, dc.name AS component_name,
       COALESCE(b.sha256, '')::text AS sha256,
       COALESCE(b.size_bytes, 0)::bigint AS size,
       COALESCE((f.attrs ->> 'installed_size')::bigint, 0)::bigint AS installed_size,
       d.control_fields
FROM package_files f
JOIN deb_files d ON d.id = f.id
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN debian_distributions dd ON dd.id = d.distribution_id
JOIN debian_components dc ON dc.id = d.component_id
LEFT JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL
  AND (sqlc.arg(dist)::text = '' OR dd.name = sqlc.arg(dist))
  AND (sqlc.arg(comp)::text = '' OR dc.name = sqlc.arg(comp))
  AND (sqlc.arg(arch)::text = '' OR f.arch = sqlc.arg(arch))
ORDER BY dd.name, dc.name, f.arch, p.name, v.version;
