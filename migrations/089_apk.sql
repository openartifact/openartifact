-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Alpine package repositories: the "apk" repository format with one row per
-- .apk file and one signing key per repository.
--
-- Rolling-safe: the repositories.format constraint is widened in one statement
-- pair (a superset of the old list, added NOT VALID, so replicas of the previous
-- version keep working and no table scan runs under the lock; the existing rows
-- are validated in 089_apk_validate.no_tx.sql), and the two tables are new, so
-- no existing table is rewritten. The live-row unique index is created together
-- with the empty table.

SET LOCAL lock_timeout = '10s';

ALTER TABLE repositories DROP CONSTRAINT chk_repo_format;
ALTER TABLE repositories ADD CONSTRAINT chk_repo_format CHECK (format IN (
    'rpm', 'deb', 'maven', 'npm', 'docker', 'helm', 'pypi', 'nuget', 'go', 'cargo', 'rubygems', 'raw',
    'conda', 'conan', 'ivy', 'sbt', 'terraform', 'apk')) NOT VALID;

COMMENT ON COLUMN repositories.format IS 'Package format: rpm, deb, maven, npm, docker, helm, pypi, nuget, go, cargo, rubygems, raw, conda, conan, ivy, sbt, terraform, apk. Constrained by chk_repo_format.';

-- One row per .apk file, addressed as branch/repo/arch/name-version.apk of a
-- mirror tree (for example v3.20/main/x86_64).
CREATE TABLE apk_packages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    branch character varying(64) NOT NULL,
    repo character varying(64) NOT NULL,
    arch character varying(32) NOT NULL,
    pkg_arch character varying(32) NOT NULL,
    name character varying(128) NOT NULL,
    version character varying(128) NOT NULL,
    checksum text NOT NULL,
    data_sha256 text NOT NULL,
    description text DEFAULT '' NOT NULL,
    url text DEFAULT '' NOT NULL,
    license text DEFAULT '' NOT NULL,
    origin text DEFAULT '' NOT NULL,
    maintainer text DEFAULT '' NOT NULL,
    commit text DEFAULT '' NOT NULL,
    build_time bigint DEFAULT 0 NOT NULL,
    installed_size bigint DEFAULT 0 NOT NULL,
    depends text[] DEFAULT '{}' NOT NULL,
    provides text[] DEFAULT '{}' NOT NULL,
    install_if text[] DEFAULT '{}' NOT NULL,
    provider_priority integer DEFAULT 0 NOT NULL,
    download_count bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text,
    CONSTRAINT apk_packages_pkey PRIMARY KEY (id),
    CONSTRAINT apk_packages_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES blobs(id) ON DELETE RESTRICT,
    CONSTRAINT apk_packages_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX apk_packages_live_coords_key ON apk_packages (repository_id, branch, repo, arch, name, version) WHERE deleted_at IS NULL;
CREATE INDEX idx_apk_packages_repo_created ON apk_packages (repository_id, created_at DESC, id) WHERE deleted_at IS NULL;
CREATE INDEX idx_apk_packages_blob_id ON apk_packages (blob_id);
CREATE INDEX idx_apk_packages_deleted ON apk_packages (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TRIGGER update_apk_packages_updated_at
    BEFORE UPDATE ON apk_packages
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE apk_packages IS 'Alpine packages: one .apk per branch/repo/arch/name/version.';
COMMENT ON COLUMN apk_packages.arch IS 'Architecture directory of the mirror tree.';
COMMENT ON COLUMN apk_packages.pkg_arch IS 'Architecture of the package itself (PKGINFO arch, APKINDEX A:); noarch inside an x86_64 directory.';
COMMENT ON COLUMN apk_packages.checksum IS 'APKINDEX C: value: Q1 + base64 of the SHA-1 of the control segment.';
COMMENT ON COLUMN apk_packages.data_sha256 IS 'Lowercase hex SHA-256 of the data segment (PKGINFO datahash).';

-- The per-repository index signing key (abuild format). Not journaled: the
-- private key is a secret and must never reach a sidecar.
CREATE TABLE apk_signing_keys (
    repository_id uuid NOT NULL,
    key_name character varying(160) NOT NULL,
    fingerprint text NOT NULL,
    public_key text NOT NULL,
    private_key bytea NOT NULL,
    created_by text DEFAULT '' NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT apk_signing_keys_pkey PRIMARY KEY (repository_id),
    CONSTRAINT apk_signing_keys_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
);

CREATE TRIGGER update_apk_signing_keys_updated_at
    BEFORE UPDATE ON apk_signing_keys
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE apk_signing_keys IS 'Alpine repository index signing keys; private_key is sealed with internal/crypto.';

-- Quota reservations (migration 061): one reserved artifact per row, sized by
-- the row's blob, exactly as the other single-blob package tables.
DO $$
DECLARE
    tbl text;
BEGIN
    FOREACH tbl IN ARRAY ARRAY['apk_packages']
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
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON apk_packages
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
