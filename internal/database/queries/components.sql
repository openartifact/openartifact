-- Repository package listing across every format (backs
-- GET /api/v1/repositories/{name}/packages). One generic branch reads the
-- listed live files of package_files (every format but docker) and maps them
-- onto the shared models.Component shape; two pseudo-branches add the docker
-- tags and the proxy cache entries. Columns with no equivalent default to ''/0.
-- Where the shape comes from (see the format specs for the keys):
--   name = packages.name, version = package_versions.version, format =
--   packages.format, arch = package_files.arch, filename = package_files.filename;
--   release, epoch, build_time, archive_size, installed_size = package_files.attrs
--   keys of that name; package_size = COALESCE(size_bytes, blob size, 0).
-- A file the plugin marks unlisted (go .mod, conan binaries) never shows.
-- Keep ListRepoComponents, ListRepoComponentsByName and CountRepoComponents in
-- sync (same branches, same filters). name_filter is an exact match on the
-- listed name; q is a case-insensitive substring match on the listed name or
-- version (on the name only for proxy cache entries).
--
-- ListRepoComponentsByName is ListRepoComponents in (name, version, id) order
-- instead of newest first: its stage 1 sorts the repository's files by that
-- order, so it backs the browser's "sort by name" but not the default listing.
--
-- The listing is built in two stages so that no row outside the requested page
-- ever touches the versions, packages or blobs tables: every branch first picks
-- its own top (page_limit + page_offset) rows in the final (created_at DESC, id)
-- order and carries the raw file columns; for the generic branch that is an
-- ordered scan of idx_package_files_listing that stops at the limit. With none
-- of format_filter, name_filter and q stage 1 reads package_files alone; the
-- other two generic branches, with versions and packages joined for those
-- predicates, are switched on only when one of them is set (a one-time filter
-- skips the others): one for name_filter, whose equality lets the planner start
-- from packages, one for format_filter and q, which walk the listing index. The union is sorted and cut to the page, and only then are the name,
-- version and format (versions/packages by primary key), the attrs columns and
-- the blob sha256/size resolved. pref_size/fb_size carry the size rules
-- (package_size = COALESCE(pref_size, blob size, fb_size, 0)); blob_id or
-- blob_key (proxy cache, resolved by sha256) locate the blob. The by-name
-- variant needs the name to sort, so its stage 1 always joins and carries it.
-- name: ListRepoComponents :many
SELECT p.id::uuid AS id, p.repository_id::uuid AS repository_id, COALESCE(p.name, pk.name)::text AS name, COALESCE(p.version, v.version)::text AS version,
       COALESCE(p.format, pk.format)::text AS format, p.arch::text AS arch,
       COALESCE(COALESCE(bi.sha256, bs.sha256), '')::text AS sha256,
       COALESCE(p.attrs->>'release', '')::text AS release, COALESCE((p.attrs->>'epoch')::int, 0)::int AS epoch,
       COALESCE((p.attrs->>'build_time')::bigint, 0)::bigint AS build_time,
       COALESCE((p.attrs->>'archive_size')::bigint, 0)::bigint AS archive_size,
       COALESCE(p.pref_size, bi.size_bytes, bs.size_bytes, p.fb_size, 0)::bigint AS package_size,
       COALESCE((p.attrs->>'installed_size')::bigint, 0)::bigint AS installed_size, p.filename::text AS filename,
       p.created_at::timestamptz AS created_at, p.updated_at::timestamptz AS updated_at
FROM (
    SELECT * FROM (
    -- generic files (every format but docker): stage 1 cuts the top rows of the repository's listed live files
    -- from package_files (plus versions/packages only when the filters need them); name, version and format are
    -- resolved for the page rows only (here only when the sort needs them).
    SELECT f.id AS id, f.repository_id AS repository_id, f.version_id AS version_id, f.pname AS name, f.pversion AS version, f.pformat AS format,
           f.arch AS arch, f.attrs AS attrs, f.size_bytes AS pref_size, NULL::bigint AS fb_size,
           f.filename AS filename, f.blob_id AS blob_id, NULL::text AS blob_key, f.created_at AS created_at, f.updated_at AS updated_at
    FROM (
        (SELECT f.id, f.repository_id, f.version_id, f.filename, f.arch, f.attrs, f.size_bytes, f.blob_id, f.created_at, f.updated_at, NULL::text AS pname, NULL::text AS pversion, NULL::text AS pformat FROM package_files f
         WHERE f.repository_id = sqlc.arg(repo_id)::uuid AND f.deleted_at IS NULL AND f.listed AND sqlc.arg(format_filter)::text = '' AND sqlc.arg(name_filter)::text = '' AND sqlc.arg(q)::text = ''
         ORDER BY f.created_at DESC, f.id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int))
        UNION ALL
        (SELECT f.id, f.repository_id, f.version_id, f.filename, f.arch, f.attrs, f.size_bytes, f.blob_id, f.created_at, f.updated_at, NULL::text, NULL::text, NULL::text FROM package_files f JOIN package_versions v ON v.id = f.version_id JOIN packages pk ON pk.id = v.package_id
         WHERE f.repository_id = sqlc.arg(repo_id)::uuid AND f.deleted_at IS NULL AND f.listed AND sqlc.arg(name_filter)::text <> '' AND (sqlc.arg(format_filter)::text = '' OR pk.format = sqlc.arg(format_filter)::text) AND pk.name = sqlc.arg(name_filter)::text AND (sqlc.arg(q)::text = '' OR strpos(lower(pk.name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(v.version), lower(sqlc.arg(q)::text)) > 0)
         ORDER BY f.created_at DESC, f.id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int))
        UNION ALL
        (SELECT f.id, f.repository_id, f.version_id, f.filename, f.arch, f.attrs, f.size_bytes, f.blob_id, f.created_at, f.updated_at, NULL::text, NULL::text, NULL::text FROM package_files f JOIN package_versions v ON v.id = f.version_id JOIN packages pk ON pk.id = v.package_id
         WHERE f.repository_id = sqlc.arg(repo_id)::uuid AND f.deleted_at IS NULL AND f.listed AND sqlc.arg(name_filter)::text = '' AND (sqlc.arg(format_filter)::text <> '' OR sqlc.arg(q)::text <> '') AND (sqlc.arg(format_filter)::text = '' OR pk.format = sqlc.arg(format_filter)::text) AND (sqlc.arg(name_filter)::text = '' OR pk.name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(pk.name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(v.version), lower(sqlc.arg(q)::text)) > 0)
         ORDER BY f.created_at DESC, f.id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int))
    ) f
    UNION ALL
    -- proxy cache: one row per cached upstream file of a proxy repository (any format); name = filename = upstream-relative path; the blob is resolved by sha256 and wins over the recorded size.
    SELECT t.id::uuid, t.repository_id::uuid, NULL::uuid, t.cache_key::text, ''::text, t.repo_format::text,
           ''::text, NULL::jsonb, NULL::bigint, t.size_bytes::bigint,
           t.cache_key::text, NULL::uuid, t.blob_key::text, t.created_at::timestamptz, t.updated_at::timestamptz
    FROM (SELECT t.*, r.format AS repo_format FROM proxy_cache_entries t JOIN repositories r ON r.id = t.repository_id WHERE r.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.status_code = 200 AND t.blob_key IS NOT NULL AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = r.format) AND (sqlc.arg(name_filter)::text = '' OR t.cache_key = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(t.cache_key), lower(sqlc.arg(q)::text)) > 0) ORDER BY t.created_at DESC, t.id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- docker: one row per tag (the registry image name is the repository name, plus the image path inside it); size = manifest size, blob = manifest blob, filename = manifest digest.
    SELECT t.id::uuid, t.repository_id::uuid, NULL::uuid, (CASE WHEN t.image = '' THEN r.name ELSE r.name || '/' || t.image END)::text, t.name::text, 'docker'::text,
           ''::text, NULL::jsonb, COALESCE(dm.size, 0)::bigint, NULL::bigint,
           t.manifest_digest::text, dm.blob_id::uuid, NULL::text, t.created_at::timestamptz, t.updated_at::timestamptz
    FROM (SELECT t.* FROM docker_tags t WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'docker') AND (sqlc.arg(name_filter)::text = '' OR (CASE WHEN t.image = '' THEN (SELECT r.name FROM repositories r WHERE r.id = t.repository_id) ELSE (SELECT r.name FROM repositories r WHERE r.id = t.repository_id) || '/' || t.image END) = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower((CASE WHEN t.image = '' THEN (SELECT r.name FROM repositories r WHERE r.id = t.repository_id) ELSE (SELECT r.name FROM repositories r WHERE r.id = t.repository_id) || '/' || t.image END)), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(t.name), lower(sqlc.arg(q)::text)) > 0) ORDER BY t.created_at DESC, t.id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    JOIN repositories r ON r.id = t.repository_id AND r.deleted_at IS NULL
    LEFT JOIN docker_manifests dm ON dm.repository_id = t.repository_id AND dm.image = t.image AND dm.digest = t.manifest_digest AND dm.deleted_at IS NULL
    ) AS u
    ORDER BY u.created_at DESC, u.id
    LIMIT sqlc.arg(page_limit)::int OFFSET sqlc.arg(page_offset)::int
) AS p
LEFT JOIN package_versions v ON v.id = p.version_id
LEFT JOIN packages pk ON pk.id = v.package_id
LEFT JOIN LATERAL (SELECT sha256, size_bytes FROM blobs WHERE id = p.blob_id) AS bi ON true
LEFT JOIN LATERAL (SELECT sha256, size_bytes FROM blobs WHERE sha256 = p.blob_key) AS bs ON true
ORDER BY p.created_at DESC, p.id;

-- name: ListRepoComponentsByName :many
SELECT p.id::uuid AS id, p.repository_id::uuid AS repository_id, COALESCE(p.name, pk.name)::text AS name, COALESCE(p.version, v.version)::text AS version,
       COALESCE(p.format, pk.format)::text AS format, p.arch::text AS arch,
       COALESCE(COALESCE(bi.sha256, bs.sha256), '')::text AS sha256,
       COALESCE(p.attrs->>'release', '')::text AS release, COALESCE((p.attrs->>'epoch')::int, 0)::int AS epoch,
       COALESCE((p.attrs->>'build_time')::bigint, 0)::bigint AS build_time,
       COALESCE((p.attrs->>'archive_size')::bigint, 0)::bigint AS archive_size,
       COALESCE(p.pref_size, bi.size_bytes, bs.size_bytes, p.fb_size, 0)::bigint AS package_size,
       COALESCE((p.attrs->>'installed_size')::bigint, 0)::bigint AS installed_size, p.filename::text AS filename,
       p.created_at::timestamptz AS created_at, p.updated_at::timestamptz AS updated_at
FROM (
    SELECT * FROM (
    -- generic files (every format but docker): stage 1 cuts the top rows of the repository's listed live files
    -- from package_files (plus versions/packages only when the filters need them); name, version and format are
    -- resolved for the page rows only (here only when the sort needs them).
    SELECT f.id AS id, f.repository_id AS repository_id, f.version_id AS version_id, f.pname AS name, f.pversion AS version, f.pformat AS format,
           f.arch AS arch, f.attrs AS attrs, f.size_bytes AS pref_size, NULL::bigint AS fb_size,
           f.filename AS filename, f.blob_id AS blob_id, NULL::text AS blob_key, f.created_at AS created_at, f.updated_at AS updated_at
    FROM (
        (SELECT f.id, f.repository_id, f.version_id, f.filename, f.arch, f.attrs, f.size_bytes, f.blob_id, f.created_at, f.updated_at, pk.name AS pname, v.version AS pversion, pk.format AS pformat FROM package_files f JOIN package_versions v ON v.id = f.version_id JOIN packages pk ON pk.id = v.package_id
         WHERE f.repository_id = sqlc.arg(repo_id)::uuid AND f.deleted_at IS NULL AND f.listed AND (sqlc.arg(format_filter)::text = '' OR pk.format = sqlc.arg(format_filter)::text) AND (sqlc.arg(name_filter)::text = '' OR pk.name = sqlc.arg(name_filter)::text)
           AND (sqlc.arg(q)::text = '' OR strpos(lower(pk.name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(v.version), lower(sqlc.arg(q)::text)) > 0)
         ORDER BY pk.name, v.version, f.id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int))
    ) f
    UNION ALL
    -- proxy cache: one row per cached upstream file of a proxy repository (any format); name = filename = upstream-relative path; the blob is resolved by sha256 and wins over the recorded size.
    SELECT t.id::uuid, t.repository_id::uuid, NULL::uuid, t.cache_key::text, ''::text, t.repo_format::text,
           ''::text, NULL::jsonb, NULL::bigint, t.size_bytes::bigint,
           t.cache_key::text, NULL::uuid, t.blob_key::text, t.created_at::timestamptz, t.updated_at::timestamptz
    FROM (SELECT t.*, r.format AS repo_format FROM proxy_cache_entries t JOIN repositories r ON r.id = t.repository_id WHERE r.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.status_code = 200 AND t.blob_key IS NOT NULL AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = r.format) AND (sqlc.arg(name_filter)::text = '' OR t.cache_key = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(t.cache_key), lower(sqlc.arg(q)::text)) > 0) ORDER BY t.cache_key, t.id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    UNION ALL
    -- docker: one row per tag (the registry image name is the repository name, plus the image path inside it); size = manifest size, blob = manifest blob, filename = manifest digest.
    SELECT t.id::uuid, t.repository_id::uuid, NULL::uuid, (CASE WHEN t.image = '' THEN r.name ELSE r.name || '/' || t.image END)::text, t.name::text, 'docker'::text,
           ''::text, NULL::jsonb, COALESCE(dm.size, 0)::bigint, NULL::bigint,
           t.manifest_digest::text, dm.blob_id::uuid, NULL::text, t.created_at::timestamptz, t.updated_at::timestamptz
    FROM (SELECT t.* FROM docker_tags t WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'docker') AND (sqlc.arg(name_filter)::text = '' OR (CASE WHEN t.image = '' THEN (SELECT r.name FROM repositories r WHERE r.id = t.repository_id) ELSE (SELECT r.name FROM repositories r WHERE r.id = t.repository_id) || '/' || t.image END) = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower((CASE WHEN t.image = '' THEN (SELECT r.name FROM repositories r WHERE r.id = t.repository_id) ELSE (SELECT r.name FROM repositories r WHERE r.id = t.repository_id) || '/' || t.image END)), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(t.name), lower(sqlc.arg(q)::text)) > 0) ORDER BY (CASE WHEN t.image = '' THEN (SELECT r.name FROM repositories r WHERE r.id = t.repository_id) ELSE (SELECT r.name FROM repositories r WHERE r.id = t.repository_id) || '/' || t.image END), t.name, t.id LIMIT (sqlc.arg(page_limit)::int + sqlc.arg(page_offset)::int)) t
    JOIN repositories r ON r.id = t.repository_id AND r.deleted_at IS NULL
    LEFT JOIN docker_manifests dm ON dm.repository_id = t.repository_id AND dm.image = t.image AND dm.digest = t.manifest_digest AND dm.deleted_at IS NULL
    ) AS u
    ORDER BY u.name, u.version, u.id
    LIMIT sqlc.arg(page_limit)::int OFFSET sqlc.arg(page_offset)::int
) AS p
LEFT JOIN package_versions v ON v.id = p.version_id
LEFT JOIN packages pk ON pk.id = v.package_id
LEFT JOIN LATERAL (SELECT sha256, size_bytes FROM blobs WHERE id = p.blob_id) AS bi ON true
LEFT JOIN LATERAL (SELECT sha256, size_bytes FROM blobs WHERE sha256 = p.blob_key) AS bs ON true
ORDER BY p.name, p.version, p.id;

-- name: CountRepoComponents :one
SELECT (
    -- generic files without name/q filter: package_files alone (index-only scan of idx_package_files_listing)
    (SELECT COUNT(*) FROM package_files f WHERE f.repository_id = sqlc.arg(repo_id)::uuid AND f.deleted_at IS NULL AND f.listed AND sqlc.arg(format_filter)::text = '' AND sqlc.arg(name_filter)::text = '' AND sqlc.arg(q)::text = '')
    + (SELECT COUNT(*) FROM package_files f JOIN package_versions v ON v.id = f.version_id JOIN packages pk ON pk.id = v.package_id WHERE f.repository_id = sqlc.arg(repo_id)::uuid AND f.deleted_at IS NULL AND f.listed AND sqlc.arg(name_filter)::text <> '' AND (sqlc.arg(format_filter)::text = '' OR pk.format = sqlc.arg(format_filter)::text) AND pk.name = sqlc.arg(name_filter)::text AND (sqlc.arg(q)::text = '' OR strpos(lower(pk.name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(v.version), lower(sqlc.arg(q)::text)) > 0))
    + (SELECT COUNT(*) FROM package_files f JOIN package_versions v ON v.id = f.version_id JOIN packages pk ON pk.id = v.package_id WHERE f.repository_id = sqlc.arg(repo_id)::uuid AND f.deleted_at IS NULL AND f.listed AND sqlc.arg(name_filter)::text = '' AND (sqlc.arg(format_filter)::text <> '' OR sqlc.arg(q)::text <> '') AND (sqlc.arg(format_filter)::text = '' OR pk.format = sqlc.arg(format_filter)::text) AND (sqlc.arg(name_filter)::text = '' OR pk.name = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(pk.name), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(v.version), lower(sqlc.arg(q)::text)) > 0))
    -- proxy cache
    + (SELECT COUNT(*) FROM proxy_cache_entries t JOIN repositories r ON r.id = t.repository_id WHERE r.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.status_code = 200 AND t.blob_key IS NOT NULL AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = r.format) AND (sqlc.arg(name_filter)::text = '' OR t.cache_key = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower(t.cache_key), lower(sqlc.arg(q)::text)) > 0))
    -- docker tags
    + (SELECT COUNT(*) FROM docker_tags t WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND (sqlc.arg(format_filter)::text = '' OR sqlc.arg(format_filter)::text = 'docker') AND (sqlc.arg(name_filter)::text = '' OR (CASE WHEN t.image = '' THEN (SELECT r.name FROM repositories r WHERE r.id = t.repository_id) ELSE (SELECT r.name FROM repositories r WHERE r.id = t.repository_id) || '/' || t.image END) = sqlc.arg(name_filter)::text) AND (sqlc.arg(q)::text = '' OR strpos(lower((CASE WHEN t.image = '' THEN (SELECT r.name FROM repositories r WHERE r.id = t.repository_id) ELSE (SELECT r.name FROM repositories r WHERE r.id = t.repository_id) || '/' || t.image END)), lower(sqlc.arg(q)::text)) > 0 OR strpos(lower(t.name), lower(sqlc.arg(q)::text)) > 0))
)::bigint;

-- Download lookup for one listed package (backs
-- GET /api/v1/repositories/{name}/packages/{id}/download). Every branch
-- mirrors the same-named ListRepoComponents branch (same id and repository
-- scope), so an id the listing returns resolves here to the blob it shows. Rows
-- without a blob (a conan recipe without one) come back with an empty sha256.

-- name: GetRepoComponentBlob :one
SELECT id, format, filename, name, version, sha256, size_bytes, media_type
FROM (
    -- generic files: the listed live file of the repository; the size is the blob's
    SELECT f.id::uuid AS id, pk.format::text AS format, COALESCE(f.filename, '')::text AS filename, pk.name::text AS name, v.version::text AS version,
           COALESCE(b.sha256, '')::text AS sha256, COALESCE(b.size_bytes, 0)::bigint AS size_bytes, COALESCE(b.media_type, '')::text AS media_type
    FROM package_files f
    JOIN package_versions v ON v.id = f.version_id
    JOIN packages pk ON pk.id = v.package_id
    LEFT JOIN blobs b ON b.id = f.blob_id
    WHERE f.id = sqlc.arg(id)::uuid AND f.repository_id = sqlc.arg(repo_id)::uuid AND f.deleted_at IS NULL AND f.listed
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

-- Display names of listed package rows by id (the finding lists show a scanned
-- artifact as name and version, and scans of the package pages record the row
-- id as artifact_id). Soft-deleted rows resolve too, so a finding of a trashed
-- package keeps its name.

-- name: ResolveComponentNames :many
SELECT repository_id, id, name, version
FROM (
    SELECT f.repository_id::uuid AS repository_id, f.id::uuid AS id, pk.name::text AS name, v.version::text AS version
    FROM package_files f
    JOIN package_versions v ON v.id = f.version_id
    JOIN packages pk ON pk.id = v.package_id
    WHERE f.id = ANY(sqlc.arg(ids)::uuid[])
    UNION ALL
    SELECT t.repository_id, t.id, t.cache_key::text, ''::text
    FROM proxy_cache_entries t WHERE t.id = ANY(sqlc.arg(ids)::uuid[])
    UNION ALL
    SELECT t.repository_id, t.id, (CASE WHEN t.image = '' THEN r.name ELSE r.name || '/' || t.image END)::text, t.name::text
    FROM docker_tags t JOIN repositories r ON r.id = t.repository_id WHERE t.id = ANY(sqlc.arg(ids)::uuid[])
) AS p;
