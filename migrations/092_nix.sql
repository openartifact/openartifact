-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Nix binary cache repositories: the "nix" repository format with one row per
-- store path (narinfo + NAR file), a staging table for NARs that arrive before
-- their narinfo, and one narinfo signing key per repository.
--
-- Rolling-safe: the repositories.format constraint is widened in one statement
-- pair (a superset of the old list, added NOT VALID, so replicas of the previous
-- version keep working and no table scan runs under the lock; the existing rows
-- are validated in 092_nix_validate.no_tx.sql), and the three tables are new, so
-- no existing table is rewritten. The live-row unique index is created together
-- with the empty table.

SET LOCAL lock_timeout = '10s';

ALTER TABLE repositories DROP CONSTRAINT chk_repo_format;
ALTER TABLE repositories ADD CONSTRAINT chk_repo_format CHECK (format IN (
    'rpm', 'deb', 'maven', 'npm', 'docker', 'helm', 'pypi', 'nuget', 'go', 'cargo', 'rubygems', 'raw',
    'conda', 'conan', 'ivy', 'sbt', 'terraform', 'apk', 'pacman', 'nix')) NOT VALID;

COMMENT ON COLUMN repositories.format IS 'Package format: rpm, deb, maven, npm, docker, helm, pypi, nuget, go, cargo, rubygems, raw, conda, conan, ivy, sbt, terraform, apk, pacman, nix. Constrained by chk_repo_format.';

-- One row per store path: the narinfo fields plus the NAR file blob. The blob
-- sha256 is the narinfo FileHash and its size is the FileSize.
CREATE TABLE nix_paths (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    store_hash character varying(32) NOT NULL,
    name character varying(211) NOT NULL,
    nar_hash text NOT NULL,
    nar_size bigint NOT NULL,
    nar_file text NOT NULL,
    compression character varying(16) NOT NULL,
    refs text[] DEFAULT '{}' NOT NULL,
    deriver text DEFAULT '' NOT NULL,
    ca text DEFAULT '' NOT NULL,
    signatures text[] DEFAULT '{}' NOT NULL,
    download_count bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text,
    CONSTRAINT nix_paths_pkey PRIMARY KEY (id),
    CONSTRAINT nix_paths_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES blobs(id) ON DELETE RESTRICT,
    CONSTRAINT nix_paths_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX nix_paths_live_hash_key ON nix_paths (repository_id, store_hash) WHERE deleted_at IS NULL;
CREATE INDEX idx_nix_paths_nar_file ON nix_paths (repository_id, nar_file) WHERE deleted_at IS NULL;
CREATE INDEX idx_nix_paths_repo_created ON nix_paths (repository_id, created_at DESC, id) WHERE deleted_at IS NULL;
CREATE INDEX idx_nix_paths_blob_id ON nix_paths (blob_id);
CREATE INDEX idx_nix_paths_deleted ON nix_paths (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TRIGGER update_nix_paths_updated_at
    BEFORE UPDATE ON nix_paths
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE nix_paths IS 'Nix store paths: one narinfo and NAR file per store hash.';
COMMENT ON COLUMN nix_paths.store_hash IS 'The 32 character nixbase32 hash of the store path (the narinfo file name).';
COMMENT ON COLUMN nix_paths.name IS 'Store path name without the hash, for example hello-2.12.';
COMMENT ON COLUMN nix_paths.nar_hash IS 'narinfo NarHash: sha256:<nixbase32> of the uncompressed NAR.';
COMMENT ON COLUMN nix_paths.nar_file IS 'File name below nar/: <nixbase32 sha256 of the file>.nar.<ext>; several paths may share one.';
COMMENT ON COLUMN nix_paths.refs IS 'narinfo References: base names of the store paths referenced.';
COMMENT ON COLUMN nix_paths.signatures IS 'narinfo Sig lines supplied by the uploader (name:base64).';

-- NAR files that arrived before their narinfo: nix copy uploads the NAR and
-- commits the path with the narinfo last. Staging only, not journaled; the
-- cleanup worker sweeps rows that never got their narinfo.
CREATE TABLE nix_pending_nars (
    repository_id uuid NOT NULL,
    sha256 text NOT NULL,
    nar_file text NOT NULL,
    blob_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT nix_pending_nars_pkey PRIMARY KEY (repository_id, sha256),
    CONSTRAINT nix_pending_nars_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES blobs(id) ON DELETE RESTRICT,
    CONSTRAINT nix_pending_nars_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
);

CREATE INDEX idx_nix_pending_nars_blob_id ON nix_pending_nars (blob_id);
CREATE INDEX idx_nix_pending_nars_created ON nix_pending_nars (created_at);

COMMENT ON TABLE nix_pending_nars IS 'Uploaded NAR files waiting for their narinfo; swept by the cleanup worker.';

-- The per-repository narinfo signing key (Nix ed25519 format). Not journaled:
-- the private key is a secret and must never reach a sidecar.
CREATE TABLE nix_signing_keys (
    repository_id uuid NOT NULL,
    key_name character varying(160) NOT NULL,
    public_key text NOT NULL,
    private_key bytea NOT NULL,
    created_by text DEFAULT '' NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT nix_signing_keys_pkey PRIMARY KEY (repository_id),
    CONSTRAINT nix_signing_keys_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
);

CREATE TRIGGER update_nix_signing_keys_updated_at
    BEFORE UPDATE ON nix_signing_keys
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE nix_signing_keys IS 'Nix narinfo signing keys; private_key is sealed with internal/crypto.';

-- Quota reservations (migration 061): one reserved artifact per row, sized by
-- the row's blob, exactly as the other single-blob package tables.
DO $$
DECLARE
    tbl text;
BEGIN
    FOREACH tbl IN ARRAY ARRAY['nix_paths']
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
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON nix_paths
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
