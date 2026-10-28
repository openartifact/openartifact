-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Composer packages (docs/formats/composer.md): the "composer" repository
-- format and its collection table.
--
-- Rolling-safe: the repositories.format constraint is widened in one statement
-- pair (a superset of the old list, added NOT VALID, so replicas of the previous
-- version keep working and no table scan runs under the lock; the existing rows
-- are validated in 096_composer_validate.no_tx.sql), and the collection table is
-- new, so no existing table is rewritten. Its live-row indexes are created
-- together with the empty table.

SET LOCAL lock_timeout = '10s';

ALTER TABLE repositories DROP CONSTRAINT chk_repo_format;
ALTER TABLE repositories ADD CONSTRAINT chk_repo_format CHECK (format IN (
    'rpm', 'deb', 'maven', 'npm', 'docker', 'helm', 'pypi', 'nuget', 'go', 'cargo', 'composer', 'rubygems', 'raw',
    'conda', 'conan', 'ivy', 'sbt', 'terraform', 'apk', 'pacman', 'nix', 'ansible')) NOT VALID;

COMMENT ON COLUMN repositories.format IS 'Package format: rpm, deb, maven, npm, docker, helm, pypi, nuget, go, cargo, composer, rubygems, raw, conda, conan, ivy, sbt, terraform, apk, pacman, nix, ansible. Constrained by chk_repo_format.';

-- One row per package version: the dist zip and the composer.json it carries.
CREATE TABLE composer_packages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    name character varying(255) NOT NULL,
    version character varying(128) NOT NULL,
    version_normalized character varying(128) NOT NULL,
    blob_id uuid NOT NULL,
    sha1 character varying(40) NOT NULL,
    composer_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text,
    CONSTRAINT composer_packages_pkey PRIMARY KEY (id),
    CONSTRAINT composer_packages_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES blobs(id) ON DELETE RESTRICT,
    CONSTRAINT composer_packages_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX composer_packages_live_coords_key ON composer_packages (repository_id, name, version_normalized) WHERE deleted_at IS NULL;
CREATE INDEX idx_composer_packages_repo_created ON composer_packages (repository_id, created_at DESC, id) WHERE deleted_at IS NULL;
CREATE INDEX idx_composer_packages_repo_name ON composer_packages (repository_id, name) WHERE deleted_at IS NULL;
CREATE INDEX idx_composer_packages_blob_id ON composer_packages (blob_id);
CREATE INDEX idx_composer_packages_deleted ON composer_packages (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TRIGGER update_composer_packages_updated_at
    BEFORE UPDATE ON composer_packages
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE composer_packages IS 'Composer packages: one dist zip per package name and normalised version.';
COMMENT ON COLUMN composer_packages.version IS 'The version as published, e.g. v1.2.3 or dev-main.';
COMMENT ON COLUMN composer_packages.version_normalized IS 'Composer VersionParser::normalize of version, e.g. 1.2.3.0.';
COMMENT ON COLUMN composer_packages.sha1 IS 'Hex sha1 of the dist zip (Composer dist.shasum).';
COMMENT ON COLUMN composer_packages.composer_json IS 'The composer.json of the package as uploaded.';

-- Quota reservations (migration 061): one reserved artifact per row, sized by
-- the row's blob, exactly as the other single-blob package tables.
CREATE FUNCTION quota_pending_composer_packages() RETURNS trigger LANGUAGE plpgsql AS $body$
BEGIN
    IF TG_OP IN ('DELETE', 'UPDATE') THEN
        PERFORM quota_pending_add(OLD.repository_id, quota_blob_size(OLD.blob_id), 1);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        PERFORM quota_pending_take(NEW.repository_id, quota_blob_size(NEW.blob_id), 1, false);
    END IF;
    RETURN NULL;
END
$body$;

CREATE TRIGGER quota_pending_ins AFTER INSERT ON composer_packages FOR EACH ROW EXECUTE FUNCTION quota_pending_composer_packages();
CREATE TRIGGER quota_pending_del AFTER DELETE ON composer_packages FOR EACH ROW EXECUTE FUNCTION quota_pending_composer_packages();
CREATE TRIGGER quota_pending_upd AFTER UPDATE OF blob_id ON composer_packages FOR EACH ROW WHEN (OLD.blob_id IS DISTINCT FROM NEW.blob_id) EXECUTE FUNCTION quota_pending_composer_packages();

-- Recovery journal (migration 068; keep in step with internal/recovery/tables.go).
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON composer_packages
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
