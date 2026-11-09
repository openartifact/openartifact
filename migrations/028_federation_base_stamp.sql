-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Federation conflict accounting: every log state records the base stamp, the
-- stamp of the unit state the change replaced where it was made. A conflict is
-- a change made without having seen the state it meets; a sequential overwrite
-- (base at or past the local state) is not one. All three columns null means no
-- base: the unit had no previous state there, the row predates this migration or
-- it came from a peer without base stamps.
ALTER TABLE public.federation_units
    ADD COLUMN base_wall bigint,
    ADD COLUMN base_counter integer,
    ADD COLUMN base_origin uuid;

-- The apply function gains the incoming entry's base; the old signature goes.
DROP FUNCTION public.federation_apply_unit(uuid, text, text, text, text, boolean, bigint, integer, uuid);

CREATE OR REPLACE FUNCTION public.federation_apply_unit(p_repo uuid, p_key text, p_kind text, p_event text, p_ref text, p_tombstone boolean, p_wall bigint, p_counter integer, p_origin uuid,
    p_base_wall bigint, p_base_counter integer, p_base_origin uuid) RETURNS boolean
    LANGUAGE plpgsql
    AS $$
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
        (repo_id, unit_key, kind, event, seq, hlc_wall, hlc_counter, origin, tombstone, content_ref, changed_at,
         base_wall, base_counter, base_origin)
    VALUES
        (p_repo, p_key, p_kind, p_event, v_seq, p_wall, p_counter, p_origin, p_tombstone, COALESCE(p_ref, ''), now(),
         p_base_wall, p_base_counter, p_base_origin)
    ON CONFLICT (repo_id, unit_key) DO UPDATE
       SET kind = EXCLUDED.kind, event = EXCLUDED.event, seq = EXCLUDED.seq,
           hlc_wall = EXCLUDED.hlc_wall, hlc_counter = EXCLUDED.hlc_counter,
           origin = EXCLUDED.origin, tombstone = EXCLUDED.tombstone,
           content_ref = EXCLUDED.content_ref, changed_at = EXCLUDED.changed_at,
           base_wall = EXCLUDED.base_wall, base_counter = EXCLUDED.base_counter,
           base_origin = EXCLUDED.base_origin;

    RETURN true;
END
$$;

CREATE OR REPLACE FUNCTION public.federation_record(p_repo uuid, p_key text, p_kind text, p_event text, p_ref text, p_tombstone boolean, p_at timestamp with time zone DEFAULT NULL::timestamp with time zone) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    v_now    bigint := federation_micros(clock_timestamp());
    v_seq    bigint;
    v_wall   bigint;
    v_ctr    integer;
    v_origin uuid;
    v_bw     bigint;
    v_bc     integer;
    v_bo     uuid;
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

    -- The state this change replaces, as known here: the base the peers use to
    -- tell a sequential overwrite from a concurrent write.
    SELECT hlc_wall, hlc_counter, origin INTO v_bw, v_bc, v_bo
      FROM federation_units WHERE repo_id = p_repo AND unit_key = p_key;

    INSERT INTO federation_units AS u
        (repo_id, unit_key, kind, event, seq, hlc_wall, hlc_counter, origin, tombstone, content_ref, changed_at,
         base_wall, base_counter, base_origin)
    VALUES
        (p_repo, p_key, p_kind, p_event, v_seq, v_wall, v_ctr, v_origin, p_tombstone, COALESCE(p_ref, ''), now(),
         v_bw, v_bc, v_bo)
    ON CONFLICT (repo_id, unit_key) DO UPDATE
       SET kind = EXCLUDED.kind, event = EXCLUDED.event, seq = EXCLUDED.seq,
           hlc_wall = EXCLUDED.hlc_wall, hlc_counter = EXCLUDED.hlc_counter,
           origin = EXCLUDED.origin, tombstone = EXCLUDED.tombstone,
           content_ref = EXCLUDED.content_ref, changed_at = EXCLUDED.changed_at,
           base_wall = EXCLUDED.base_wall, base_counter = EXCLUDED.base_counter,
           base_origin = EXCLUDED.base_origin;
END
$$;
