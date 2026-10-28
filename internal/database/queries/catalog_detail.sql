-- One package version of a repository, with the metadata its table stores
-- (backs GET /api/v1/repositories/{name}/packages/{id}). The id is a row id of
-- the package listing, as for GetRepoComponentBlob, and has one branch per
-- package table like it: keep the branches of both in sync with ListRepoComponents.
--
-- info is a flat object of display fields (null or empty values are dropped by
-- the caller); deps is the raw column the dependencies are read from, whose
-- shape is per format and decoded in internal/api (artifact_detail.go).
-- pref_size is the size the table records itself (it wins over the blob's, as in
-- the listing); blob_key is a proxy cache entry's blob, by sha256.

-- name: GetRepoComponentDetail :one
SELECT p.id::uuid AS id, p.format::text AS format, p.name::text AS name, p.version::text AS version,
       p.arch::text AS arch, p.filename::text AS filename,
       p.created_at::timestamptz AS created_at, p.updated_at::timestamptz AS updated_at,
       COALESCE(COALESCE(bi.sha256, bs.sha256), '')::text AS sha256,
       COALESCE(p.pref_size, bi.size_bytes, bs.size_bytes, p.fb_size, 0)::bigint AS size_bytes,
       p.summary::text AS summary, p.description::text AS description, p.license::text AS license, p.homepage::text AS homepage,
       p.md5::text AS md5, p.sha1::text AS sha1,
       p.info::jsonb AS info, p.deps::jsonb AS deps
FROM (
    -- deb
    SELECT t.id::uuid AS id, 'deb'::text AS format, t.name::text AS name, t.version::text AS version, t.arch::text AS arch,
           COALESCE(t.filename, '')::text AS filename, t.created_at::timestamptz AS created_at, t.updated_at::timestamptz AS updated_at,
           t.blob_id::uuid AS blob_id, NULL::text AS blob_key, NULL::bigint AS pref_size, NULL::bigint AS fb_size,
           COALESCE(t.summary, '')::text AS summary, COALESCE(t.description, '')::text AS description,
           COALESCE(t.license, '')::text AS license, COALESCE(t.homepage, '')::text AS homepage, ''::text AS md5, ''::text AS sha1,
           jsonb_build_object('maintainer', COALESCE(t.maintainer, ''), 'installed_size', COALESCE(t.installed_size, 0)::text)::jsonb AS info,
           t.control_fields::jsonb AS deps
    FROM deb_packages t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- rpm
    SELECT t.id::uuid, 'rpm'::text, t.name::text, t.version::text, t.arch::text,
           COALESCE(t.filename, '')::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, t.package_size::bigint, NULL::bigint,
           COALESCE(t.summary, '')::text, COALESCE(t.description, '')::text,
           COALESCE(t.license, '')::text, COALESCE(t.url, '')::text, ''::text, ''::text,
           jsonb_build_object('release', t.release, 'epoch', t.epoch::text, 'vendor', COALESCE(t.vendor, ''),
                              'packager', COALESCE(t.packager, ''), 'group', COALESCE(t.group_name, ''),
                              'source_rpm', COALESCE(t.source_rpm, ''), 'build_host', COALESCE(t.build_host, ''),
                              'installed_size', COALESCE(t.installed_size, 0)::text)::jsonb,
           t.rpm_extra::jsonb
    FROM rpm_packages t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- npm
    SELECT t.id::uuid, 'npm'::text, np.full_name::text, t.version::text, ''::text,
           t.tarball_filename::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, COALESCE(t.manifest_json->>'description', '')::text,
           CASE WHEN jsonb_typeof(t.manifest_json->'license') = 'string' THEN t.manifest_json->>'license' ELSE '' END::text,
           CASE WHEN jsonb_typeof(t.manifest_json->'homepage') = 'string' THEN t.manifest_json->>'homepage' ELSE '' END::text,
           ''::text, t.shasum::text,
           jsonb_build_object('integrity', t.integrity, 'scope', np.scope)::jsonb,
           t.manifest_json::jsonb
    FROM npm_versions t JOIN npm_packages np ON np.id = t.package_id
    WHERE t.deleted_at IS NULL AND np.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- pypi
    SELECT t.id::uuid, 'pypi'::text, t.name::text, t.version::text, COALESCE(t.python_version, '')::text,
           t.filename::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           COALESCE(t.summary, '')::text, ''::text, COALESCE(t.license, '')::text, ''::text, ''::text, ''::text,
           jsonb_build_object('package_type', t.package_type, 'requires_python', COALESCE(t.requires_python, ''),
                              'author', COALESCE(t.author, ''), 'author_email', COALESCE(t.author_email, ''),
                              'keywords', COALESCE(t.keywords, ''), 'classifiers', array_to_string(t.classifiers, E'\n'))::jsonb,
           NULL::jsonb
    FROM pypi_packages t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- maven
    SELECT t.id::uuid, 'maven'::text, t.group_id || ':' || t.artifact_id_coord::text, t.version::text, t.classifier::text,
           t.repo_path::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, ''::text, ''::text, ''::text, t.md5::text, t.sha1::text,
           jsonb_build_object('group_id', t.group_id, 'artifact_id', t.artifact_id_coord, 'classifier', t.classifier,
                              'extension', t.extension, 'snapshot', t.is_snapshot::text,
                              'snapshot_timestamp', COALESCE(t.snapshot_timestamp, ''))::jsonb,
           NULL::jsonb
    FROM maven_artifacts t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- ivy
    SELECT t.id::uuid, 'ivy'::text, t.organisation || ':' || t.module::text, t.revision::text, ''::text,
           t.artifact_name || '.' || t.artifact_ext::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, ''::text, ''::text, ''::text, ''::text, ''::text,
           jsonb_build_object('organisation', t.organisation, 'module', t.module, 'artifact_type', t.artifact_type,
                              'artifact_ext', t.artifact_ext)::jsonb,
           NULL::jsonb
    FROM ivy_artifacts t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- sbt
    SELECT t.id::uuid, 'sbt'::text, t.group_id || ':' || t.artifact_id::text, t.version::text, t.scala_version::text,
           t.filename::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, ''::text, ''::text, ''::text, ''::text, ''::text,
           jsonb_build_object('group_id', t.group_id, 'artifact_id', t.artifact_id, 'scala_version', t.scala_version)::jsonb,
           NULL::jsonb
    FROM sbt_artifacts t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- nuget
    SELECT t.id::uuid, 'nuget'::text, t.package_id::text, t.version::text, ''::text,
           t.package_id || '.' || t.version || '.nupkg'::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           COALESCE(t.summary, '')::text, COALESCE(t.description, '')::text, COALESCE(t.license_url, '')::text, COALESCE(t.project_url, '')::text,
           ''::text, ''::text,
           jsonb_build_object('title', COALESCE(t.title, ''), 'authors', COALESCE(t.authors, ''), 'owners', COALESCE(t.owners, ''),
                              'tags', COALESCE(t.tags, ''), 'min_client_version', COALESCE(t.min_client_version, ''),
                              'prerelease', t.is_prerelease::text, 'download_count', t.download_count::text)::jsonb,
           t.dependency_groups::jsonb
    FROM nuget_packages t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- go: the zip blob is preferred, the .mod blob otherwise
    SELECT t.id::uuid, 'go'::text, t.module_path::text, t.version::text, ''::text,
           t.version || CASE WHEN t.zip_blob_id IS NOT NULL THEN '.zip' ELSE '.mod' END::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           COALESCE(t.zip_blob_id, t.mod_blob_id)::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, ''::text, ''::text, ''::text, ''::text, ''::text,
           jsonb_build_object('module_path', t.module_path)::jsonb,
           to_jsonb(t.go_mod_content)::jsonb
    FROM gomod_modules t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- cargo
    SELECT t.id::uuid, 'cargo'::text, t.name::text, t.version::text, ''::text,
           t.name || '-' || t.version || '.crate'::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, COALESCE(t.description, '')::text, COALESCE(t.license, '')::text, COALESCE(t.homepage, '')::text, ''::text, ''::text,
           jsonb_build_object('documentation', COALESCE(t.documentation, ''), 'repository', COALESCE(t.repository, ''),
                              'keywords', array_to_string(t.keywords, ', '), 'categories', array_to_string(t.categories, ', '),
                              'authors', array_to_string(t.authors, ', '), 'yanked', t.yanked::text,
                              'download_count', t.download_count::text)::jsonb,
           t.metadata_json::jsonb
    FROM cargo_crates t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- rubygems
    SELECT t.id::uuid, 'rubygems'::text, t.name::text, t.version::text, t.platform::text,
           t.name || '-' || t.version || CASE WHEN t.platform = 'ruby' THEN '' ELSE '-' || t.platform END || '.gem'::text,
           t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULLIF(t.size, 0)::bigint, NULL::bigint,
           COALESCE(t.summary, '')::text, COALESCE(t.description, '')::text, array_to_string(t.licenses, ', ')::text, COALESCE(t.homepage, '')::text,
           ''::text, ''::text,
           jsonb_build_object('authors', array_to_string(t.authors, ', '), 'platform', t.platform, 'yanked', t.yanked::text,
                              'download_count', t.download_count::text)::jsonb,
           t.dependencies::jsonb
    FROM rubygems_gems t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- conda
    SELECT t.id::uuid, 'conda'::text, t.name::text, t.version::text, t.platform::text,
           t.name || '-' || t.version || '-' || t.build_string || t.extension::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, ''::text, ''::text, ''::text, t.md5::text, ''::text,
           jsonb_build_object('build_string', t.build_string, 'build_number', t.build_number::text, 'extension', t.extension,
                              'timestamp_ms', t.timestamp_ms::text)::jsonb,
           to_jsonb(t.depends)::jsonb
    FROM conda_packages t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- composer
    SELECT t.id::uuid, 'composer'::text, t.name::text, t.version::text, ''::text,
           (replace(t.name, '/', '-') || '-' || t.version || '.zip')::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, COALESCE(t.composer_json->>'description', '')::text, ''::text, COALESCE(t.composer_json->>'homepage', '')::text,
           ''::text, t.sha1::text,
           jsonb_build_object('version_normalized', t.version_normalized)::jsonb,
           COALESCE(t.composer_json->'require', '{}'::jsonb)::jsonb
    FROM composer_packages t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- ansible
    SELECT t.id::uuid, 'ansible'::text, (t.namespace || '.' || t.name)::text, t.version::text, ''::text,
           t.filename::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, ''::text, ''::text, ''::text, ''::text, ''::text,
           jsonb_build_object('namespace', t.namespace, 'requires_ansible', t.requires_ansible)::jsonb,
           t.dependencies::jsonb
    FROM ansible_collections t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- pacman
    SELECT t.id::uuid, 'pacman'::text, t.name::text, t.version::text, t.arch::text,
           t.filename::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, ''::text, ''::text, ''::text, t.md5::text, ''::text,
           jsonb_build_object('base', t.base, 'repo_arch', t.repo_arch, 'description', t.description, 'url', t.url,
                              'packager', t.packager, 'build_date', t.build_date::text, 'installed_size', t.installed_size::text,
                              'signature_source', t.signature_source)::jsonb,
           to_jsonb(t.depends)::jsonb
    FROM pacman_packages t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- nix: one row per store path; version = store hash
    SELECT t.id::uuid, 'nix'::text, t.name::text, t.store_hash::text, ''::text,
           (t.store_hash || '.narinfo')::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, ''::text, ''::text, ''::text, ''::text, ''::text,
           jsonb_build_object('nar_hash', t.nar_hash, 'nar_size', t.nar_size::text, 'nar_file', t.nar_file,
                              'compression', t.compression, 'deriver', t.deriver, 'ca', t.ca)::jsonb,
           to_jsonb(t.refs)::jsonb
    FROM nix_paths t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- conan: the recipe revision row
    SELECT t.id::uuid, 'conan'::text, t.name::text, t.version::text, ''::text,
           t.name || '/' || t.version || '@' || t.user_channel || '#' || t.recipe_revision::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, ''::text, ''::text, ''::text, t.md5::text, t.sha1::text,
           jsonb_build_object('user_channel', t.user_channel, 'recipe_revision', t.recipe_revision)::jsonb,
           NULL::jsonb
    FROM conan_packages t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- apk: arch = architecture; the filename is the .apk
    SELECT t.id::uuid, 'apk'::text, t.name::text, t.version::text, t.arch::text,
           t.name || '-' || t.version || '.apk'::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, t.description::text, t.license::text, t.url::text, ''::text, ''::text,
           jsonb_build_object('branch', t.branch, 'repo', t.repo, 'origin', t.origin, 'maintainer', t.maintainer,
                              'commit', t.commit, 'installed_size', t.installed_size::text, 'build_time', t.build_time::text,
                              'checksum', t.checksum, 'data_sha256', t.data_sha256,
                              'download_count', t.download_count::text)::jsonb,
           NULL::jsonb
    FROM apk_packages t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- terraform module
    SELECT t.id::uuid, 'terraform'::text, ('modules/' || t.name || '/' || t.system)::text, t.version::text, ''::text,
           t.name || '-' || t.system || '-' || t.version || '.' || t.archive_ext::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, ''::text, ''::text, ''::text, ''::text, ''::text,
           jsonb_build_object('kind', 'module', 'archive_ext', t.archive_ext, 'download_count', t.download_count::text)::jsonb,
           NULL::jsonb
    FROM terraform_modules t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- terraform provider: one platform zip; arch = OS_ARCH
    SELECT t.id::uuid, 'terraform'::text, ('providers/' || t.type)::text, t.version::text, (t.os || '_' || t.arch)::text,
           'terraform-provider-' || t.type || '_' || t.version || '_' || t.os || '_' || t.arch || '.zip'::text,
           t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, ''::text, ''::text, ''::text, ''::text, ''::text,
           jsonb_build_object('kind', 'provider', 'protocols', array_to_string(t.protocols, ', '),
                              'download_count', t.download_count::text)::jsonb,
           NULL::jsonb
    FROM terraform_providers t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- helm
    SELECT t.id::uuid, 'helm'::text, t.name::text, t.version::text, ''::text,
           t.name || '-' || t.version || '.tgz'::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, COALESCE(t.description, '')::text, ''::text, COALESCE(t.home, '')::text, ''::text, ''::text,
           jsonb_build_object('app_version', COALESCE(t.app_version, ''), 'api_version', t.api_version, 'type', t.type,
                              'keywords', array_to_string(t.keywords, ', '), 'sources', array_to_string(t.sources, E'\n'),
                              'icon', COALESCE(t.icon, ''))::jsonb,
           NULL::jsonb
    FROM helm_charts t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- raw
    SELECT t.id::uuid, 'raw'::text, t.path::text, ''::text, ''::text,
           t.path::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           t.blob_id::uuid, NULL::text, NULL::bigint, NULL::bigint,
           ''::text, ''::text, ''::text, ''::text, ''::text, ''::text,
           jsonb_build_object('content_type', t.content_type)::jsonb,
           NULL::jsonb
    FROM raw_artifacts t
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
    UNION ALL
    -- proxy cache: a cached upstream file of a proxy repository
    SELECT t.id::uuid, r.format::text, t.cache_key::text, ''::text, ''::text,
           t.cache_key::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           NULL::uuid, t.blob_key::text, NULL::bigint, t.size_bytes::bigint,
           ''::text, ''::text, ''::text, ''::text, ''::text, ''::text,
           jsonb_build_object('content_type', COALESCE(t.content_type, ''), 'hit_count', t.hit_count::text)::jsonb,
           NULL::jsonb
    FROM proxy_cache_entries t JOIN repositories r ON r.id = t.repository_id
    WHERE r.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
      AND t.status_code = 200 AND t.blob_key IS NOT NULL
    UNION ALL
    -- docker: one row per tag; the manifest is the file
    SELECT t.id::uuid, 'docker'::text, (CASE WHEN t.image = '' THEN r.name ELSE r.name || '/' || t.image END)::text, t.name::text, ''::text,
           t.manifest_digest::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           dm.blob_id::uuid, NULL::text, COALESCE(dm.size, 0)::bigint, NULL::bigint,
           ''::text, ''::text, ''::text, ''::text, ''::text, ''::text,
           jsonb_build_object('digest', t.manifest_digest, 'media_type', COALESCE(dm.media_type, ''))::jsonb,
           NULL::jsonb
    FROM docker_tags t
    JOIN repositories r ON r.id = t.repository_id AND r.deleted_at IS NULL
    LEFT JOIN docker_manifests dm ON dm.repository_id = t.repository_id AND dm.image = t.image AND dm.digest = t.manifest_digest AND dm.deleted_at IS NULL
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
) AS p
LEFT JOIN LATERAL (SELECT sha256, size_bytes FROM blobs WHERE id = p.blob_id) AS bi ON true
LEFT JOIN LATERAL (SELECT sha256, size_bytes FROM blobs WHERE sha256 = p.blob_key) AS bs ON true
LIMIT 1;
