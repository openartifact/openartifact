-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Row journal for disaster recovery (docs/operations/rebuild-from-storage.md).
--
-- Package metadata that cannot be derived from the bytes (repository names,
-- project, path/name/version mapping, dist-tags, deb distributions, docker
-- tags, trash state, ...) lives only in Postgres. The journal mirrors every
-- such row as a small JSON "sidecar" blob in the blob store, so
-- `openartifact rebuild` can re-index an empty database from storage.
--
-- Capture is trigger based and cheap: an AFTER row trigger on each journaled
-- table reads only the KEY columns of the row and upserts a mark into
-- recovery_dirty. It never serializes the row (npm manifests can be large).
-- The journal worker (internal/recovery) drains the queue, writes/deletes the
-- sidecar blobs and records what it wrote in recovery_sidecars. Which tables
-- are journaled, their keys and excluded columns live in
-- internal/recovery/tables.go; a guard test keeps the two in sync.

SET LOCAL lock_timeout = '10s';

-- Singleton configuration/progress row. `enabled` is set from the server
-- config at startup; while false the triggers return immediately.
CREATE TABLE recovery_state (
    id                 boolean     PRIMARY KEY DEFAULT true CHECK (id),
    enabled            boolean     NOT NULL DEFAULT true,
    last_error         text,
    last_error_at      timestamptz,
    last_drain_at      timestamptz,
    last_reconcile_at  timestamptz
);

INSERT INTO recovery_state (id) VALUES (true);

COMMENT ON TABLE recovery_state IS 'Singleton: journal enabled flag (set from config at startup) and worker progress; see migration 068';

CREATE SEQUENCE recovery_dirty_seq;

-- One mark per (table, row key). seq is bumped on every re-mark so the worker
-- can delete a mark only if nothing touched the row since it read it.
CREATE TABLE recovery_dirty (
    table_name  text        NOT NULL,
    row_key     text        NOT NULL,
    seq         bigint      NOT NULL DEFAULT nextval('recovery_dirty_seq'),
    marked_at   timestamptz NOT NULL DEFAULT now(),
    attempts    integer     NOT NULL DEFAULT 0,
    retry_at    timestamptz,
    last_error  text,
    PRIMARY KEY (table_name, row_key)
);

CREATE INDEX recovery_dirty_seq_idx ON recovery_dirty (seq);

COMMENT ON TABLE recovery_dirty IS 'Journal work queue: rows whose sidecar must be (re)written or deleted; see migration 068';

-- What the worker has written: the hash of the journaled row image, the
-- sha256 of the sidecar blob (its storage key) and, while a write is in
-- flight, the sha256 it is about to store (so a crash cannot leave a sidecar
-- nothing knows about).
CREATE TABLE recovery_sidecars (
    table_name      text        NOT NULL,
    row_key         text        NOT NULL,
    row_hash        text        NOT NULL,
    sidecar_sha256  text        NOT NULL,
    pending_sha256  text,
    written_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (table_name, row_key)
);

COMMENT ON TABLE recovery_sidecars IS 'Tracking of journal sidecar blobs in the blob store; sidecars have no blobs row; see migration 068';

-- Resumable reconcile (backfill and stale-sidecar sweep) progress per table.
CREATE TABLE recovery_reconcile (
    table_name         text        PRIMARY KEY,
    stage              text        NOT NULL DEFAULT 'missing' CHECK (stage IN ('missing', 'stale')),
    full_pass          boolean     NOT NULL DEFAULT false,
    cursor             text,
    pass_started_at    timestamptz,
    last_completed_at  timestamptz,
    marked_total       bigint      NOT NULL DEFAULT 0
);

COMMENT ON TABLE recovery_reconcile IS 'Keyset progress of the journal reconcile passes; see migration 068';

-- recovery_mark upserts a mark; a re-mark gets a fresh seq and is retried at once.
CREATE FUNCTION recovery_mark(p_table text, p_key text)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO recovery_dirty (table_name, row_key) VALUES (p_table, p_key)
    ON CONFLICT (table_name, row_key)
    DO UPDATE SET seq = nextval('recovery_dirty_seq'), marked_at = now(), retry_at = NULL;
END
$$;

-- Trigger function for tables keyed by a single uuid column named id.
CREATE FUNCTION recovery_trg_id()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT (SELECT enabled FROM recovery_state) THEN
        RETURN NULL;
    END IF;

    IF TG_OP = 'DELETE' THEN
        PERFORM recovery_mark(TG_TABLE_NAME, OLD.id::text);
    ELSE
        PERFORM recovery_mark(TG_TABLE_NAME, NEW.id::text);

        IF TG_OP = 'UPDATE' AND OLD.id IS DISTINCT FROM NEW.id THEN
            PERFORM recovery_mark(TG_TABLE_NAME, OLD.id::text);
        END IF;
    END IF;

    RETURN NULL;
END
$$;

-- Composite-key tables: the row key is the jsonb array of the key columns as
-- text, the same expression internal/recovery/tables.go uses.
CREATE FUNCTION recovery_trg_group_members()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT (SELECT enabled FROM recovery_state) THEN
        RETURN NULL;
    END IF;

    IF TG_OP = 'DELETE' THEN
        PERFORM recovery_mark(TG_TABLE_NAME, jsonb_build_array(OLD.group_repo_id::text, OLD.member_repo_id::text)::text);
    ELSE
        PERFORM recovery_mark(TG_TABLE_NAME, jsonb_build_array(NEW.group_repo_id::text, NEW.member_repo_id::text)::text);

        IF TG_OP = 'UPDATE' THEN
            PERFORM recovery_mark(TG_TABLE_NAME, jsonb_build_array(OLD.group_repo_id::text, OLD.member_repo_id::text)::text);
        END IF;
    END IF;

    RETURN NULL;
END
$$;

CREATE FUNCTION recovery_trg_docker_blob_images()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT (SELECT enabled FROM recovery_state) THEN
        RETURN NULL;
    END IF;

    IF TG_OP = 'DELETE' THEN
        PERFORM recovery_mark(TG_TABLE_NAME, jsonb_build_array(OLD.repository_id::text, OLD.image, OLD.digest)::text);
    ELSE
        PERFORM recovery_mark(TG_TABLE_NAME, jsonb_build_array(NEW.repository_id::text, NEW.image, NEW.digest)::text);

        IF TG_OP = 'UPDATE' THEN
            PERFORM recovery_mark(TG_TABLE_NAME, jsonb_build_array(OLD.repository_id::text, OLD.image, OLD.digest)::text);
        END IF;
    END IF;

    RETURN NULL;
END
$$;

CREATE FUNCTION recovery_trg_docker_manifest_refs()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT (SELECT enabled FROM recovery_state) THEN
        RETURN NULL;
    END IF;

    IF TG_OP = 'DELETE' THEN
        PERFORM recovery_mark(TG_TABLE_NAME, jsonb_build_array(OLD.manifest_id::text, OLD.kind, OLD.digest)::text);
    ELSE
        PERFORM recovery_mark(TG_TABLE_NAME, jsonb_build_array(NEW.manifest_id::text, NEW.kind, NEW.digest)::text);

        IF TG_OP = 'UPDATE' THEN
            PERFORM recovery_mark(TG_TABLE_NAME, jsonb_build_array(OLD.manifest_id::text, OLD.kind, OLD.digest)::text);
        END IF;
    END IF;

    RETURN NULL;
END
$$;

-- Journaled tables (keep in sync with internal/recovery/tables.go).
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON projects
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON blobs
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON repositories
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON rpm_repos
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON debian_distributions
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON debian_components
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON debian_architectures
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON deb_packages
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON rpm_packages
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON maven_artifacts
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON ivy_artifacts
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON sbt_artifacts
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON npm_packages
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON npm_versions
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON npm_dist_tags
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON pypi_packages
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON nuget_packages
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON gomod_modules
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON cargo_crates
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON rubygems_gems
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON conda_packages
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON conan_packages
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON helm_charts
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON raw_artifacts
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON docker_blobs
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON docker_manifests
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON docker_tags
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON artifact_refs
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON artifact_properties
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON repository_group_members
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_group_members();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON docker_blob_images
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_docker_blob_images();
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON docker_manifest_refs
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_docker_manifest_refs();
