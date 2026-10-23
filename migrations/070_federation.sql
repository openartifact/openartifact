-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Federated repositories (docs/design/federation.md): tables, the change-log
-- functions and the repositories.federated flag. The capture triggers on the
-- per-format unit tables are in 071.

SET LOCAL lock_timeout = '10s';

ALTER TABLE repositories ADD COLUMN federated boolean NOT NULL DEFAULT false;

-- One row per instance: the stable identity stamped on every change it makes
-- (the last tie-breaker of the hybrid logical clock).
CREATE TABLE federation_instance (
    singleton   boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    instance_id uuid    NOT NULL DEFAULT gen_random_uuid(),
    created_at  timestamptz NOT NULL DEFAULT now()
);

INSERT INTO federation_instance DEFAULT VALUES;

-- One row per federated repository: the log identity, the head of the local
-- sequence and the repository's hybrid logical clock.
CREATE TABLE federation_repos (
    repo_id     uuid   PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
    log_id      uuid   NOT NULL DEFAULT gen_random_uuid(),
    head_seq    bigint NOT NULL DEFAULT 0,
    hlc_wall    bigint NOT NULL DEFAULT 0,
    hlc_counter integer NOT NULL DEFAULT 0,
    enabled_at  timestamptz NOT NULL DEFAULT now()
);

-- The change log, compacted by unit: one row per unit (an artifact or a
-- reference such as a tag) holding its latest event. It is the log, the state
-- and the snapshot at once.
CREATE TABLE federation_units (
    repo_id     uuid    NOT NULL REFERENCES federation_repos(repo_id) ON DELETE CASCADE,
    unit_key    text    NOT NULL,
    kind        text    NOT NULL CHECK (kind IN ('artifact', 'ref')),
    event       text    NOT NULL CHECK (event IN ('artifact_added', 'artifact_deleted', 'ref_moved', 'metadata_changed')),
    seq         bigint  NOT NULL,
    hlc_wall    bigint  NOT NULL,
    hlc_counter integer NOT NULL,
    origin      uuid    NOT NULL,
    tombstone   boolean NOT NULL DEFAULT false,
    content_ref text    NOT NULL DEFAULT '',
    changed_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, unit_key)
);

CREATE INDEX idx_federation_units_seq ON federation_units (repo_id, seq);

-- Pull sources of a federated repository, with their replication state.
CREATE TABLE federation_peers (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id           uuid NOT NULL REFERENCES federation_repos(repo_id) ON DELETE CASCADE,
    name              text NOT NULL,
    base_url          text NOT NULL,
    token_enc         text NOT NULL,
    ca_pem            text NOT NULL DEFAULT '',
    client_cert_enc   text NOT NULL DEFAULT '',
    client_key_enc    text NOT NULL DEFAULT '',
    enabled           boolean NOT NULL DEFAULT true,
    cursor_seq        bigint  NOT NULL DEFAULT 0,
    remote_head_seq   bigint  NOT NULL DEFAULT 0,
    remote_log_id     uuid,
    last_attempt_at   timestamptz,
    last_success_at   timestamptz,
    last_error        text NOT NULL DEFAULT '',
    error_count       integer NOT NULL DEFAULT 0,
    next_attempt_at   timestamptz,
    oldest_pending_at timestamptz,
    applied_total     bigint NOT NULL DEFAULT 0,
    conflicts_total   bigint NOT NULL DEFAULT 0,
    resync_requested  boolean NOT NULL DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (repo_id, name)
);

CREATE TRIGGER update_federation_peers_updated_at BEFORE UPDATE ON federation_peers
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- Units the receiver could not apply yet (poison unit, quarantined at the
-- origin, a dependency not yet available): retried on their own backoff so a
-- single unit never blocks the stream.
CREATE TABLE federation_deferred (
    peer_id         uuid   NOT NULL REFERENCES federation_peers(id) ON DELETE CASCADE,
    unit_key        text   NOT NULL,
    seq             bigint NOT NULL,
    attempts        integer NOT NULL DEFAULT 1,
    reason          text   NOT NULL DEFAULT '',
    last_error      text   NOT NULL DEFAULT '',
    first_seen_at   timestamptz NOT NULL DEFAULT now(),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (peer_id, unit_key)
);

-- Inbound credentials: a peer presents one to pull a single repository.
-- Only the SHA-256 is stored.
CREATE TABLE federation_peer_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id      uuid NOT NULL REFERENCES federation_repos(repo_id) ON DELETE CASCADE,
    name         text NOT NULL,
    token_hash   bytea NOT NULL UNIQUE,
    created_by   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at   timestamptz
);

CREATE INDEX idx_federation_peer_tokens_repo ON federation_peer_tokens (repo_id);

-- federation_active reports whether changes of repo must be logged: the
-- repository is federated and the change is not itself an applied replica
-- (the apply path sets oa.federation_apply for its transaction).
CREATE FUNCTION federation_active(p_repo uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT COALESCE(current_setting('oa.federation_apply', true), '') <> 'on'
       AND EXISTS (SELECT 1 FROM federation_repos WHERE repo_id = p_repo)
$$;

-- federation_micros is the wall clock in microseconds since the epoch.
CREATE FUNCTION federation_micros(p_at timestamptz) RETURNS bigint
LANGUAGE sql STABLE AS $$
    SELECT (extract(epoch FROM p_at) * 1000000)::bigint
$$;

-- federation_record logs one local change of a unit: it takes the next
-- sequence number of the repository (serialising writers of one federated
-- repository, so sequence numbers follow commit order), stamps the change with
-- the repository's hybrid logical clock and upserts the unit row.
--
-- p_at is set only when a unit is registered from an existing row (enabling
-- federation): the stamp then carries that time and the clock is left alone.
CREATE FUNCTION federation_record(
    p_repo uuid, p_key text, p_kind text, p_event text, p_ref text,
    p_tombstone boolean, p_at timestamptz DEFAULT NULL
) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_now    bigint := federation_micros(clock_timestamp());
    v_seq    bigint;
    v_wall   bigint;
    v_ctr    integer;
    v_origin uuid;
BEGIN
    IF p_at IS NULL THEN
        UPDATE federation_repos
           SET head_seq    = head_seq + 1,
               hlc_counter = CASE WHEN v_now > hlc_wall THEN 0 ELSE hlc_counter + 1 END,
               hlc_wall    = GREATEST(hlc_wall, v_now)
         WHERE repo_id = p_repo
        RETURNING head_seq, hlc_wall, hlc_counter INTO v_seq, v_wall, v_ctr;
    ELSE
        UPDATE federation_repos
           SET head_seq = head_seq + 1
         WHERE repo_id = p_repo
        RETURNING head_seq INTO v_seq;

        v_wall := federation_micros(p_at);
        v_ctr  := 0;
    END IF;

    IF v_seq IS NULL THEN
        RETURN;
    END IF;

    SELECT instance_id INTO v_origin FROM federation_instance;

    INSERT INTO federation_units AS u
        (repo_id, unit_key, kind, event, seq, hlc_wall, hlc_counter, origin, tombstone, content_ref, changed_at)
    VALUES
        (p_repo, p_key, p_kind, p_event, v_seq, v_wall, v_ctr, v_origin, p_tombstone, COALESCE(p_ref, ''), now())
    ON CONFLICT (repo_id, unit_key) DO UPDATE
       SET kind = EXCLUDED.kind, event = EXCLUDED.event, seq = EXCLUDED.seq,
           hlc_wall = EXCLUDED.hlc_wall, hlc_counter = EXCLUDED.hlc_counter,
           origin = EXCLUDED.origin, tombstone = EXCLUDED.tombstone,
           content_ref = EXCLUDED.content_ref, changed_at = EXCLUDED.changed_at;
END
$$;

-- federation_apply_unit records a unit received from a peer, keeping the
-- remote stamp and giving it a fresh local sequence number so the change is
-- served onward to this member's own peers. It raises the repository clock to
-- the stamp (the receive rule of a hybrid logical clock). It returns false,
-- writing nothing, when the unit's current stamp is already at or past the
-- remote one: the caller rolls its transaction back, the change lost the race.
CREATE FUNCTION federation_apply_unit(
    p_repo uuid, p_key text, p_kind text, p_event text, p_ref text,
    p_tombstone boolean, p_wall bigint, p_counter integer, p_origin uuid
) RETURNS boolean
LANGUAGE plpgsql AS $$
DECLARE
    v_seq      bigint;
    v_cur_wall bigint;
    v_cur_ctr  integer;
    v_cur_org  uuid;
    v_clock_wall bigint;
    v_clock_ctr  integer;
    v_new_wall   bigint;
    v_new_ctr    integer;
    v_now      bigint := federation_micros(clock_timestamp());
BEGIN
    SELECT hlc_wall, hlc_counter INTO v_clock_wall, v_clock_ctr
      FROM federation_repos WHERE repo_id = p_repo FOR UPDATE;

    IF NOT FOUND THEN
        RETURN false;
    END IF;

    v_new_wall := GREATEST(v_clock_wall, p_wall, v_now);
    v_new_ctr  := CASE
        WHEN v_new_wall = v_clock_wall AND v_new_wall = p_wall THEN GREATEST(v_clock_ctr, p_counter) + 1
        WHEN v_new_wall = v_clock_wall THEN v_clock_ctr + 1
        WHEN v_new_wall = p_wall THEN p_counter + 1
        ELSE 0 END;

    UPDATE federation_repos
       SET head_seq = head_seq + 1, hlc_wall = v_new_wall, hlc_counter = v_new_ctr
     WHERE repo_id = p_repo
    RETURNING head_seq INTO v_seq;

    SELECT hlc_wall, hlc_counter, origin INTO v_cur_wall, v_cur_ctr, v_cur_org
      FROM federation_units WHERE repo_id = p_repo AND unit_key = p_key;

    IF FOUND AND (v_cur_wall, v_cur_ctr, v_cur_org) >= (p_wall, p_counter, p_origin) THEN
        RETURN false;
    END IF;

    INSERT INTO federation_units AS u
        (repo_id, unit_key, kind, event, seq, hlc_wall, hlc_counter, origin, tombstone, content_ref, changed_at)
    VALUES
        (p_repo, p_key, p_kind, p_event, v_seq, p_wall, p_counter, p_origin, p_tombstone, COALESCE(p_ref, ''), now())
    ON CONFLICT (repo_id, unit_key) DO UPDATE
       SET kind = EXCLUDED.kind, event = EXCLUDED.event, seq = EXCLUDED.seq,
           hlc_wall = EXCLUDED.hlc_wall, hlc_counter = EXCLUDED.hlc_counter,
           origin = EXCLUDED.origin, tombstone = EXCLUDED.tombstone,
           content_ref = EXCLUDED.content_ref, changed_at = EXCLUDED.changed_at;

    RETURN true;
END
$$;

-- federation_capture is the shared body of the capture triggers: it turns one
-- row change into at most one logged unit event. Old/new images are passed as
-- jsonb so one function serves every table; p_ignore lists the volatile
-- columns (timestamps, counters, generated columns) whose change is not a
-- change of the unit.
--
-- A row is "live" until it is soft-deleted (deleted_at set) or removed. The
-- hard removal of an already trashed row (the trash purge) is not logged: the
-- soft delete was.
CREATE FUNCTION federation_capture(
    p_repo uuid, p_op text, p_kind text,
    p_old_key text, p_new_key text,
    p_old jsonb, p_new jsonb,
    p_old_ref text, p_new_ref text,
    p_ignore text[]
) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_old_live boolean := p_old IS NOT NULL AND (p_old->>'deleted_at') IS NULL;
    v_new_live boolean := p_new IS NOT NULL AND (p_new->>'deleted_at') IS NULL;
    v_added    text := CASE p_kind WHEN 'ref' THEN 'ref_moved' ELSE 'artifact_added' END;
    v_deleted  text := CASE p_kind WHEN 'ref' THEN 'ref_moved' ELSE 'artifact_deleted' END;
BEGIN
    IF p_op = 'INSERT' THEN
        IF v_new_live THEN
            PERFORM federation_record(p_repo, p_new_key, p_kind, v_added, p_new_ref, false);
        END IF;
    ELSIF p_op = 'DELETE' THEN
        IF v_old_live THEN
            PERFORM federation_record(p_repo, p_old_key, p_kind, v_deleted, p_old_ref, true);
        END IF;
    ELSE
        IF v_old_live AND v_new_live THEN
            IF p_old_key IS DISTINCT FROM p_new_key THEN
                PERFORM federation_record(p_repo, p_old_key, p_kind, v_deleted, p_old_ref, true);
                PERFORM federation_record(p_repo, p_new_key, p_kind, v_added, p_new_ref, false);
            ELSIF (p_old - p_ignore) IS DISTINCT FROM (p_new - p_ignore) THEN
                PERFORM federation_record(p_repo, p_new_key, p_kind,
                    CASE WHEN p_kind = 'ref' THEN 'ref_moved'
                         WHEN p_old_ref IS DISTINCT FROM p_new_ref THEN 'artifact_added'
                         ELSE 'metadata_changed' END,
                    p_new_ref, false);
            END IF;
        ELSIF v_old_live THEN
            PERFORM federation_record(p_repo, p_old_key, p_kind, v_deleted, p_old_ref, true);
        ELSIF v_new_live THEN
            PERFORM federation_record(p_repo, p_new_key, p_kind, v_added, p_new_ref, false);
        END IF;
    END IF;
END
$$;

-- federation_blob_sha returns the sha256 of a blob row, or '' for no blob.
CREATE FUNCTION federation_blob_sha(p_blob uuid) RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT COALESCE((SELECT sha256 FROM blobs WHERE id = p_blob), '')
$$;
