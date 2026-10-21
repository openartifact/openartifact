-- Debian package queries against deb_packages. Package data lives in the
-- per-format rpm_packages and deb_packages tables; their blob_id FKs point
-- into the global blobs table, which carries the sha256/size columns.
--
-- RPM-side lookups (GetComponentByID, GetComponentByFilename,
-- GetComponentsByRepo, DeleteComponent) are hand-written in postgres.go
-- because they must try both rpm_packages and deb_packages (IDs are shared
-- across the historical split) — no sqlc query is generated for them.

-- name: GetDebianPackageByFilename :one
SELECT dp.id, dp.repository_id, dp.distribution_id, dp.component_id,
       COALESCE(b.sha256, '') AS sha256,
       dp.name, dp.version, dp.arch,
       COALESCE(dp.summary, '') AS summary,
       COALESCE(dp.description, '') AS description,
       COALESCE(dp.license, '') AS license,
       COALESCE(dp.maintainer, '') AS maintainer,
       COALESCE(dp.homepage, '') AS homepage,
       COALESCE(dp.filename, '') AS filename,
       COALESCE(b.size_bytes, 0) AS package_size,
       COALESCE(dp.installed_size, 0) AS installed_size,
       dp.created_at, dp.updated_at
FROM deb_packages dp
LEFT JOIN blobs b ON b.id = dp.blob_id
WHERE dp.repository_id = $1 AND dp.filename = $2;

-- name: GetDebianPackagesByDistComp :many
SELECT dp.id, dp.repository_id, dp.distribution_id, dp.component_id,
       COALESCE(b.sha256, '') AS sha256,
       dp.name, dp.version, dp.arch,
       COALESCE(dp.summary, '') AS summary,
       COALESCE(dp.description, '') AS description,
       COALESCE(dp.license, '') AS license,
       COALESCE(dp.maintainer, '') AS maintainer,
       COALESCE(dp.homepage, '') AS homepage,
       COALESCE(dp.filename, '') AS filename,
       COALESCE(b.size_bytes, 0) AS package_size,
       COALESCE(dp.installed_size, 0) AS installed_size,
       dp.created_at, dp.updated_at
FROM deb_packages dp
LEFT JOIN blobs b ON b.id = dp.blob_id
JOIN debian_distributions dd ON dd.id = dp.distribution_id
JOIN debian_components dc ON dc.id = dp.component_id
WHERE dp.repository_id = $1
  AND (sqlc.arg(dist)::text = '' OR dd.name = sqlc.arg(dist))
  AND (sqlc.arg(comp)::text = '' OR dc.name = sqlc.arg(comp))
  AND (sqlc.arg(arch)::text = '' OR dp.arch = sqlc.arg(arch));


-- Repository package listing across every format (backs
-- GET /api/v1/repositories/{name}/packages). Each branch maps one per-format
-- table onto the shared models.Component shape; columns with no equivalent
-- default to ''/0. The format filter is applied inside every branch so the
-- planner can discard the other formats' tables. Keep ListRepoComponents and
-- CountRepoComponents in sync (same branches, same filter).

-- name: ListRepoComponents :many
SELECT id, repository_id, name, version, format, arch, sha256, release, epoch, build_time,
       archive_size, package_size, installed_size, filename, created_at, updated_at
FROM (
    -- deb: one row per deb_packages row; size = blob size.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'deb'::text AS format, t.arch::text AS arch, COALESCE(b.sha256, '')::text AS sha256,
           ''::text AS release, 0::int AS epoch, 0::bigint AS build_time,
           0::bigint AS archive_size, COALESCE(b.size_bytes, 0)::bigint AS package_size, COALESCE(t.installed_size, 0)::bigint AS installed_size,
           COALESCE(t.filename, '')::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM deb_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'deb')
    UNION ALL
    -- rpm: one row per rpm_packages row.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'rpm'::text AS format, t.arch::text AS arch, COALESCE(b.sha256, '')::text AS sha256,
           t.release::text AS release, t.epoch::int AS epoch, COALESCE(EXTRACT(EPOCH FROM t.build_time)::bigint, 0)::bigint AS build_time,
           COALESCE(t.archive_size, 0)::bigint AS archive_size, COALESCE(t.package_size, b.size_bytes, 0)::bigint AS package_size, COALESCE(t.installed_size, 0)::bigint AS installed_size,
           COALESCE(t.filename, '')::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM rpm_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'rpm')
    UNION ALL
    -- npm: one row per published version (scoped name in full_name).
    SELECT t.id::uuid AS id, np.repository_id::uuid AS repository_id, np.full_name::text AS name, t.version::text AS version,
           'npm'::text AS format, ''::text AS arch, COALESCE(b.sha256, '')::text AS sha256,
           ''::text AS release, 0::int AS epoch, 0::bigint AS build_time,
           0::bigint AS archive_size, COALESCE(b.size_bytes, 0)::bigint AS package_size, 0::bigint AS installed_size,
           t.tarball_filename::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM npm_versions t JOIN npm_packages np ON np.id = t.package_id LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE np.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'npm')
    UNION ALL
    -- pypi: one row per distribution file; arch = python_version tag.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'pypi'::text AS format, COALESCE(t.python_version, '')::text AS arch, COALESCE(b.sha256, '')::text AS sha256,
           ''::text AS release, 0::int AS epoch, 0::bigint AS build_time,
           0::bigint AS archive_size, COALESCE(b.size_bytes, 0)::bigint AS package_size, 0::bigint AS installed_size,
           t.filename::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM pypi_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'pypi')
    UNION ALL
    -- maven: one row per stored file (jar, pom, ...); name = group:artifact, arch = classifier, filename = repo path.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.group_id || ':' || t.artifact_id_coord::text AS name, t.version::text AS version,
           'maven'::text AS format, t.classifier::text AS arch, COALESCE(b.sha256, '')::text AS sha256,
           ''::text AS release, 0::int AS epoch, 0::bigint AS build_time,
           0::bigint AS archive_size, COALESCE(b.size_bytes, 0)::bigint AS package_size, 0::bigint AS installed_size,
           t.repo_path::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM maven_artifacts t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'maven')
    UNION ALL
    -- ivy: one row per artifact file; name = organisation:module.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.organisation || ':' || t.module::text AS name, t.revision::text AS version,
           'ivy'::text AS format, ''::text AS arch, COALESCE(b.sha256, '')::text AS sha256,
           ''::text AS release, 0::int AS epoch, 0::bigint AS build_time,
           0::bigint AS archive_size, COALESCE(b.size_bytes, 0)::bigint AS package_size, 0::bigint AS installed_size,
           t.artifact_name || '.' || t.artifact_ext::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM ivy_artifacts t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'ivy')
    UNION ALL
    -- sbt: one row per artifact file; arch = scala_version.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.group_id || ':' || t.artifact_id::text AS name, t.version::text AS version,
           'sbt'::text AS format, t.scala_version::text AS arch, COALESCE(b.sha256, '')::text AS sha256,
           ''::text AS release, 0::int AS epoch, 0::bigint AS build_time,
           0::bigint AS archive_size, COALESCE(b.size_bytes, 0)::bigint AS package_size, 0::bigint AS installed_size,
           t.filename::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM sbt_artifacts t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'sbt')
    UNION ALL
    -- nuget: one row per package version.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.package_id::text AS name, t.version::text AS version,
           'nuget'::text AS format, ''::text AS arch, COALESCE(b.sha256, '')::text AS sha256,
           ''::text AS release, 0::int AS epoch, 0::bigint AS build_time,
           0::bigint AS archive_size, COALESCE(b.size_bytes, 0)::bigint AS package_size, 0::bigint AS installed_size,
           t.package_id || '.' || t.version || '.nupkg'::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM nuget_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'nuget')
    UNION ALL
    -- gomod: one row per module version; the zip blob is preferred, the .mod blob otherwise.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.module_path::text AS name, t.version::text AS version,
           'gomod'::text AS format, ''::text AS arch, COALESCE(b.sha256, '')::text AS sha256,
           ''::text AS release, 0::int AS epoch, 0::bigint AS build_time,
           0::bigint AS archive_size, COALESCE(b.size_bytes, 0)::bigint AS package_size, 0::bigint AS installed_size,
           t.version || CASE WHEN t.zip_blob_id IS NOT NULL THEN '.zip' ELSE '.mod' END::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM gomod_modules t LEFT JOIN blobs b ON b.id = COALESCE(t.zip_blob_id, t.mod_blob_id)
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'gomod')
    UNION ALL
    -- cargo: one row per crate version (yanked included).
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'cargo'::text AS format, ''::text AS arch, COALESCE(b.sha256, '')::text AS sha256,
           ''::text AS release, 0::int AS epoch, 0::bigint AS build_time,
           0::bigint AS archive_size, COALESCE(b.size_bytes, 0)::bigint AS package_size, 0::bigint AS installed_size,
           t.name || '-' || t.version || '.crate'::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM cargo_crates t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'cargo')
    UNION ALL
    -- rubygems: one row per gem version/platform; arch = platform.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'rubygems'::text AS format, t.platform::text AS arch, COALESCE(b.sha256, '')::text AS sha256,
           ''::text AS release, 0::int AS epoch, 0::bigint AS build_time,
           0::bigint AS archive_size, COALESCE(NULLIF(t.size, 0), b.size_bytes, 0)::bigint AS package_size, 0::bigint AS installed_size,
           t.name || '-' || t.version || CASE WHEN t.platform = 'ruby' THEN '' ELSE '-' || t.platform END || '.gem'::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM rubygems_gems t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'rubygems')
    UNION ALL
    -- conda: one row per build; arch = platform (subdir), release = build_string.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'conda'::text AS format, t.platform::text AS arch, COALESCE(b.sha256, '')::text AS sha256,
           t.build_string::text AS release, 0::int AS epoch, 0::bigint AS build_time,
           0::bigint AS archive_size, COALESCE(b.size_bytes, 0)::bigint AS package_size, 0::bigint AS installed_size,
           t.name || '-' || t.version || '-' || t.build_string || '.tar.bz2'::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM conda_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'conda')
    UNION ALL
    -- conan: one row per recipe revision (blob_id is nullable); release = recipe_revision, filename = full reference.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'conan'::text AS format, ''::text AS arch, COALESCE(b.sha256, '')::text AS sha256,
           t.recipe_revision::text AS release, 0::int AS epoch, 0::bigint AS build_time,
           0::bigint AS archive_size, COALESCE(b.size_bytes, 0)::bigint AS package_size, 0::bigint AS installed_size,
           t.name || '/' || t.version || '@' || t.user_channel || '#' || t.recipe_revision::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM conan_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'conan')
    UNION ALL
    -- helm: one row per chart version.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'helm'::text AS format, ''::text AS arch, COALESCE(b.sha256, '')::text AS sha256,
           ''::text AS release, 0::int AS epoch, 0::bigint AS build_time,
           0::bigint AS archive_size, COALESCE(b.size_bytes, 0)::bigint AS package_size, 0::bigint AS installed_size,
           t.name || '-' || t.version || '.tgz'::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM helm_charts t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'helm')
    UNION ALL
    -- raw: one row per stored path; name = filename = path, no version.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.path::text AS name, ''::text AS version,
           'raw'::text AS format, ''::text AS arch, COALESCE(b.sha256, '')::text AS sha256,
           ''::text AS release, 0::int AS epoch, 0::bigint AS build_time,
           0::bigint AS archive_size, COALESCE(b.size_bytes, 0)::bigint AS package_size, 0::bigint AS installed_size,
           t.path::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM raw_artifacts t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'raw')
    UNION ALL
    -- docker: one row per tag (the registry image name is the repository name); size = manifest size, filename = manifest digest.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, r.name::text AS name, t.name::text AS version,
           'docker'::text AS format, ''::text AS arch, COALESCE(b.sha256, '')::text AS sha256,
           ''::text AS release, 0::int AS epoch, 0::bigint AS build_time,
           0::bigint AS archive_size, COALESCE(dm.size, 0)::bigint AS package_size, 0::bigint AS installed_size,
           t.manifest_digest::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM docker_tags t JOIN repositories r ON r.id = t.repository_id LEFT JOIN docker_manifests dm ON dm.repository_id = t.repository_id AND dm.digest = t.manifest_digest LEFT JOIN blobs b ON b.id = dm.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'docker')
) AS p
ORDER BY created_at DESC, id
LIMIT sqlc.arg(page_limit)::int OFFSET sqlc.arg(page_offset)::int;

-- name: CountRepoComponents :one
SELECT COUNT(*)::bigint FROM (
    -- deb
    SELECT 1 FROM deb_packages t
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'deb')
    UNION ALL
    -- rpm
    SELECT 1 FROM rpm_packages t
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'rpm')
    UNION ALL
    -- npm
    SELECT 1 FROM npm_versions t JOIN npm_packages np ON np.id = t.package_id
    WHERE np.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'npm')
    UNION ALL
    -- pypi
    SELECT 1 FROM pypi_packages t
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'pypi')
    UNION ALL
    -- maven
    SELECT 1 FROM maven_artifacts t
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'maven')
    UNION ALL
    -- ivy
    SELECT 1 FROM ivy_artifacts t
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'ivy')
    UNION ALL
    -- sbt
    SELECT 1 FROM sbt_artifacts t
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'sbt')
    UNION ALL
    -- nuget
    SELECT 1 FROM nuget_packages t
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'nuget')
    UNION ALL
    -- gomod
    SELECT 1 FROM gomod_modules t
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'gomod')
    UNION ALL
    -- cargo
    SELECT 1 FROM cargo_crates t
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'cargo')
    UNION ALL
    -- rubygems
    SELECT 1 FROM rubygems_gems t
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'rubygems')
    UNION ALL
    -- conda
    SELECT 1 FROM conda_packages t
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'conda')
    UNION ALL
    -- conan
    SELECT 1 FROM conan_packages t
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'conan')
    UNION ALL
    -- helm
    SELECT 1 FROM helm_charts t
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'helm')
    UNION ALL
    -- raw
    SELECT 1 FROM raw_artifacts t
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'raw')
    UNION ALL
    -- docker
    SELECT 1 FROM docker_tags t
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'docker')
) AS p;

-- Download lookup for one listed package (backs
-- GET /api/v1/repositories/{name}/packages/{id}/download). Every branch
-- mirrors the same-named ListRepoComponents branch (same id, repository scope,
-- blob join and filename expression), so an id the listing returns resolves
-- here to the blob it shows. Rows without a blob (conan recipes without one)
-- come back with an empty sha256. Keep in sync with ListRepoComponents.

-- name: GetRepoComponentBlob :one
SELECT id, format, filename, sha256, size_bytes, media_type
FROM (
    SELECT t.id::uuid AS id, 'deb'::text AS format, COALESCE(t.filename, '')::text AS filename,
           COALESCE(b.sha256, '')::text AS sha256, COALESCE(b.size_bytes, 0)::bigint AS size_bytes, COALESCE(b.media_type, '')::text AS media_type
    FROM deb_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'rpm'::text, COALESCE(t.filename, '')::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM rpm_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'npm'::text, t.tarball_filename::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM npm_versions t JOIN npm_packages np ON np.id = t.package_id LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE np.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'pypi'::text, t.filename::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM pypi_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'maven'::text, t.repo_path::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM maven_artifacts t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'ivy'::text, t.artifact_name || '.' || t.artifact_ext::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM ivy_artifacts t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'sbt'::text, t.filename::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM sbt_artifacts t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'nuget'::text, t.package_id || '.' || t.version || '.nupkg'::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM nuget_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'gomod'::text, t.version || CASE WHEN t.zip_blob_id IS NOT NULL THEN '.zip' ELSE '.mod' END::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM gomod_modules t LEFT JOIN blobs b ON b.id = COALESCE(t.zip_blob_id, t.mod_blob_id)
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'cargo'::text, t.name || '-' || t.version || '.crate'::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM cargo_crates t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'rubygems'::text, t.name || '-' || t.version || CASE WHEN t.platform = 'ruby' THEN '' ELSE '-' || t.platform END || '.gem'::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM rubygems_gems t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'conda'::text, t.name || '-' || t.version || '-' || t.build_string || '.tar.bz2'::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM conda_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'conan'::text, t.name || '/' || t.version || '@' || t.user_channel || '#' || t.recipe_revision::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM conan_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'helm'::text, t.name || '-' || t.version || '.tgz'::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM helm_charts t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'raw'::text, t.path::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM raw_artifacts t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- docker: the manifest blob; media type is the manifest's own.
    SELECT t.id::uuid, 'docker'::text, t.manifest_digest::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(dm.media_type, '')::text
    FROM docker_tags t LEFT JOIN docker_manifests dm ON dm.repository_id = t.repository_id AND dm.digest = t.manifest_digest LEFT JOIN blobs b ON b.id = dm.blob_id
    WHERE t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
) AS p
LIMIT 1;
