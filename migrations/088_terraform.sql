-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Terraform/OpenTofu registry (docs/formats/terraform.md): the "terraform"
-- repository format with module archives and per-platform provider zips.
--
-- Rolling-safe: the repositories.format constraint is widened in one statement
-- pair (a superset of the old list, added NOT VALID, so replicas of the previous
-- version keep working and no table scan runs under the lock; the existing rows
-- are validated in 088_terraform_validate.no_tx.sql), and the two package tables
-- are new, so no existing table is rewritten. Their live-row unique indexes are
-- created together with the empty tables.

SET LOCAL lock_timeout = '10s';

ALTER TABLE repositories DROP CONSTRAINT chk_repo_format;
ALTER TABLE repositories ADD CONSTRAINT chk_repo_format CHECK (format IN (
    'rpm', 'deb', 'maven', 'npm', 'docker', 'helm', 'pypi', 'nuget', 'go', 'cargo', 'rubygems', 'raw',
    'conda', 'conan', 'ivy', 'sbt', 'terraform')) NOT VALID;

COMMENT ON COLUMN repositories.format IS 'Package format: rpm, deb, maven, npm, docker, helm, pypi, nuget, go, cargo, rubygems, raw, conda, conan, ivy, sbt, terraform. Constrained by chk_repo_format.';

-- One row per published module version. The registry namespace of a module is
-- the repository name, so there is no namespace column.
CREATE TABLE terraform_modules (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    name character varying(64) NOT NULL,
    system character varying(64) NOT NULL,
    version character varying(100) NOT NULL,
    archive_ext character varying(8) NOT NULL,
    download_count bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text,
    CONSTRAINT terraform_modules_pkey PRIMARY KEY (id),
    CONSTRAINT terraform_modules_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES blobs(id) ON DELETE RESTRICT,
    CONSTRAINT terraform_modules_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE,
    CONSTRAINT terraform_modules_archive_ext_check CHECK (archive_ext IN ('zip', 'tar.gz'))
);

CREATE UNIQUE INDEX terraform_modules_live_coords_key ON terraform_modules (repository_id, name, system, version) WHERE deleted_at IS NULL;
CREATE INDEX idx_terraform_modules_repo_created ON terraform_modules (repository_id, created_at DESC, id) WHERE deleted_at IS NULL;
CREATE INDEX idx_terraform_modules_blob_id ON terraform_modules (blob_id);
CREATE INDEX idx_terraform_modules_deleted ON terraform_modules (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TRIGGER update_terraform_modules_updated_at
    BEFORE UPDATE ON terraform_modules
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE terraform_modules IS 'Terraform/OpenTofu registry modules: one archive (zip or tar.gz) per name/system/version.';
COMMENT ON COLUMN terraform_modules.archive_ext IS 'Archive kind as stored: zip or tar.gz; it is the extension of the download URL.';

-- One row per provider platform zip. A provider version is the set of its rows;
-- its SHA256SUMS file is generated from the blobs' digests.
CREATE TABLE terraform_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    type character varying(64) NOT NULL,
    version character varying(100) NOT NULL,
    os character varying(32) NOT NULL,
    arch character varying(32) NOT NULL,
    protocols text[] DEFAULT '{5.0}' NOT NULL,
    download_count bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text,
    CONSTRAINT terraform_providers_pkey PRIMARY KEY (id),
    CONSTRAINT terraform_providers_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES blobs(id) ON DELETE RESTRICT,
    CONSTRAINT terraform_providers_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX terraform_providers_live_coords_key ON terraform_providers (repository_id, type, version, os, arch) WHERE deleted_at IS NULL;
CREATE INDEX idx_terraform_providers_repo_created ON terraform_providers (repository_id, created_at DESC, id) WHERE deleted_at IS NULL;
CREATE INDEX idx_terraform_providers_blob_id ON terraform_providers (blob_id);
CREATE INDEX idx_terraform_providers_deleted ON terraform_providers (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TRIGGER update_terraform_providers_updated_at
    BEFORE UPDATE ON terraform_providers
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE terraform_providers IS 'Terraform/OpenTofu registry providers: one platform zip per type/version/os/arch.';
COMMENT ON COLUMN terraform_providers.protocols IS 'Plugin protocol versions the provider speaks, as the registry reports them (for example 5.0, 6.0).';

-- Quota reservations (migration 061): one reserved artifact per row, sized by
-- the row's blob, exactly as the other single-blob package tables.
DO $$
DECLARE
    tbl text;
BEGIN
    FOREACH tbl IN ARRAY ARRAY['terraform_modules', 'terraform_providers']
    LOOP
        EXECUTE format($f$
            CREATE FUNCTION %1$I() RETURNS trigger LANGUAGE plpgsql AS $body$
            BEGIN
                IF TG_OP IN ('DELETE', 'UPDATE') THEN
                    PERFORM quota_pending_add(OLD.repository_id, quota_blob_size(OLD.blob_id), 1);
                END IF;
                IF TG_OP IN ('INSERT', 'UPDATE') THEN
                    PERFORM quota_pending_take(NEW.repository_id, quota_blob_size(NEW.blob_id), 1, false);
                END IF;
                RETURN NULL;
            END
            $body$
        $f$, 'quota_pending_' || tbl);

        EXECUTE format('CREATE TRIGGER quota_pending_ins AFTER INSERT ON %1$I FOR EACH ROW EXECUTE FUNCTION %2$I()',
                       tbl, 'quota_pending_' || tbl);
        EXECUTE format('CREATE TRIGGER quota_pending_del AFTER DELETE ON %1$I FOR EACH ROW EXECUTE FUNCTION %2$I()',
                       tbl, 'quota_pending_' || tbl);
        EXECUTE format('CREATE TRIGGER quota_pending_upd AFTER UPDATE OF blob_id ON %1$I FOR EACH ROW WHEN (OLD.blob_id IS DISTINCT FROM NEW.blob_id) EXECUTE FUNCTION %2$I()',
                       tbl, 'quota_pending_' || tbl);
    END LOOP;
END
$$;

-- Recovery journal (migration 068; keep in step with internal/recovery/tables.go).
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON terraform_modules
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON terraform_providers
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
