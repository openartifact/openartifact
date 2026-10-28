-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Ansible collections (docs/formats/ansible.md): the "ansible" repository
-- format and its collection table.
--
-- Rolling-safe: the repositories.format constraint is widened in one statement
-- pair (a superset of the old list, added NOT VALID, so replicas of the previous
-- version keep working and no table scan runs under the lock; the existing rows
-- are validated in 095_ansible_validate.no_tx.sql), and the collection table is
-- new, so no existing table is rewritten. Its live-row indexes are created
-- together with the empty table.

SET LOCAL lock_timeout = '10s';

ALTER TABLE repositories DROP CONSTRAINT chk_repo_format;
ALTER TABLE repositories ADD CONSTRAINT chk_repo_format CHECK (format IN (
    'rpm', 'deb', 'maven', 'npm', 'docker', 'helm', 'pypi', 'nuget', 'go', 'cargo', 'rubygems', 'raw',
    'conda', 'conan', 'ivy', 'sbt', 'terraform', 'apk', 'pacman', 'nix', 'ansible')) NOT VALID;

COMMENT ON COLUMN repositories.format IS 'Package format: rpm, deb, maven, npm, docker, helm, pypi, nuget, go, cargo, rubygems, raw, conda, conan, ivy, sbt, terraform, apk, pacman, nix, ansible. Constrained by chk_repo_format.';

-- One row per collection version: the published ns-name-version.tar.gz.
CREATE TABLE ansible_collections (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    namespace character varying(64) NOT NULL,
    name character varying(64) NOT NULL,
    version character varying(128) NOT NULL,
    filename character varying(255) NOT NULL,
    blob_id uuid NOT NULL,
    dependencies jsonb DEFAULT '{}'::jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    requires_ansible character varying(255) DEFAULT '' NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text,
    CONSTRAINT ansible_collections_pkey PRIMARY KEY (id),
    CONSTRAINT ansible_collections_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES blobs(id) ON DELETE RESTRICT,
    CONSTRAINT ansible_collections_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX ansible_collections_live_coords_key ON ansible_collections (repository_id, namespace, name, version) WHERE deleted_at IS NULL;
CREATE INDEX idx_ansible_collections_repo_created ON ansible_collections (repository_id, created_at DESC, id) WHERE deleted_at IS NULL;
CREATE INDEX idx_ansible_collections_blob_id ON ansible_collections (blob_id);
CREATE INDEX idx_ansible_collections_deleted ON ansible_collections (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TRIGGER update_ansible_collections_updated_at
    BEFORE UPDATE ON ansible_collections
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE ansible_collections IS 'Ansible collections: one ns-name-version.tar.gz artifact per namespace, name and version.';
COMMENT ON COLUMN ansible_collections.dependencies IS 'Collection dependencies from MANIFEST.json: {"namespace.name": "version requirement"}.';
COMMENT ON COLUMN ansible_collections.metadata IS 'The collection_info object of MANIFEST.json (authors, description, license, tags, links); served as the metadata of the version detail.';
COMMENT ON COLUMN ansible_collections.requires_ansible IS 'requires_ansible of meta/runtime.yml, empty when the collection has none.';

-- Quota reservations (migration 061): one reserved artifact per row, sized by
-- the row's blob, exactly as the other single-blob package tables.
CREATE FUNCTION quota_pending_ansible_collections() RETURNS trigger LANGUAGE plpgsql AS $body$
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

CREATE TRIGGER quota_pending_ins AFTER INSERT ON ansible_collections FOR EACH ROW EXECUTE FUNCTION quota_pending_ansible_collections();
CREATE TRIGGER quota_pending_del AFTER DELETE ON ansible_collections FOR EACH ROW EXECUTE FUNCTION quota_pending_ansible_collections();
CREATE TRIGGER quota_pending_upd AFTER UPDATE OF blob_id ON ansible_collections FOR EACH ROW WHEN (OLD.blob_id IS DISTINCT FROM NEW.blob_id) EXECUTE FUNCTION quota_pending_ansible_collections();

-- Recovery journal (migration 068; keep in step with internal/recovery/tables.go).
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON ansible_collections
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
