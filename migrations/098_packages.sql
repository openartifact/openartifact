-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Generic package tables (design: docs/design/format-plugins.md).
--
-- Every format except docker keeps its packages in packages (one logical
-- package of a repository), package_versions (one version incarnation) and
-- package_files (one stored file: the unit of listing, trash, quota, blob
-- references and scan ids). package_tags holds mutable named pointers to
-- versions (npm dist-tags).
--
-- Expand step of an expand/backfill/switch/contract rollout: this migration
-- only creates the tables, empty, and the import helpers the per-format
-- migrations (099-119) use to mirror and backfill the legacy per-format
-- tables. The legacy tables are dropped by the contract migration of the next
-- release.

SET LOCAL lock_timeout = '10s';

CREATE TABLE packages (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id uuid        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    format        varchar(16) NOT NULL,
    name          text        NOT NULL,
    name_key      text        NOT NULL,
    attrs         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE packages IS 'One logical package of a repository; identity is (repository_id, name_key). See docs/design/format-plugins.md';
COMMENT ON COLUMN packages.name IS 'Listing and display name';
COMMENT ON COLUMN packages.name_key IS 'Identity inside the repository: the normalised name';

CREATE UNIQUE INDEX uq_packages_name ON packages (repository_id, name_key);
CREATE INDEX idx_packages_name_trgm ON packages USING gin (name gin_trgm_ops);

CREATE TABLE package_versions (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    package_id    uuid        NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    repository_id uuid        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    version       text        NOT NULL,
    version_key   text        NOT NULL,
    metadata      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    unit_key      text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz,
    deleted_by    text
);

COMMENT ON TABLE package_versions IS 'One version incarnation; live iff at least one of its files is live';
COMMENT ON COLUMN package_versions.version_key IS 'Identity inside the package: the version plus the coordinates the format keys on; cleanup policies rank by it';
COMMENT ON COLUMN package_versions.unit_key IS 'Federation unit key when the federation unit is the version';

CREATE UNIQUE INDEX uq_package_versions_live ON package_versions (package_id, version_key) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_versions_pkg ON package_versions (package_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_versions_repo ON package_versions (repository_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_versions_deleted ON package_versions (package_id) WHERE deleted_at IS NOT NULL;

CREATE TABLE package_files (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    version_id     uuid        NOT NULL REFERENCES package_versions(id) ON DELETE CASCADE,
    repository_id  uuid        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    path           text        NOT NULL,
    filename       text        NOT NULL,
    blob_id        uuid        REFERENCES blobs(id) ON DELETE RESTRICT,
    size_bytes     bigint,
    role           text        NOT NULL DEFAULT 'primary',
    listed         boolean     NOT NULL DEFAULT true,
    arch           text        NOT NULL DEFAULT '',
    part           text        NOT NULL DEFAULT '',
    attrs          jsonb       NOT NULL DEFAULT '{}'::jsonb,
    checksums      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    unit_key       text,
    download_count bigint      NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    deleted_at     timestamptz,
    deleted_by     text
);

COMMENT ON TABLE package_files IS 'One stored file: the unit of listing, trash, quota, blob references and scan ids. Migrated rows keep the id of the legacy row';
COMMENT ON COLUMN package_files.path IS 'Repository-wide locator, unique while live';
COMMENT ON COLUMN package_files.filename IS 'What a client downloads';
COMMENT ON COLUMN package_files.size_bytes IS 'Declared size that wins over the blob size when set (rpm, rubygems)';
COMMENT ON COLUMN package_files.role IS 'primary counts as one artifact for quota; any other role (go mod/info) counts none';
COMMENT ON COLUMN package_files.listed IS 'true: the file is a unit of the repository listing';
COMMENT ON COLUMN package_files.arch IS 'Platform qualifier shown by the listing and the second retention partition key';
COMMENT ON COLUMN package_files.part IS 'Extra retention-partition suffix (deb distribution, maven snapshot flag)';
COMMENT ON COLUMN package_files.checksums IS 'sha1/md5/sha512/shasum/integrity for checksum search; sha256 is the blob''s';
COMMENT ON COLUMN package_files.unit_key IS 'Federation unit key when the federation unit is the file';

CREATE UNIQUE INDEX uq_package_files_path ON package_files (repository_id, path) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_files_listing ON package_files (repository_id, created_at DESC, id) WHERE deleted_at IS NULL AND listed;
CREATE INDEX idx_package_files_version ON package_files (version_id);
CREATE INDEX idx_package_files_filename ON package_files (repository_id, filename) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_files_blob ON package_files (blob_id) WHERE blob_id IS NOT NULL;
CREATE INDEX idx_package_files_trash ON package_files (repository_id, deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX idx_package_files_sha1 ON package_files ((checksums ->> 'sha1')) WHERE checksums ? 'sha1' AND deleted_at IS NULL;
CREATE INDEX idx_package_files_md5 ON package_files ((checksums ->> 'md5')) WHERE checksums ? 'md5' AND deleted_at IS NULL;
CREATE INDEX idx_package_files_shasum ON package_files ((checksums ->> 'shasum')) WHERE checksums ? 'shasum' AND deleted_at IS NULL;
CREATE INDEX idx_package_files_unit ON package_files (repository_id, unit_key) WHERE unit_key IS NOT NULL AND deleted_at IS NULL;

CREATE TABLE package_tags (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    package_id uuid        NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    tag        text        NOT NULL,
    version    text        NOT NULL,
    unit_key   text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (package_id, tag)
);

COMMENT ON TABLE package_tags IS 'Mutable named pointers to versions (npm dist-tags); version is resolved at read time';

CREATE TRIGGER update_packages_updated_at BEFORE UPDATE ON packages
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE TRIGGER update_package_versions_updated_at BEFORE UPDATE ON package_versions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
-- A checksum-only update (a digest backfill) does not bump updated_at.
CREATE TRIGGER update_package_files_updated_at BEFORE UPDATE ON package_files
    FOR EACH ROW WHEN (old.checksums IS NOT DISTINCT FROM new.checksums)
    EXECUTE FUNCTION update_updated_at_column();
CREATE TRIGGER update_package_tags_updated_at BEFORE UPDATE ON package_tags
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- oa_import_file upserts one legacy row into the generic tables and returns
-- the package_files id. It is the single importer: the per-format
-- oa_import_<legacy table>(row) functions only map their columns onto its
-- named arguments, so the mirror triggers and the backfill share one code path
-- and importing a row twice changes nothing.
--
--   * The package is found or created by (repository, name_key); p_pkg_id
--     fixes the id of a created package (npm keeps the legacy package id).
--   * The version is p_ver_id when given (one legacy row = one version, the
--     version takes the legacy id). Otherwise a live file joins the live
--     version with the same version_key, and a soft-deleted file joins the
--     soft-deleted version with the same version_key and deleted_at, so
--     siblings trashed together stay together; a missing version is created.
--   * The file keeps p_file_id (the legacy row id). When a file moves to
--     another version, the version it left is removed when no file is left.
--
-- Callers must set oa.mirror = 'on' (the mirror triggers do) so that the
-- quota and federation triggers of the generic tables skip the write: the
-- legacy table's own triggers have accounted for it.
CREATE FUNCTION oa_import_file(
    p_repo        uuid,
    p_format      text,
    p_name        text,
    p_name_key    text,
    p_version     text,
    p_version_key text,
    p_file_id     uuid,
    p_path        text,
    p_filename    text,
    p_pkg_id      uuid        DEFAULT NULL,
    p_pkg_attrs   jsonb       DEFAULT '{}'::jsonb,
    p_ver_id      uuid        DEFAULT NULL,
    p_ver_meta    jsonb       DEFAULT '{}'::jsonb,
    p_ver_unit    text        DEFAULT NULL,
    p_blob        uuid        DEFAULT NULL,
    p_size        bigint      DEFAULT NULL,
    p_role        text        DEFAULT 'primary',
    p_listed      boolean     DEFAULT true,
    p_arch        text        DEFAULT '',
    p_part        text        DEFAULT '',
    p_file_attrs  jsonb       DEFAULT '{}'::jsonb,
    p_checksums   jsonb       DEFAULT '{}'::jsonb,
    p_file_unit   text        DEFAULT NULL,
    p_downloads   bigint      DEFAULT 0,
    p_created     timestamptz DEFAULT now(),
    p_updated     timestamptz DEFAULT now(),
    p_deleted     timestamptz DEFAULT NULL,
    p_deleted_by  text        DEFAULT NULL
) RETURNS uuid
LANGUAGE plpgsql AS $$
DECLARE
    v_pkg     uuid;
    v_ver     uuid;
    v_old_ver uuid;
BEGIN
    INSERT INTO packages AS p (id, repository_id, format, name, name_key, attrs, created_at, updated_at)
    VALUES (COALESCE(p_pkg_id, gen_random_uuid()), p_repo, p_format, p_name, p_name_key,
            COALESCE(p_pkg_attrs, '{}'::jsonb), p_created, p_updated)
    ON CONFLICT (repository_id, name_key) DO UPDATE
       SET attrs      = p.attrs || EXCLUDED.attrs,
           created_at = LEAST(p.created_at, EXCLUDED.created_at),
           updated_at = GREATEST(p.updated_at, EXCLUDED.updated_at)
    RETURNING p.id INTO v_pkg;

    SELECT version_id INTO v_old_ver FROM package_files WHERE id = p_file_id;

    v_ver := p_ver_id;

    IF v_ver IS NULL AND v_old_ver IS NOT NULL THEN
        SELECT id INTO v_ver FROM package_versions
         WHERE id = v_old_ver AND package_id = v_pkg AND version_key = p_version_key
           AND deleted_at IS NOT DISTINCT FROM p_deleted;
    END IF;

    IF v_ver IS NULL THEN
        SELECT id INTO v_ver FROM package_versions
         WHERE package_id = v_pkg AND version_key = p_version_key
           AND deleted_at IS NOT DISTINCT FROM p_deleted
         ORDER BY created_at LIMIT 1;
    END IF;

    IF v_ver IS NULL THEN
        v_ver := gen_random_uuid();
    END IF;

    -- Legacy unique keys the per-format path rendering does not preserve must
    -- not abort a migration or an old replica's write: the file stays
    -- reachable by id under a disambiguated path.
    IF p_deleted IS NULL AND EXISTS (
        SELECT 1 FROM package_files
         WHERE repository_id = p_repo AND path = p_path AND deleted_at IS NULL AND id <> p_file_id
    ) THEN
        RAISE WARNING 'oa_import_file: live path % of repository % is taken; importing file % as %#%',
            p_path, p_repo, p_file_id, p_path, p_file_id;
        p_path := p_path || '#' || p_file_id::text;
    END IF;

    INSERT INTO package_versions AS v
        (id, package_id, repository_id, version, version_key, metadata, unit_key,
         created_at, updated_at, deleted_at, deleted_by)
    VALUES (v_ver, v_pkg, p_repo, p_version, p_version_key, COALESCE(p_ver_meta, '{}'::jsonb), p_ver_unit,
            p_created, p_updated, p_deleted, p_deleted_by)
    ON CONFLICT (id) DO UPDATE
       SET package_id  = EXCLUDED.package_id,
           version     = EXCLUDED.version,
           version_key = EXCLUDED.version_key,
           metadata    = EXCLUDED.metadata,
           unit_key    = EXCLUDED.unit_key,
           created_at  = LEAST(v.created_at, EXCLUDED.created_at),
           updated_at  = GREATEST(v.updated_at, EXCLUDED.updated_at),
           deleted_at  = EXCLUDED.deleted_at,
           deleted_by  = EXCLUDED.deleted_by;

    INSERT INTO package_files AS f
        (id, version_id, repository_id, path, filename, blob_id, size_bytes, role, listed, arch, part,
         attrs, checksums, unit_key, download_count, created_at, updated_at, deleted_at, deleted_by)
    VALUES (p_file_id, v_ver, p_repo, p_path, p_filename, p_blob, p_size, p_role, p_listed, p_arch, p_part,
            COALESCE(p_file_attrs, '{}'::jsonb), COALESCE(p_checksums, '{}'::jsonb), p_file_unit,
            COALESCE(p_downloads, 0), p_created, p_updated, p_deleted, p_deleted_by)
    ON CONFLICT (id) DO UPDATE
       SET version_id     = EXCLUDED.version_id,
           repository_id  = EXCLUDED.repository_id,
           path           = EXCLUDED.path,
           filename       = EXCLUDED.filename,
           blob_id        = EXCLUDED.blob_id,
           size_bytes     = EXCLUDED.size_bytes,
           role           = EXCLUDED.role,
           listed         = EXCLUDED.listed,
           arch           = EXCLUDED.arch,
           part           = EXCLUDED.part,
           attrs          = EXCLUDED.attrs,
           checksums      = EXCLUDED.checksums,
           unit_key       = EXCLUDED.unit_key,
           download_count = EXCLUDED.download_count,
           created_at     = EXCLUDED.created_at,
           updated_at     = EXCLUDED.updated_at,
           deleted_at     = EXCLUDED.deleted_at,
           deleted_by     = EXCLUDED.deleted_by;

    IF v_old_ver IS NOT NULL AND v_old_ver <> v_ver THEN
        DELETE FROM package_versions v
         WHERE v.id = v_old_ver
           AND NOT EXISTS (SELECT 1 FROM package_files f WHERE f.version_id = v.id);
    END IF;

    RETURN p_file_id;
END
$$;

-- oa_unimport_file removes a file the legacy table deleted, together with the
-- version and package rows it leaves empty.
CREATE FUNCTION oa_unimport_file(p_file uuid) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_ver uuid;
    v_pkg uuid;
BEGIN
    DELETE FROM package_files WHERE id = p_file RETURNING version_id INTO v_ver;

    IF v_ver IS NULL THEN
        RETURN;
    END IF;

    DELETE FROM package_versions v
     WHERE v.id = v_ver AND NOT EXISTS (SELECT 1 FROM package_files f WHERE f.version_id = v.id)
    RETURNING v.package_id INTO v_pkg;

    IF v_pkg IS NOT NULL THEN
        DELETE FROM packages p
         WHERE p.id = v_pkg AND NOT EXISTS (SELECT 1 FROM package_versions v WHERE v.package_id = p.id);
    END IF;
END
$$;

-- oa_mirror_on / oa_mirror_off bracket a mirror write (transaction-local).
CREATE FUNCTION oa_mirror_on() RETURNS void LANGUAGE sql AS $$
    SELECT set_config('oa.mirror', 'on', true)
$$;

CREATE FUNCTION oa_mirror_off() RETURNS void LANGUAGE sql AS $$
    SELECT set_config('oa.mirror', 'off', true)
$$;

-- oa_mirror_active is true while a mirror trigger writes the generic tables.
CREATE FUNCTION oa_mirror_active() RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT COALESCE(current_setting('oa.mirror', true), '') = 'on'
$$;
