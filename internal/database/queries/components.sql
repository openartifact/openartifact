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
WHERE dp.repository_id = $1 AND dp.deleted_at IS NULL AND dp.filename = $2;

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
  AND dp.deleted_at IS NULL
  AND (sqlc.arg(dist)::text = '' OR dd.name = sqlc.arg(dist))
  AND (sqlc.arg(comp)::text = '' OR dc.name = sqlc.arg(comp))
  AND (sqlc.arg(arch)::text = '' OR dp.arch = sqlc.arg(arch));


-- Repository package listing across every format (backs
-- GET /api/v1/repositories/{name}/packages). Each branch maps one per-format
-- table onto the shared models.Component shape; columns with no equivalent
-- default to ''/0. The format filter is applied inside every branch so the
-- planner can discard the other formats' tables. Keep ListRepoComponents,
-- ListRepoComponentsByName and CountRepoComponents in sync (same branches, same
-- filters). name_filter is an exact match on the listed name; q is a
-- case-insensitive substring match on the listed name or version.
--
-- ListRepoComponentsByName is ListRepoComponents in (name, version, id) order
-- instead of newest first: every branch picks its top rows by that order (a
-- bounded sort of the repository's rows in the branch table), so it backs the
-- browser's "sort by name" but not the default listing.
--
-- The listing is built in two stages so that no row outside the requested page
-- ever touches the blobs table: every branch first picks its own top
-- (page_limit + page_offset) rows in the final (created_at DESC, id) order from
-- the narrow base table and maps them to the shared shape, the union is sorted
-- and cut to the page, and only then are blob sha256/size resolved for those
-- rows. The branch columns pref_size/fb_size carry the per-format size rules
-- (package_size = COALESCE(pref_size, blob size, fb_size, 0)); blob_id or
-- blob_key (proxy cache, resolved by sha256) locate the blob.

-- name: ListRepoComponents :many
SELECT p.id::uuid AS id, p.repository_id::uuid AS repository_id, p.name::text AS name, p.version::text AS version,
       p.format::text AS format, p.arch::text AS arch,
       COALESCE(COALESCE(bi.sha256, bs.sha256), '')::text AS sha256,
       p.release::text AS release, p.epoch::int AS epoch, p.build_time::bigint AS build_time,
       p.archive_size::bigint AS archive_size,
       COALESCE(p.pref_size, bi.size_bytes, bs.size_bytes, p.fb_size, 0)::bigint AS package_size,
       p.installed_size::bigint AS installed_size, p.filename::text AS filename,
       p.created_at::timestamptz AS created_at, p.updated_at::timestamptz AS updated_at
FROM (
    SELECT * FROM (
    -- deb: one row per deb_packages row; size = blob size.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'deb'::text AS format, t.arch::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, COALESCE(t.installed_size, 0)::bigint AS installed_size,
           COALESCE(t.filename, '')::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT id, repository_id, distribution_id, component_id, blob_id, name, version, arch, summary, description, license, maintainer, homepage, filename, installed_size, created_at, updated_at, control_fields, deleted_at, deleted_by FROM deb_packages WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'deb') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY created_at DESC, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- rpm: one row per rpm_packages row; the package's own size wins over the blob size.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'rpm'::text AS format, t.arch::text AS arch, t.release::text AS release, t.epoch::int AS epoch, COALESCE(EXTRACT(EPOCH FROM t.build_time)::bigint, 0)::bigint AS build_time,
           COALESCE(t.archive_size, 0)::bigint AS archive_size, t.package_size::bigint AS pref_size, NULL::bigint::bigint AS fb_size, COALESCE(t.installed_size, 0)::bigint AS installed_size,
           COALESCE(t.filename, '')::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT id, repository_id, blob_id, name, version, release, epoch, arch, summary, description, license, url, vendor, group_name, packager, source_rpm, build_host, build_time, filename, package_size, installed_size, archive_size, created_at, updated_at, header_start, header_end, rpm_extra, deleted_at, deleted_by FROM rpm_packages WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'rpm') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY created_at DESC, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- npm: one row per published version (scoped name in full_name).
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.full_name::text AS name, t.version::text AS version,
           'npm'::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.tarball_filename::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT v.id, v.package_id, v.version, v.manifest_json, v.integrity, v.shasum, v.tarball_filename, v.created_at, v.blob_id, v.updated_at, v.deleted_at, v.deleted_by, np.repository_id, np.full_name FROM npm_versions v JOIN npm_packages np ON np.id = v.package_id WHERE v.deleted_at IS NULL AND np.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'npm') AND (sqlc.arg(name_filter)::text = '' OR np.full_name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(np.full_name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(v.version), lower(sqlc.arg(q)::text)) > 0) ORDER BY v.created_at DESC, v.id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- pypi: one row per distribution file; arch = python_version tag.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'pypi'::text AS format, COALESCE(t.python_version, '')::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.filename::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM pypi_packages WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'pypi') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY created_at DESC, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- maven: one row per stored file (jar, pom, ...); name = group:artifact, arch = classifier, filename = repo path.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.group_id || ':' || t.artifact_id_coord::text AS name, t.version::text AS version,
           'maven'::text AS format, t.classifier::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.repo_path::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT id, repository_id, group_id, artifact_id_coord, version, classifier, extension, snapshot_timestamp, snapshot_build_number, is_snapshot, repo_path, created_at, updated_at, blob_id, md5, sha1, sha512, deleted_at, deleted_by FROM maven_artifacts WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'maven') AND (sqlc.arg(name_filter)::text = '' OR (group_id || ':' || artifact_id_coord) = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower((group_id || ':' || artifact_id_coord)), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY created_at DESC, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- ivy: one row per artifact file; name = organisation:module.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.organisation || ':' || t.module::text AS name, t.revision::text AS version,
           'ivy'::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.artifact_name || '.' || t.artifact_ext::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM ivy_artifacts WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'ivy') AND (sqlc.arg(name_filter)::text = '' OR (organisation || ':' || module) = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower((organisation || ':' || module)), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(revision), lower(sqlc.arg(q)::text)) > 0) ORDER BY created_at DESC, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- sbt: one row per artifact file; arch = scala_version.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.group_id || ':' || t.artifact_id::text AS name, t.version::text AS version,
           'sbt'::text AS format, t.scala_version::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.filename::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM sbt_artifacts WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'sbt') AND (sqlc.arg(name_filter)::text = '' OR (group_id || ':' || artifact_id) = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower((group_id || ':' || artifact_id)), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY created_at DESC, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- nuget: one row per package version.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.package_id::text AS name, t.version::text AS version,
           'nuget'::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.package_id || '.' || t.version || '.nupkg'::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM nuget_packages WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'nuget') AND (sqlc.arg(name_filter)::text = '' OR package_id = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(package_id), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY created_at DESC, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- gomod: one row per module version; the zip blob is preferred, the .mod blob otherwise.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.module_path::text AS name, t.version::text AS version,
           'go'::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.version || CASE WHEN t.zip_blob_id IS NOT NULL THEN '.zip' ELSE '.mod' END::text AS filename, COALESCE(t.zip_blob_id, t.mod_blob_id)::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM gomod_modules WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'go') AND (sqlc.arg(name_filter)::text = '' OR module_path = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(module_path), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY created_at DESC, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- cargo: one row per crate version (yanked included).
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'cargo'::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.name || '-' || t.version || '.crate'::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM cargo_crates WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'cargo') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY created_at DESC, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- rubygems: one row per gem version/platform; arch = platform; a non-zero recorded size wins over the blob size.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'rubygems'::text AS format, t.platform::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULLIF(t.size, 0)::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.name || '-' || t.version || CASE WHEN t.platform = 'ruby' THEN '' ELSE '-' || t.platform END || '.gem'::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM rubygems_gems WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'rubygems') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY created_at DESC, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- conda: one row per build; arch = platform (subdir), release = build_string.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'conda'::text AS format, t.platform::text AS arch, t.build_string::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.name || '-' || t.version || '-' || t.build_string || t.extension::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM conda_packages WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'conda') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY created_at DESC, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- conan: one row per recipe revision (its conanmanifest.txt, or a pre-0.5.0 row with no file name);
    -- release = recipe_revision, filename = full reference.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'conan'::text AS format, ''::text AS arch, t.recipe_revision::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.name || '/' || t.version || '@' || t.user_channel || '#' || t.recipe_revision::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM conan_packages WHERE deleted_at IS NULL AND package_id = '' AND filename IN ('', 'conanmanifest.txt') AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'conan') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY created_at DESC, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- helm: one row per chart version.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'helm'::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.name || '-' || t.version || '.tgz'::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM helm_charts WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'helm') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY created_at DESC, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- raw: one row per stored path; name = filename = path, no version.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.path::text AS name, ''::text AS version,
           'raw'::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.path::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM raw_artifacts WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'raw') AND (sqlc.arg(name_filter)::text = '' OR path = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(path), lower(sqlc.arg(q)::text)) > 0) ORDER BY created_at DESC, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- proxy cache: one row per cached upstream file of a proxy repository (any format); name = filename = upstream-relative path; the blob is resolved by sha256 and wins over the recorded size.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.cache_key::text AS name, ''::text AS version,
           t.repo_format::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, t.size_bytes::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.cache_key::text AS filename, NULL::uuid AS blob_id, t.blob_key::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT p.*, r.format AS repo_format FROM proxy_cache_entries p JOIN repositories r ON r.id = p.repository_id WHERE r.deleted_at IS NULL AND p.repository_id = sqlc.arg(repo_id)::uuid AND p.status_code = 200 AND p.blob_key IS NOT NULL AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = r.format) AND (sqlc.arg(name_filter)::text = '' OR cache_key = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(cache_key), lower(sqlc.arg(q)::text)) > 0) ORDER BY p.created_at DESC, p.id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- docker: one row per tag (the registry image name is the repository name, plus the image path inside it); size = manifest size, blob = manifest blob, filename = manifest digest.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, (CASE WHEN t.image = '' THEN r.name ELSE r.name || '/' || t.image END)::text AS name, t.name::text AS version,
           'docker'::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, COALESCE(dm.size, 0)::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.manifest_digest::text AS filename, dm.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM docker_tags WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'docker') AND (sqlc.arg(name_filter)::text = '' OR (CASE WHEN image = '' THEN (SELECT r.name FROM repositories r WHERE r.id = repository_id) ELSE (SELECT r.name FROM repositories r WHERE r.id = repository_id) || '/' || image END) = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower((CASE WHEN image = '' THEN (SELECT r.name FROM repositories r WHERE r.id = repository_id) ELSE (SELECT r.name FROM repositories r WHERE r.id = repository_id) || '/' || image END)), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0) ORDER BY created_at DESC, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    JOIN repositories r ON r.id = t.repository_id AND r.deleted_at IS NULL
    LEFT JOIN docker_manifests dm ON dm.repository_id = t.repository_id AND dm.image = t.image AND dm.digest = t.manifest_digest AND dm.deleted_at IS NULL
    ) AS u
    ORDER BY u.created_at DESC, u.id
    LIMIT sqlc.arg(page_limit)::int OFFSET sqlc.arg(page_offset)::int
) AS p
LEFT JOIN LATERAL (SELECT sha256, size_bytes FROM blobs WHERE id = p.blob_id) AS bi ON true
LEFT JOIN LATERAL (SELECT sha256, size_bytes FROM blobs WHERE sha256 = p.blob_key) AS bs ON true
ORDER BY p.created_at DESC, p.id;

-- name: ListRepoComponentsByName :many
SELECT p.id::uuid AS id, p.repository_id::uuid AS repository_id, p.name::text AS name, p.version::text AS version,
       p.format::text AS format, p.arch::text AS arch,
       COALESCE(COALESCE(bi.sha256, bs.sha256), '')::text AS sha256,
       p.release::text AS release, p.epoch::int AS epoch, p.build_time::bigint AS build_time,
       p.archive_size::bigint AS archive_size,
       COALESCE(p.pref_size, bi.size_bytes, bs.size_bytes, p.fb_size, 0)::bigint AS package_size,
       p.installed_size::bigint AS installed_size, p.filename::text AS filename,
       p.created_at::timestamptz AS created_at, p.updated_at::timestamptz AS updated_at
FROM (
    SELECT * FROM (
    -- deb: one row per deb_packages row; size = blob size.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'deb'::text AS format, t.arch::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, COALESCE(t.installed_size, 0)::bigint AS installed_size,
           COALESCE(t.filename, '')::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT id, repository_id, distribution_id, component_id, blob_id, name, version, arch, summary, description, license, maintainer, homepage, filename, installed_size, created_at, updated_at, control_fields, deleted_at, deleted_by FROM deb_packages WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'deb') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY name, version, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- rpm: one row per rpm_packages row; the package's own size wins over the blob size.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'rpm'::text AS format, t.arch::text AS arch, t.release::text AS release, t.epoch::int AS epoch, COALESCE(EXTRACT(EPOCH FROM t.build_time)::bigint, 0)::bigint AS build_time,
           COALESCE(t.archive_size, 0)::bigint AS archive_size, t.package_size::bigint AS pref_size, NULL::bigint::bigint AS fb_size, COALESCE(t.installed_size, 0)::bigint AS installed_size,
           COALESCE(t.filename, '')::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT id, repository_id, blob_id, name, version, release, epoch, arch, summary, description, license, url, vendor, group_name, packager, source_rpm, build_host, build_time, filename, package_size, installed_size, archive_size, created_at, updated_at, header_start, header_end, rpm_extra, deleted_at, deleted_by FROM rpm_packages WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'rpm') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY name, version, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- npm: one row per published version (scoped name in full_name).
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.full_name::text AS name, t.version::text AS version,
           'npm'::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.tarball_filename::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT v.id, v.package_id, v.version, v.manifest_json, v.integrity, v.shasum, v.tarball_filename, v.created_at, v.blob_id, v.updated_at, v.deleted_at, v.deleted_by, np.repository_id, np.full_name FROM npm_versions v JOIN npm_packages np ON np.id = v.package_id WHERE v.deleted_at IS NULL AND np.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'npm') AND (sqlc.arg(name_filter)::text = '' OR np.full_name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(np.full_name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(v.version), lower(sqlc.arg(q)::text)) > 0) ORDER BY np.full_name, v.version, v.id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- pypi: one row per distribution file; arch = python_version tag.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'pypi'::text AS format, COALESCE(t.python_version, '')::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.filename::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM pypi_packages WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'pypi') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY name, version, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- maven: one row per stored file (jar, pom, ...); name = group:artifact, arch = classifier, filename = repo path.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.group_id || ':' || t.artifact_id_coord::text AS name, t.version::text AS version,
           'maven'::text AS format, t.classifier::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.repo_path::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT id, repository_id, group_id, artifact_id_coord, version, classifier, extension, snapshot_timestamp, snapshot_build_number, is_snapshot, repo_path, created_at, updated_at, blob_id, md5, sha1, sha512, deleted_at, deleted_by FROM maven_artifacts WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'maven') AND (sqlc.arg(name_filter)::text = '' OR (group_id || ':' || artifact_id_coord) = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower((group_id || ':' || artifact_id_coord)), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY (group_id || ':' || artifact_id_coord), version, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- ivy: one row per artifact file; name = organisation:module.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.organisation || ':' || t.module::text AS name, t.revision::text AS version,
           'ivy'::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.artifact_name || '.' || t.artifact_ext::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM ivy_artifacts WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'ivy') AND (sqlc.arg(name_filter)::text = '' OR (organisation || ':' || module) = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower((organisation || ':' || module)), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(revision), lower(sqlc.arg(q)::text)) > 0) ORDER BY (organisation || ':' || module), revision, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- sbt: one row per artifact file; arch = scala_version.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.group_id || ':' || t.artifact_id::text AS name, t.version::text AS version,
           'sbt'::text AS format, t.scala_version::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.filename::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM sbt_artifacts WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'sbt') AND (sqlc.arg(name_filter)::text = '' OR (group_id || ':' || artifact_id) = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower((group_id || ':' || artifact_id)), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY (group_id || ':' || artifact_id), version, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- nuget: one row per package version.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.package_id::text AS name, t.version::text AS version,
           'nuget'::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.package_id || '.' || t.version || '.nupkg'::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM nuget_packages WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'nuget') AND (sqlc.arg(name_filter)::text = '' OR package_id = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(package_id), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY package_id, version, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- gomod: one row per module version; the zip blob is preferred, the .mod blob otherwise.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.module_path::text AS name, t.version::text AS version,
           'go'::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.version || CASE WHEN t.zip_blob_id IS NOT NULL THEN '.zip' ELSE '.mod' END::text AS filename, COALESCE(t.zip_blob_id, t.mod_blob_id)::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM gomod_modules WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'go') AND (sqlc.arg(name_filter)::text = '' OR module_path = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(module_path), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY module_path, version, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- cargo: one row per crate version (yanked included).
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'cargo'::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.name || '-' || t.version || '.crate'::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM cargo_crates WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'cargo') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY name, version, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- rubygems: one row per gem version/platform; arch = platform; a non-zero recorded size wins over the blob size.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'rubygems'::text AS format, t.platform::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULLIF(t.size, 0)::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.name || '-' || t.version || CASE WHEN t.platform = 'ruby' THEN '' ELSE '-' || t.platform END || '.gem'::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM rubygems_gems WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'rubygems') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY name, version, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- conda: one row per build; arch = platform (subdir), release = build_string.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'conda'::text AS format, t.platform::text AS arch, t.build_string::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.name || '-' || t.version || '-' || t.build_string || t.extension::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM conda_packages WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'conda') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY name, version, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- conan: one row per recipe revision (its conanmanifest.txt, or a pre-0.5.0 row with no file name);
    -- release = recipe_revision, filename = full reference.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'conan'::text AS format, ''::text AS arch, t.recipe_revision::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.name || '/' || t.version || '@' || t.user_channel || '#' || t.recipe_revision::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM conan_packages WHERE deleted_at IS NULL AND package_id = '' AND filename IN ('', 'conanmanifest.txt') AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'conan') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY name, version, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- helm: one row per chart version.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.name::text AS name, t.version::text AS version,
           'helm'::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.name || '-' || t.version || '.tgz'::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM helm_charts WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'helm') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0) ORDER BY name, version, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- raw: one row per stored path; name = filename = path, no version.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.path::text AS name, ''::text AS version,
           'raw'::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.path::text AS filename, t.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM raw_artifacts WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'raw') AND (sqlc.arg(name_filter)::text = '' OR path = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(path), lower(sqlc.arg(q)::text)) > 0) ORDER BY path, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- proxy cache: one row per cached upstream file of a proxy repository (any format); name = filename = upstream-relative path; the blob is resolved by sha256 and wins over the recorded size.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, t.cache_key::text AS name, ''::text AS version,
           t.repo_format::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, NULL::bigint::bigint AS pref_size, t.size_bytes::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.cache_key::text AS filename, NULL::uuid AS blob_id, t.blob_key::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT p.*, r.format AS repo_format FROM proxy_cache_entries p JOIN repositories r ON r.id = p.repository_id WHERE r.deleted_at IS NULL AND p.repository_id = sqlc.arg(repo_id)::uuid AND p.status_code = 200 AND p.blob_key IS NOT NULL AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = r.format) AND (sqlc.arg(name_filter)::text = '' OR cache_key = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(cache_key), lower(sqlc.arg(q)::text)) > 0) ORDER BY cache_key, p.id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- docker: one row per tag (the registry image name is the repository name, plus the image path inside it); size = manifest size, blob = manifest blob, filename = manifest digest.
    SELECT t.id::uuid AS id, t.repository_id::uuid AS repository_id, (CASE WHEN t.image = '' THEN r.name ELSE r.name || '/' || t.image END)::text AS name, t.name::text AS version,
           'docker'::text AS format, ''::text AS arch, ''::text::text AS release, 0::int::int AS epoch, 0::bigint::bigint AS build_time,
           0::bigint::bigint AS archive_size, COALESCE(dm.size, 0)::bigint AS pref_size, NULL::bigint::bigint AS fb_size, 0::bigint::bigint AS installed_size,
           t.manifest_digest::text AS filename, dm.blob_id::uuid AS blob_id, NULL::text::text AS blob_key, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at
    FROM (SELECT * FROM docker_tags WHERE deleted_at IS NULL AND repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'docker') AND (sqlc.arg(name_filter)::text = '' OR (CASE WHEN image = '' THEN (SELECT r.name FROM repositories r WHERE r.id = repository_id) ELSE (SELECT r.name FROM repositories r WHERE r.id = repository_id) || '/' || image END) = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower((CASE WHEN image = '' THEN (SELECT r.name FROM repositories r WHERE r.id = repository_id) ELSE (SELECT r.name FROM repositories r WHERE r.id = repository_id) || '/' || image END)), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0) ORDER BY (CASE WHEN image = '' THEN (SELECT r.name FROM repositories r WHERE r.id = repository_id) ELSE (SELECT r.name FROM repositories r WHERE r.id = repository_id) || '/' || image END), name, id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    JOIN repositories r ON r.id = t.repository_id AND r.deleted_at IS NULL
    LEFT JOIN docker_manifests dm ON dm.repository_id = t.repository_id AND dm.image = t.image AND dm.digest = t.manifest_digest AND dm.deleted_at IS NULL
    ) AS u
    ORDER BY u.name, u.version, u.id
    LIMIT sqlc.arg(page_limit)::int OFFSET sqlc.arg(page_offset)::int
) AS p
LEFT JOIN LATERAL (SELECT sha256, size_bytes FROM blobs WHERE id = p.blob_id) AS bi ON true
LEFT JOIN LATERAL (SELECT sha256, size_bytes FROM blobs WHERE sha256 = p.blob_key) AS bs ON true
ORDER BY p.name, p.version, p.id;

-- name: CountRepoComponents :one
SELECT COUNT(*)::bigint FROM (
    -- deb
    SELECT 1 FROM deb_packages t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'deb') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0)
    UNION ALL
    -- rpm
    SELECT 1 FROM rpm_packages t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'rpm') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0)
    UNION ALL
    -- npm
    SELECT 1 FROM npm_versions t JOIN npm_packages np ON np.id = t.package_id
    WHERE t.deleted_at IS NULL AND np.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'npm') AND (sqlc.arg(name_filter)::text = '' OR np.full_name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(np.full_name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(t.version), lower(sqlc.arg(q)::text)) > 0)
    UNION ALL
    -- pypi
    SELECT 1 FROM pypi_packages t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'pypi') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0)
    UNION ALL
    -- maven
    SELECT 1 FROM maven_artifacts t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'maven') AND (sqlc.arg(name_filter)::text = '' OR (group_id || ':' || artifact_id_coord) = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower((group_id || ':' || artifact_id_coord)), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0)
    UNION ALL
    -- ivy
    SELECT 1 FROM ivy_artifacts t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'ivy') AND (sqlc.arg(name_filter)::text = '' OR (organisation || ':' || module) = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower((organisation || ':' || module)), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(revision), lower(sqlc.arg(q)::text)) > 0)
    UNION ALL
    -- sbt
    SELECT 1 FROM sbt_artifacts t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'sbt') AND (sqlc.arg(name_filter)::text = '' OR (group_id || ':' || artifact_id) = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower((group_id || ':' || artifact_id)), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0)
    UNION ALL
    -- nuget
    SELECT 1 FROM nuget_packages t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'nuget') AND (sqlc.arg(name_filter)::text = '' OR package_id = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(package_id), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0)
    UNION ALL
    -- gomod
    SELECT 1 FROM gomod_modules t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'go') AND (sqlc.arg(name_filter)::text = '' OR module_path = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(module_path), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0)
    UNION ALL
    -- cargo
    SELECT 1 FROM cargo_crates t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'cargo') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0)
    UNION ALL
    -- rubygems
    SELECT 1 FROM rubygems_gems t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'rubygems') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0)
    UNION ALL
    -- conda
    SELECT 1 FROM conda_packages t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'conda') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0)
    UNION ALL
    -- conan
    SELECT 1 FROM conan_packages t
    WHERE t.deleted_at IS NULL AND t.package_id = '' AND t.filename IN ('', 'conanmanifest.txt') AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'conan') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0)
    UNION ALL
    -- helm
    SELECT 1 FROM helm_charts t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'helm') AND (sqlc.arg(name_filter)::text = '' OR name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(version), lower(sqlc.arg(q)::text)) > 0)
    UNION ALL
    -- raw
    SELECT 1 FROM raw_artifacts t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'raw') AND (sqlc.arg(name_filter)::text = '' OR path = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(path), lower(sqlc.arg(q)::text)) > 0)
    UNION ALL
    -- proxy cache
    SELECT 1 FROM proxy_cache_entries t JOIN repositories r ON r.id = t.repository_id
    WHERE r.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.status_code = 200 AND t.blob_key IS NOT NULL
      AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = r.format) AND (sqlc.arg(name_filter)::text = '' OR cache_key = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(cache_key), lower(sqlc.arg(q)::text)) > 0)
    UNION ALL
    -- docker
    SELECT 1 FROM docker_tags t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'docker') AND (sqlc.arg(name_filter)::text = '' OR (CASE WHEN image = '' THEN (SELECT r.name FROM repositories r WHERE r.id = repository_id) ELSE (SELECT r.name FROM repositories r WHERE r.id = repository_id) || '/' || image END) = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower((CASE WHEN image = '' THEN (SELECT r.name FROM repositories r WHERE r.id = repository_id) ELSE (SELECT r.name FROM repositories r WHERE r.id = repository_id) || '/' || image END)), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(name), lower(sqlc.arg(q)::text)) > 0)
) AS p;

-- Download lookup for one listed package (backs
-- GET /api/v1/repositories/{name}/packages/{id}/download). Every branch
-- mirrors the same-named ListRepoComponents branch (same id, repository scope,
-- blob join and filename expression), so an id the listing returns resolves
-- here to the blob it shows. Rows without a blob (conan recipes without one)
-- come back with an empty sha256. Keep in sync with ListRepoComponents.

-- name: GetRepoComponentBlob :one
SELECT id, format, filename, name, version, sha256, size_bytes, media_type
FROM (
    SELECT t.id::uuid AS id, 'deb'::text AS format, COALESCE(t.filename, '')::text AS filename, t.name::text AS name, t.version::text AS version,
           COALESCE(b.sha256, '')::text AS sha256, COALESCE(b.size_bytes, 0)::bigint AS size_bytes, COALESCE(b.media_type, '')::text AS media_type
    FROM deb_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'rpm'::text, COALESCE(t.filename, '')::text, t.name::text, t.version::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM rpm_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'npm'::text, t.tarball_filename::text, np.full_name::text, t.version::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM npm_versions t JOIN npm_packages np ON np.id = t.package_id LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.deleted_at IS NULL AND np.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'pypi'::text, t.filename::text, t.name::text, t.version::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM pypi_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'maven'::text, t.repo_path::text, t.group_id || ':' || t.artifact_id_coord::text, t.version::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM maven_artifacts t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'ivy'::text, t.artifact_name || '.' || t.artifact_ext::text, t.organisation || ':' || t.module::text, t.revision::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM ivy_artifacts t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'sbt'::text, t.filename::text, t.group_id || ':' || t.artifact_id::text, t.version::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM sbt_artifacts t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'nuget'::text, t.package_id || '.' || t.version || '.nupkg'::text, t.package_id::text, t.version::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM nuget_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'go'::text, t.version || CASE WHEN t.zip_blob_id IS NOT NULL THEN '.zip' ELSE '.mod' END::text, t.module_path::text, t.version::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM gomod_modules t LEFT JOIN blobs b ON b.id = COALESCE(t.zip_blob_id, t.mod_blob_id)
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'cargo'::text, t.name || '-' || t.version || '.crate'::text, t.name::text, t.version::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM cargo_crates t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'rubygems'::text, t.name || '-' || t.version || CASE WHEN t.platform = 'ruby' THEN '' ELSE '-' || t.platform END || '.gem'::text, t.name::text, t.version::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM rubygems_gems t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'conda'::text, t.name || '-' || t.version || '-' || t.build_string || t.extension::text, t.name::text, t.version::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM conda_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'conan'::text, t.name || '/' || t.version || '@' || t.user_channel || '#' || t.recipe_revision::text, t.name::text, t.version::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM conan_packages t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'helm'::text, t.name || '-' || t.version || '.tgz'::text, t.name::text, t.version::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM helm_charts t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT t.id::uuid, 'raw'::text, t.path::text, t.path::text, ''::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.media_type, '')::text
    FROM raw_artifacts t LEFT JOIN blobs b ON b.id = t.blob_id
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- proxy cache: a cached upstream file; the filename is its upstream-relative path.
    SELECT t.id::uuid, r.format::text, t.cache_key::text, t.cache_key::text, ''::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, t.size_bytes, 0)::bigint, COALESCE(NULLIF(t.content_type, ''), b.media_type, '')::text
    FROM proxy_cache_entries t JOIN repositories r ON r.id = t.repository_id LEFT JOIN blobs b ON b.sha256 = t.blob_key
    WHERE r.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid AND t.status_code = 200 AND t.blob_key IS NOT NULL
    UNION ALL
    -- docker: the manifest blob; media type is the manifest's own.
    SELECT t.id::uuid, 'docker'::text, t.manifest_digest::text, (CASE WHEN t.image = '' THEN r.name ELSE r.name || '/' || t.image END)::text, t.name::text,
           COALESCE(b.sha256, '')::text, COALESCE(b.size_bytes, 0)::bigint, COALESCE(dm.media_type, '')::text
    FROM docker_tags t JOIN repositories r ON r.id = t.repository_id AND r.deleted_at IS NULL LEFT JOIN docker_manifests dm ON dm.repository_id = t.repository_id AND dm.image = t.image AND dm.digest = t.manifest_digest AND dm.deleted_at IS NULL LEFT JOIN blobs b ON b.id = dm.blob_id
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
) AS p
LIMIT 1;
