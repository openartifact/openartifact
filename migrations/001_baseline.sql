-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- OpenArtifact 0.15 baseline: the complete schema in one migration.
--
-- Releases before 0.15 shipped 97 incremental migrations. 0.15 squashes them
-- into this file and replaces the per-format package tables with the generic
-- packages / package_versions / package_files / package_tags tables
-- (docs/design/format-plugins.md). A database created by an older release is
-- not upgraded in place: the migrator refuses it, and the data moves with
-- `oa import` (docs/getting-started/upgrading.md).
--
-- Migrations after this one start at 002 and are forward-only.

-- The SQL functions below reference tables created later in this file.
SET LOCAL check_function_bodies = false;

--
--


--
-- Name: pg_trgm; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;


--
-- Name: EXTENSION pg_trgm; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION pg_trgm IS 'text similarity measurement and index searching based on trigrams';


--
-- Name: federation_active(uuid); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.federation_active(p_repo uuid) RETURNS boolean
    LANGUAGE sql STABLE
    AS $$
    SELECT COALESCE(current_setting('oa.federation_apply', true), '') <> 'on'
       AND EXISTS (SELECT 1 FROM federation_repos WHERE repo_id = p_repo)
$$;


--
-- Name: federation_apply_unit(uuid, text, text, text, text, boolean, bigint, integer, uuid); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.federation_apply_unit(p_repo uuid, p_key text, p_kind text, p_event text, p_ref text, p_tombstone boolean, p_wall bigint, p_counter integer, p_origin uuid) RETURNS boolean
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


--
-- Name: federation_blob_sha(uuid); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.federation_blob_sha(p_blob uuid) RETURNS text
    LANGUAGE sql STABLE
    AS $$
    SELECT COALESCE((SELECT sha256 FROM blobs WHERE id = p_blob), '')
$$;


--
-- Name: federation_capture(uuid, text, text, text, text, jsonb, jsonb, text, text, text[]); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.federation_capture(p_repo uuid, p_op text, p_kind text, p_old_key text, p_new_key text, p_old jsonb, p_new jsonb, p_old_ref text, p_new_ref text, p_ignore text[]) RETURNS void
    LANGUAGE plpgsql
    AS $$
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


--
-- Name: federation_micros(timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.federation_micros(p_at timestamp with time zone) RETURNS bigint
    LANGUAGE sql STABLE
    AS $$
    SELECT (extract(epoch FROM p_at) * 1000000)::bigint
$$;


--
-- Name: federation_record(uuid, text, text, text, text, boolean, timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.federation_record(p_repo uuid, p_key text, p_kind text, p_event text, p_ref text, p_tombstone boolean, p_at timestamp with time zone DEFAULT NULL::timestamp with time zone) RETURNS void
    LANGUAGE plpgsql
    AS $$
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


--
-- Name: federation_trg_docker_manifests(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.federation_trg_docker_manifests() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
            DECLARE
                o jsonb;
                n jsonb;
                v_repo uuid;
            BEGIN
                IF TG_OP = 'DELETE' THEN v_repo := OLD.repository_id; ELSE v_repo := NEW.repository_id; END IF;

                IF NOT federation_active(v_repo) THEN
                    RETURN NULL;
                END IF;

                IF TG_OP <> 'INSERT' THEN o := to_jsonb(OLD); END IF;
                IF TG_OP <> 'DELETE' THEN n := to_jsonb(NEW); END IF;

                PERFORM federation_capture(v_repo, TG_OP, 'artifact',
                    CASE WHEN o IS NULL THEN NULL ELSE 'm:' || (o->>'image') || '@' || (o->>'digest') END,
                    CASE WHEN n IS NULL THEN NULL ELSE 'm:' || (n->>'image') || '@' || (n->>'digest') END,
                    o, n,
                    CASE WHEN o IS NULL THEN NULL ELSE o->>'digest' END,
                    CASE WHEN n IS NULL THEN NULL ELSE n->>'digest' END,
                    '{updated_at,deleted_at,deleted_by,search_tsv,download_count}'::text[]);

                RETURN NULL;
            END
            $$;


--
-- Name: federation_trg_docker_tags(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.federation_trg_docker_tags() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
            DECLARE
                o jsonb;
                n jsonb;
                v_repo uuid;
            BEGIN
                IF TG_OP = 'DELETE' THEN v_repo := OLD.repository_id; ELSE v_repo := NEW.repository_id; END IF;

                IF NOT federation_active(v_repo) THEN
                    RETURN NULL;
                END IF;

                IF TG_OP <> 'INSERT' THEN o := to_jsonb(OLD); END IF;
                IF TG_OP <> 'DELETE' THEN n := to_jsonb(NEW); END IF;

                PERFORM federation_capture(v_repo, TG_OP, 'ref',
                    CASE WHEN o IS NULL THEN NULL ELSE 't:' || (o->>'image') || ':' || (o->>'name') END,
                    CASE WHEN n IS NULL THEN NULL ELSE 't:' || (n->>'image') || ':' || (n->>'name') END,
                    o, n,
                    CASE WHEN o IS NULL THEN NULL ELSE o->>'manifest_digest' END,
                    CASE WHEN n IS NULL THEN NULL ELSE n->>'manifest_digest' END,
                    '{updated_at,deleted_at,deleted_by,search_tsv,download_count}'::text[]);

                RETURN NULL;
            END
            $$;


--
-- Name: quota_blob_size(uuid); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.quota_blob_size(p_blob uuid) RETURNS bigint
    LANGUAGE sql STABLE
    AS $$
    SELECT COALESCE((SELECT size_bytes FROM blobs WHERE id = p_blob), 0)
$$;


--
-- Name: quota_pending_add(uuid, bigint, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.quota_pending_add(p_repo uuid, p_size bigint, p_count integer) RETURNS void
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF p_repo IS NULL OR (COALESCE(p_size, 0) <= 0 AND COALESCE(p_count, 0) <= 0) THEN
        RETURN;
    END IF;

    INSERT INTO quota_pending (repository_id, size_bytes, artifact_count, kind)
    VALUES (p_repo, COALESCE(p_size, 0), COALESCE(p_count, 0), 1);
END
$$;


--
-- Name: quota_pending_docker_blobs(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.quota_pending_docker_blobs() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
            BEGIN
                IF TG_OP IN ('DELETE', 'UPDATE') THEN
                    PERFORM quota_pending_add(OLD.repository_id, quota_blob_size(OLD.blob_id), 0);
                END IF;
                IF TG_OP IN ('INSERT', 'UPDATE') THEN
                    PERFORM quota_pending_take(NEW.repository_id, quota_blob_size(NEW.blob_id), 0, false);
                END IF;
                RETURN NULL;
            END
            $$;


--
-- Name: quota_pending_docker_manifests(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.quota_pending_docker_manifests() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
            BEGIN
                IF TG_OP IN ('DELETE', 'UPDATE') THEN
                    PERFORM quota_pending_add(OLD.repository_id, quota_blob_size(OLD.blob_id), 1);
                END IF;
                IF TG_OP IN ('INSERT', 'UPDATE') THEN
                    PERFORM quota_pending_take(NEW.repository_id, quota_blob_size(NEW.blob_id), 1, false);
                END IF;
                RETURN NULL;
            END
            $$;


--
-- Name: quota_pending_take(uuid, bigint, integer, boolean); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.quota_pending_take(p_repo uuid, p_size bigint, p_count integer, p_greedy boolean) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    e           record;
    rem_size    bigint  := p_size;
    rem_count   integer := p_count;
BEGIN
    IF p_repo IS NULL OR (COALESCE(p_size, 0) <= 0 AND COALESCE(p_count, 0) <= 0) THEN
        RETURN;
    END IF;

    DELETE FROM quota_pending
     WHERE id = (SELECT id FROM quota_pending
                  WHERE repository_id = p_repo AND size_bytes = p_size AND artifact_count = p_count
                  ORDER BY id LIMIT 1
                  FOR UPDATE SKIP LOCKED);

    IF FOUND OR NOT p_greedy THEN
        RETURN;
    END IF;

    FOR e IN SELECT id, size_bytes, artifact_count FROM quota_pending
              WHERE repository_id = p_repo ORDER BY id FOR UPDATE SKIP LOCKED
    LOOP
        IF e.size_bytes <= rem_size AND e.artifact_count <= rem_count THEN
            DELETE FROM quota_pending WHERE id = e.id;

            rem_size  := rem_size - e.size_bytes;
            rem_count := rem_count - e.artifact_count;

            EXIT WHEN rem_size = 0 AND rem_count = 0;
        END IF;
    END LOOP;
END
$$;


--
-- Name: recovery_mark(text, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.recovery_mark(p_table text, p_key text) RETURNS void
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO recovery_dirty (table_name, row_key) VALUES (p_table, p_key)
    ON CONFLICT (table_name, row_key)
    DO UPDATE SET seq = nextval('recovery_dirty_seq'), marked_at = now(), retry_at = NULL;
END
$$;


--
-- Name: recovery_trg_docker_blob_images(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.recovery_trg_docker_blob_images() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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


--
-- Name: recovery_trg_docker_manifest_refs(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.recovery_trg_docker_manifest_refs() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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


--
-- Name: recovery_trg_group_members(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.recovery_trg_group_members() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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


--
-- Name: recovery_trg_id(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.recovery_trg_id() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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


--
-- Name: revoke_project_tokens_of_deleted_user(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.revoke_project_tokens_of_deleted_user() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    UPDATE auth_users SET disabled = true
    WHERE id IN (SELECT user_id FROM auth_tokens
                 WHERE created_by = OLD.id AND project_id IS NOT NULL AND revoked_at IS NULL);
    UPDATE auth_tokens SET revoked_at = now()
    WHERE created_by = OLD.id AND project_id IS NOT NULL AND revoked_at IS NULL;
    RETURN OLD;
END;
$$;


--
-- Name: update_updated_at_column(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.update_updated_at_column() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$;


--
-- Name: ai_results; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ai_results (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    scan_job_id uuid NOT NULL,
    summary text DEFAULT ''::text NOT NULL,
    tags jsonb DEFAULT '[]'::jsonb NOT NULL,
    license_status character varying(20) DEFAULT 'UNKNOWN'::character varying NOT NULL,
    license_name text DEFAULT ''::text NOT NULL,
    security_notes text DEFAULT ''::text NOT NULL,
    quarantine_recommended boolean DEFAULT false NOT NULL,
    model_used text DEFAULT ''::text NOT NULL,
    prompt_tokens integer DEFAULT 0 NOT NULL,
    completion_tokens integer DEFAULT 0 NOT NULL,
    artifact_name text DEFAULT ''::text NOT NULL,
    artifact_version text DEFAULT ''::text NOT NULL,
    CONSTRAINT ai_results_license_status_check CHECK (((license_status)::text = ANY ((ARRAY['SAFE'::character varying, 'REVIEW_REQUIRED'::character varying, 'UNKNOWN'::character varying])::text[])))
);


--
-- Name: TABLE ai_results; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.ai_results IS 'AI analysis payloads. FK to scan_jobs.';


--
-- Name: apk_signing_keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.apk_signing_keys (
    repository_id uuid NOT NULL,
    key_name character varying(160) NOT NULL,
    fingerprint text NOT NULL,
    public_key text NOT NULL,
    private_key bytea NOT NULL,
    created_by text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE apk_signing_keys; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.apk_signing_keys IS 'Alpine repository index signing keys; private_key is sealed with internal/crypto.';


--
-- Name: artifact_properties; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.artifact_properties (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    key character varying(255) NOT NULL,
    value text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    artifact_ref_id uuid
);


--
-- Name: TABLE artifact_properties; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.artifact_properties IS 'Custom key-value metadata for artifacts';


--
-- Name: COLUMN artifact_properties.artifact_ref_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.artifact_properties.artifact_ref_id IS 'FK to artifact_refs.';


--
-- Name: artifact_refs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.artifact_refs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    artifact_path text NOT NULL,
    format character varying(50) NOT NULL,
    sha256 character varying(64),
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE artifact_refs; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.artifact_refs IS 'Universal scan target anchors. One row per (repository, artifact_path). artifact_path = HTTP request path used to fetch the artifact. Format-agnostic. Created on scan submission. Cascades on repository deletion.';


--
-- Name: audit_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.audit_logs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    action character varying(50) NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP,
    project_id uuid,
    repository_id uuid,
    actor_id uuid,
    actor_type character varying(20),
    details_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    resource_type character varying(50),
    resource_id text,
    ip_address inet,
    CONSTRAINT audit_logs_actor_type_check CHECK (((actor_type)::text = ANY ((ARRAY['user'::character varying, 'system'::character varying, 'token'::character varying, 'sso'::character varying])::text[]))),
    CONSTRAINT chk_audit_action CHECK (((action)::text = ANY (ARRAY[('artifact.uploaded'::character varying)::text, ('artifact.downloaded'::character varying)::text, ('artifact.deleted'::character varying)::text, ('artifact.moved'::character varying)::text, ('artifact.promoted'::character varying)::text, ('artifact.quarantined'::character varying)::text, ('artifact.released'::character varying)::text, ('artifact.restored'::character varying)::text, ('artifact.purged'::character varying)::text, ('repository.created'::character varying)::text, ('repository.updated'::character varying)::text, ('repository.deleted'::character varying)::text, ('repository.blocked'::character varying)::text, ('repository.unblocked'::character varying)::text, ('repository.restored'::character varying)::text, ('repository.purged'::character varying)::text, ('auth.login'::character varying)::text, ('auth.logout'::character varying)::text, ('auth.login_failed'::character varying)::text, ('auth.token_created'::character varying)::text, ('auth.token_revoked'::character varying)::text, ('auth.password_changed'::character varying)::text, ('auth.sso_login'::character varying)::text, ('auth.token_validate'::character varying)::text, ('permission.target_created'::character varying)::text, ('permission.target_updated'::character varying)::text, ('permission.target_deleted'::character varying)::text, ('permission.assigned'::character varying)::text, ('permission.revoked'::character varying)::text, ('org.created'::character varying)::text, ('org.updated'::character varying)::text, ('org.deleted'::character varying)::text, ('org.member_added'::character varying)::text, ('org.member_removed'::character varying)::text, ('org.member_role_changed'::character varying)::text, ('project.created'::character varying)::text, ('project.updated'::character varying)::text, ('project.deleted'::character varying)::text, ('project.policy_changed'::character varying)::text, ('project.quota_changed'::character varying)::text, ('project.member_added'::character varying)::text, ('project.member_removed'::character varying)::text, ('project.member_role_changed'::character varying)::text, ('project.group_added'::character varying)::text, ('project.group_removed'::character varying)::text, ('project.group_role_changed'::character varying)::text, ('project.token_created'::character varying)::text, ('project.token_revoked'::character varying)::text, ('project.repository_created'::character varying)::text, ('project.repository_deleted'::character varying)::text, ('project.repository_transferred'::character varying)::text, ('project.cleanup_policy_changed'::character varying)::text, ('system.setting_changed'::character varying)::text, ('system.storage_backend_changed'::character varying)::text, ('scan.vuln_detected'::character varying)::text, ('scan.vuln_resolved'::character varying)::text, ('scan.secret_detected'::character varying)::text, ('scan.quarantine_applied'::character varying)::text, ('bundle.created'::character varying)::text, ('bundle.sealed'::character varying)::text, ('bundle.archived'::character varying)::text, ('bundle.promoted'::character varying)::text, ('http.request'::character varying)::text, ('user.created'::character varying)::text, ('user.updated'::character varying)::text, ('user.disabled'::character varying)::text, ('user.deleted'::character varying)::text, ('group.created'::character varying)::text, ('group.updated'::character varying)::text, ('group.deleted'::character varying)::text, ('group.member_added'::character varying)::text, ('group.member_removed'::character varying)::text, ('group.members_set'::character varying)::text, ('permission_target.created'::character varying)::text, ('permission_target.updated'::character varying)::text, ('permission_target.deleted'::character varying)::text, ('permission_target.assigned'::character varying)::text, ('permission_target.unassigned'::character varying)::text, ('sso_provider.created'::character varying)::text, ('sso_provider.updated'::character varying)::text, ('sso_provider.deleted'::character varying)::text, ('token.created_for_user'::character varying)::text, ('gpg_key.created'::character varying)::text, ('gpg_key.deleted'::character varying)::text, ('gpg_key.activated'::character varying)::text, ('gpg_key.deactivated'::character varying)::text, ('gpg_key.exported'::character varying)::text])))
);


--
-- Name: TABLE audit_logs; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.audit_logs IS 'Compliance-grade audit trail. actor_id FK to auth_users, details_json JSONB.';


--
-- Name: COLUMN audit_logs.actor_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.audit_logs.actor_id IS 'FK to auth_users.';


--
-- Name: COLUMN audit_logs.details_json; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.audit_logs.details_json IS 'Structured event details as JSONB.';


--
-- Name: auth_groups; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_groups (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(64) NOT NULL,
    description text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    project_id uuid
);


--
-- Name: TABLE auth_groups; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.auth_groups IS 'Groups for organizing users and assigning permissions.';


--
-- Name: COLUMN auth_groups.project_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.auth_groups.project_id IS 'Project this group belongs to. NULL = system-level group (pre-multitenancy).';


--
-- Name: auth_saml_assertions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_saml_assertions (
    assertion_id text NOT NULL,
    provider_slug text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE auth_saml_assertions; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.auth_saml_assertions IS 'Consumed SAML assertion IDs (replay protection); rows are purgeable after expires_at';


--
-- Name: auth_session_sso; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_session_sso (
    session_id uuid NOT NULL,
    provider_slug text NOT NULL,
    id_token text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: auth_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    token_hash text NOT NULL,
    ip_address inet,
    user_agent text,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE auth_sessions; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.auth_sessions IS 'Browser session tokens stored as SHA-256 hashes';


--
-- Name: auth_sso_providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_sso_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    slug character varying(64) NOT NULL,
    kind character varying(8) NOT NULL,
    display_name text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    oidc_config jsonb,
    saml_config jsonb,
    claim_mapping jsonb,
    group_mapping jsonb DEFAULT '{}'::jsonb NOT NULL,
    auto_provision_users boolean DEFAULT true NOT NULL,
    link_by_email boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    permission_mapping jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT auth_sso_providers_kind_check CHECK (((kind)::text = ANY ((ARRAY['oidc'::character varying, 'saml'::character varying])::text[]))),
    CONSTRAINT chk_sso_config_matches_kind CHECK (((((kind)::text = 'oidc'::text) AND (oidc_config IS NOT NULL)) OR (((kind)::text = 'saml'::text) AND (saml_config IS NOT NULL))))
);


--
-- Name: TABLE auth_sso_providers; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.auth_sso_providers IS 'SSO provider configuration for OIDC and SAML identity providers';


--
-- Name: auth_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    name character varying(128) NOT NULL,
    kind character varying(16) NOT NULL,
    token_hash text NOT NULL,
    scopes jsonb DEFAULT '[]'::jsonb NOT NULL,
    expires_at timestamp with time zone,
    last_used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    revoked_at timestamp with time zone,
    project_id uuid,
    created_by uuid,
    project_role text,
    CONSTRAINT auth_tokens_kind_check CHECK (((kind)::text = ANY ((ARRAY['access'::character varying, 'deploy'::character varying])::text[]))),
    CONSTRAINT chk_token_project CHECK (((project_id IS NULL) = (project_role IS NULL))),
    CONSTRAINT chk_token_project_role CHECK (((project_role IS NULL) OR (project_role = ANY (ARRAY['viewer'::text, 'developer'::text]))))
);


--
-- Name: TABLE auth_tokens; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.auth_tokens IS 'Personal access tokens and deploy tokens stored as SHA-256 hashes';


--
-- Name: COLUMN auth_tokens.project_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.auth_tokens.project_id IS 'Set for project tokens: the project the token belongs to.';


--
-- Name: COLUMN auth_tokens.created_by; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.auth_tokens.created_by IS 'Project tokens: the user who minted the token; the token dies with that user''s membership.';


--
-- Name: COLUMN auth_tokens.project_role; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.auth_tokens.project_role IS 'Project tokens: viewer or developer.';


--
-- Name: auth_user_groups; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_user_groups (
    user_id uuid NOT NULL,
    group_id uuid NOT NULL,
    is_sso_managed boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE auth_user_groups; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.auth_user_groups IS 'Many-to-many membership between users and groups';


--
-- Name: COLUMN auth_user_groups.updated_at; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.auth_user_groups.updated_at IS 'Auto-updated by trigger on every UPDATE.';


--
-- Name: auth_users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    username character varying(64) NOT NULL,
    email character varying(254),
    password_hash text,
    full_name text,
    disabled boolean DEFAULT false NOT NULL,
    must_change_password boolean DEFAULT false NOT NULL,
    last_login_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    external_id text,
    provider_slug text,
    sso_managed_groups boolean DEFAULT false NOT NULL,
    kind text DEFAULT 'human'::text NOT NULL,
    project_id uuid,
    password_expires_at timestamp with time zone,
    CONSTRAINT chk_auth_user_kind CHECK ((kind = ANY (ARRAY['human'::text, 'service'::text]))),
    CONSTRAINT chk_service_user_project CHECK (((kind = 'service'::text) = (project_id IS NOT NULL)))
);


--
-- Name: TABLE auth_users; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.auth_users IS 'User accounts for authentication and authorization';


--
-- Name: COLUMN auth_users.kind; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.auth_users.kind IS 'human: a person; service: the account behind one project token.';


--
-- Name: COLUMN auth_users.project_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.auth_users.project_id IS 'The only project a service user belongs to.';


--
-- Name: COLUMN auth_users.password_expires_at; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.auth_users.password_expires_at IS 'After this instant the password is rejected at login (generated bootstrap password); NULL = never. Cleared whenever the password changes.';


--
-- Name: backup_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.backup_jobs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    type character varying(20) DEFAULT 'full'::character varying NOT NULL,
    storage_path text,
    size_bytes bigint,
    error_message text,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_backup_status CHECK (((status)::text = ANY ((ARRAY['pending'::character varying, 'running'::character varying, 'completed'::character varying, 'failed'::character varying])::text[]))),
    CONSTRAINT chk_backup_type CHECK (((type)::text = ANY ((ARRAY['full'::character varying, 'metadata-only'::character varying])::text[])))
);


--
-- Name: TABLE backup_jobs; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.backup_jobs IS 'Backup job tracking';


--
-- Name: backup_leases; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.backup_leases (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    snapshot text,
    snapshot_at timestamp with time zone DEFAULT now() NOT NULL,
    heartbeat_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL
);


--
-- Name: TABLE backup_leases; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.backup_leases IS 'Running backups; while a lease is live the blob bytes its snapshot may still reference are not deleted (blob_deletions).';


--
-- Name: blob_deletions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.blob_deletions (
    sha256 character varying(64) NOT NULL,
    scope text NOT NULL,
    queued_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_xid bigint DEFAULT ((pg_current_xact_id())::text)::bigint NOT NULL,
    not_before timestamp with time zone NOT NULL,
    claimed_until timestamp with time zone,
    attempts integer DEFAULT 0 NOT NULL
);


--
-- Name: TABLE blob_deletions; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.blob_deletions IS 'Blob copies whose bytes are waiting to be deleted: after the grace period and once no live backup snapshot can still need them (backup_leases).';


--
-- Name: COLUMN blob_deletions.scope; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.blob_deletions.scope IS '''*'' = every copy of the blob; '''' = the primary store copy; otherwise the storage_backends id of the tier whose copy is retired.';


--
-- Name: COLUMN blob_deletions.deleted_xid; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.blob_deletions.deleted_xid IS 'Top-level transaction id of the deleting transaction; compared with backup_leases.snapshot.';


--
-- Name: blob_replication; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.blob_replication (
    sha256 character varying(64) NOT NULL,
    op text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    claimed_until timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    enqueued_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT blob_replication_op_check CHECK ((op = ANY (ARRAY['copy'::text, 'delete'::text]))),
    CONSTRAINT blob_replication_sha256_hex CHECK (((sha256)::text ~ '^[0-9a-f]{64}$'::text))
);


--
-- Name: TABLE blob_replication; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.blob_replication IS 'Pending blob replication work: one row per blob, op = copy or delete; the latest operation wins.';


--
-- Name: COLUMN blob_replication.version; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.blob_replication.version IS 'Bumped when op changes; a worker completes or fails a row only for the version it claimed.';


--
-- Name: blob_replication_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.blob_replication_state (
    id smallint DEFAULT 1 NOT NULL,
    replicated_total bigint DEFAULT 0 NOT NULL,
    replicated_bytes bigint DEFAULT 0 NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    last_error_at timestamp with time zone,
    verify_requested boolean DEFAULT false NOT NULL,
    verify_cursor text DEFAULT ''::text NOT NULL,
    verify_run jsonb,
    last_verify jsonb,
    CONSTRAINT blob_replication_state_singleton CHECK ((id = 1))
);


--
-- Name: TABLE blob_replication_state; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.blob_replication_state IS 'Singleton row: blob replication totals and verify progress.';


--
-- Name: blobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.blobs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    sha256 character varying(64) NOT NULL,
    size_bytes bigint NOT NULL,
    media_type text DEFAULT 'application/octet-stream'::text NOT NULL,
    storage_backend_id uuid,
    blob_key text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_accessed_at timestamp with time zone,
    tier_changed_at timestamp with time zone
);


--
-- Name: TABLE blobs; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.blobs IS 'Global content-addressed blob store. One row per unique sha256. Cross-format, cross-repo deduplication. GC via ref_count. All format tables reference blobs via blob_id FK.';


--
-- Name: COLUMN blobs.storage_backend_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.blobs.storage_backend_id IS 'Authoritative placement: the storage backend holding the blob (NULL = the primary store the server is configured with). Changed only by the tier mover, in one transaction.';


--
-- Name: COLUMN blobs.blob_key; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.blobs.blob_key IS 'Key used to retrieve the blob from the configured storage backend.';


--
-- Name: COLUMN blobs.last_accessed_at; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.blobs.last_accessed_at IS 'Last client download, coalesced (resolution storage tiering access resolution); NULL = never read since tracking started.';


--
-- Name: COLUMN blobs.tier_changed_at; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.blobs.tier_changed_at IS 'When the mover last flipped the placement; NULL = never moved.';


--
-- Name: bundle_promotions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bundle_promotions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bundle_id uuid NOT NULL,
    from_stage character varying(255),
    to_stage character varying(255) NOT NULL,
    target_repo_id uuid NOT NULL,
    status character varying(20) DEFAULT 'in_progress'::character varying NOT NULL,
    notes text,
    error_message text,
    item_count integer DEFAULT 0 NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    promoter_id uuid,
    CONSTRAINT bundle_promotions_status_check CHECK (((status)::text = ANY ((ARRAY['in_progress'::character varying, 'success'::character varying, 'failed'::character varying])::text[])))
);


--
-- Name: TABLE bundle_promotions; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.bundle_promotions IS 'Immutable audit trail of every bundle promotion attempt';


--
-- Name: COLUMN bundle_promotions.promoter_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.bundle_promotions.promoter_id IS 'FK to auth_users.';


--
-- Name: cleanup_policies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.cleanup_policies (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid,
    name character varying(255) NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    keep_last_n integer,
    keep_since_days integer,
    keep_downloaded_since_days integer,
    name_pattern text,
    schedule_cron text DEFAULT '0 2 * * *'::text NOT NULL,
    last_run_at timestamp with time zone,
    last_run_deleted integer,
    last_run_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    project_id uuid,
    CONSTRAINT chk_cleanup_policy_scope CHECK (((repository_id IS NULL) <> (project_id IS NULL)))
);


--
-- Name: TABLE cleanup_policies; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.cleanup_policies IS 'Cleanup/retention policies for automatic artifact deletion';


--
-- Name: COLUMN cleanup_policies.project_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.cleanup_policies.project_id IS 'Set for a project default policy (repository_id is then NULL).';


--
-- Name: debian_architectures; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.debian_architectures (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    distribution_id uuid NOT NULL,
    name character varying(50) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: debian_components; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.debian_components (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    distribution_id uuid NOT NULL,
    name character varying(255) NOT NULL,
    description text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: debian_distributions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.debian_distributions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    name character varying(255) NOT NULL,
    codename character varying(255),
    description text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: declarative_objects; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.declarative_objects (
    kind text NOT NULL,
    name text NOT NULL,
    object_id text NOT NULL,
    applied_hash text DEFAULT ''::text NOT NULL,
    applied_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT declarative_objects_applied_hash_check CHECK ((char_length(applied_hash) <= 128)),
    CONSTRAINT declarative_objects_kind_check CHECK (((char_length(kind) >= 1) AND (char_length(kind) <= 64))),
    CONSTRAINT declarative_objects_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 512))),
    CONSTRAINT declarative_objects_object_id_check CHECK (((char_length(object_id) >= 1) AND (char_length(object_id) <= 256)))
);


--
-- Name: TABLE declarative_objects; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.declarative_objects IS 'Objects created or adopted by oa apply; object_id is the server-side id at apply time, so a deleted-and-recreated object is not claimed by a stale marker.';


--
-- Name: dependency_track_pushes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.dependency_track_pushes (
    repository_id uuid NOT NULL,
    artifact_id text NOT NULL,
    state character varying(16) NOT NULL,
    detail text DEFAULT ''::text NOT NULL,
    project_uuid text DEFAULT ''::text NOT NULL,
    project_name text DEFAULT ''::text NOT NULL,
    project_version text DEFAULT ''::text NOT NULL,
    components integer DEFAULT 0 NOT NULL,
    vex_statements integer DEFAULT 0 NOT NULL,
    vex_applied integer DEFAULT 0 NOT NULL,
    bom_pushed_at timestamp with time zone,
    vex_pushed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT dependency_track_pushes_state_check CHECK (((state)::text = ANY ((ARRAY['queued'::character varying, 'running'::character varying, 'done'::character varying, 'skipped'::character varying, 'failed'::character varying])::text[])))
);


--
-- Name: TABLE dependency_track_pushes; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.dependency_track_pushes IS 'Last BOM/VEX push of an artifact to Dependency-Track: state, counts and the DT project.';


--
-- Name: docker_blob_images; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.docker_blob_images (
    repository_id uuid NOT NULL,
    image text DEFAULT ''::text NOT NULL,
    digest text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE docker_blob_images; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.docker_blob_images IS 'Images of a repository that may serve and reference a blob (pushed, mounted or referenced by one of their manifests). A docker_blobs row with no entry here predates this table and stays visible to every image of its repository.';


--
-- Name: docker_blobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.docker_blobs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    digest text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    last_used_at timestamp with time zone DEFAULT now() NOT NULL,
    size_bytes bigint
);


--
-- Name: TABLE docker_blobs; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.docker_blobs IS 'OCI/Docker blob registry: per-repo existence index. Content (size, media_type, storage key) is in the global blobs table via blob_id. Shared blobs are deduplicated across repositories.';


--
-- Name: COLUMN docker_blobs.digest; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.docker_blobs.digest IS 'OCI digest in "sha256:<hex>" format. Used by OCI registry protocol.';


--
-- Name: COLUMN docker_blobs.last_used_at; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.docker_blobs.last_used_at IS 'Last upload, mount, re-push or HEAD of this blob in the repository; registry GC keeps blobs used within its grace period.';


--
-- Name: COLUMN docker_blobs.size_bytes; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.docker_blobs.size_bytes IS 'Size in bytes verified at upload; NULL for rows created before it was recorded.';


--
-- Name: docker_manifest_refs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.docker_manifest_refs (
    manifest_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    image text DEFAULT ''::text NOT NULL,
    kind text NOT NULL,
    digest text DEFAULT ''::text NOT NULL,
    CONSTRAINT docker_manifest_refs_kind_check CHECK ((kind = ANY (ARRAY['blob'::text, 'manifest'::text, 'indexed'::text])))
);


--
-- Name: TABLE docker_manifest_refs; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.docker_manifest_refs IS 'References held by a manifest body: kind=blob (config, layers), kind=manifest (index children); kind=indexed marks the manifest as fully indexed.';


--
-- Name: COLUMN docker_manifest_refs.repository_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.docker_manifest_refs.repository_id IS 'Denormalised from docker_manifests so blob references can be looked up per repository without a join.';


--
-- Name: COLUMN docker_manifest_refs.image; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.docker_manifest_refs.image IS 'Image of the referencing manifest; child manifests are resolved inside the same image.';


--
-- Name: docker_manifests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.docker_manifests (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    digest text NOT NULL,
    media_type text NOT NULL,
    size bigint DEFAULT 0 NOT NULL,
    subject_digest text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    image text DEFAULT ''::text NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: TABLE docker_manifests; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.docker_manifests IS 'OCI/Docker image manifests indexed by digest';


--
-- Name: COLUMN docker_manifests.image; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.docker_manifests.image IS 'Image name inside the repository (distribution name minus its first segment); empty for the default image.';


--
-- Name: docker_tags; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.docker_tags (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    name text NOT NULL,
    manifest_digest text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    image text DEFAULT ''::text NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: TABLE docker_tags; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.docker_tags IS 'Named tags pointing to manifests in a repository';


--
-- Name: COLUMN docker_tags.image; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.docker_tags.image IS 'Image name inside the repository (distribution name minus its first segment); empty for the default image.';


--
-- Name: docker_uploads; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.docker_uploads (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    upload_uuid text NOT NULL,
    "offset" bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE docker_uploads; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.docker_uploads IS 'In-progress chunked blob upload sessions';


--
-- Name: epss_scores; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.epss_scores (
    cve_id text NOT NULL,
    score double precision NOT NULL,
    percentile double precision NOT NULL,
    generation bigint NOT NULL
);


--
-- Name: TABLE epss_scores; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.epss_scores IS 'FIRST EPSS scores (probability and percentile) keyed by upper-case CVE id; generation is the import that last saw the row.';


--
-- Name: exploit_feeds; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.exploit_feeds (
    name text NOT NULL,
    source_url text DEFAULT ''::text NOT NULL,
    version text DEFAULT ''::text NOT NULL,
    published_at timestamp with time zone,
    entry_count integer DEFAULT 0 NOT NULL,
    generation bigint DEFAULT 0 NOT NULL,
    etag text DEFAULT ''::text NOT NULL,
    last_modified text DEFAULT ''::text NOT NULL,
    imported_at timestamp with time zone,
    checked_at timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    CONSTRAINT exploit_feeds_name_check CHECK ((name = ANY (ARRAY['kev'::text, 'epss'::text])))
);


--
-- Name: TABLE exploit_feeds; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.exploit_feeds IS 'State of the exploitation-signal feeds: what is loaded (version, publication date, rows), the validators for conditional requests, the last check and its error.';


--
-- Name: federation_deferred; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.federation_deferred (
    peer_id uuid NOT NULL,
    unit_key text NOT NULL,
    seq bigint NOT NULL,
    attempts integer DEFAULT 1 NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    first_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: federation_instance; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.federation_instance (
    singleton boolean DEFAULT true NOT NULL,
    instance_id uuid DEFAULT gen_random_uuid() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT federation_instance_singleton_check CHECK (singleton)
);


--
-- Name: federation_peer_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.federation_peer_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repo_id uuid NOT NULL,
    name text NOT NULL,
    token_hash bytea NOT NULL,
    created_by text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone,
    revoked_at timestamp with time zone
);


--
-- Name: federation_peers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.federation_peers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repo_id uuid NOT NULL,
    name text NOT NULL,
    base_url text NOT NULL,
    token_enc text NOT NULL,
    ca_pem text DEFAULT ''::text NOT NULL,
    client_cert_enc text DEFAULT ''::text NOT NULL,
    client_key_enc text DEFAULT ''::text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    cursor_seq bigint DEFAULT 0 NOT NULL,
    remote_head_seq bigint DEFAULT 0 NOT NULL,
    remote_log_id uuid,
    last_attempt_at timestamp with time zone,
    last_success_at timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    error_count integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone,
    oldest_pending_at timestamp with time zone,
    applied_total bigint DEFAULT 0 NOT NULL,
    conflicts_total bigint DEFAULT 0 NOT NULL,
    resync_requested boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: federation_repos; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.federation_repos (
    repo_id uuid NOT NULL,
    log_id uuid DEFAULT gen_random_uuid() NOT NULL,
    head_seq bigint DEFAULT 0 NOT NULL,
    hlc_wall bigint DEFAULT 0 NOT NULL,
    hlc_counter integer DEFAULT 0 NOT NULL,
    enabled_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: federation_units; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.federation_units (
    repo_id uuid NOT NULL,
    unit_key text NOT NULL,
    kind text NOT NULL,
    event text NOT NULL,
    seq bigint NOT NULL,
    hlc_wall bigint NOT NULL,
    hlc_counter integer NOT NULL,
    origin uuid NOT NULL,
    tombstone boolean DEFAULT false NOT NULL,
    content_ref text DEFAULT ''::text NOT NULL,
    changed_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT federation_units_event_check CHECK ((event = ANY (ARRAY['artifact_added'::text, 'artifact_deleted'::text, 'ref_moved'::text, 'metadata_changed'::text]))),
    CONSTRAINT federation_units_kind_check CHECK ((kind = ANY (ARRAY['artifact'::text, 'ref'::text])))
);


--
-- Name: gpg_key_repositories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.gpg_key_repositories (
    repository_id uuid NOT NULL,
    gpg_key_id uuid NOT NULL,
    assigned_by text DEFAULT ''::text NOT NULL,
    assigned_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE gpg_key_repositories; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.gpg_key_repositories IS 'Per-repository signing key override; wins over the global active key.';


--
-- Name: gpg_keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.gpg_keys (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(255) NOT NULL,
    fingerprint character varying(64) NOT NULL,
    key_id character varying(16) NOT NULL,
    algorithm character varying(32) NOT NULL,
    user_id text DEFAULT ''::text NOT NULL,
    public_key text NOT NULL,
    private_key_enc text NOT NULL,
    is_global_active boolean DEFAULT false NOT NULL,
    source character varying(16) DEFAULT 'imported'::character varying NOT NULL,
    created_by text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone,
    CONSTRAINT gpg_keys_source_check CHECK (((source)::text = ANY ((ARRAY['imported'::character varying, 'generated'::character varying, 'auto'::character varying])::text[])))
);


--
-- Name: TABLE gpg_keys; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.gpg_keys IS 'GPG signing keys managed through the admin API. private_key_enc is sealed by internal/crypto and never returned.';


--
-- Name: group_metadata_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.group_metadata_state (
    repository_id uuid NOT NULL,
    stale boolean DEFAULT true NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    generated_at timestamp with time zone,
    refresh_after timestamp with time zone,
    dists jsonb DEFAULT '[]'::jsonb NOT NULL
);


--
-- Name: group_package_owners; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.group_package_owners (
    group_repo_id uuid NOT NULL,
    path text NOT NULL,
    member_repo_id uuid NOT NULL
);


--
-- Name: kev_entries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.kev_entries (
    cve_id text NOT NULL,
    vendor_project text DEFAULT ''::text NOT NULL,
    product text DEFAULT ''::text NOT NULL,
    vulnerability_name text DEFAULT ''::text NOT NULL,
    date_added date NOT NULL,
    due_date date,
    known_ransomware boolean DEFAULT false NOT NULL
);


--
-- Name: TABLE kev_entries; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.kev_entries IS 'CISA Known Exploited Vulnerabilities catalog, keyed by upper-case CVE id (CC0 1.0).';


--
-- Name: license_policies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.license_policies (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid,
    name character varying(255) NOT NULL,
    description text,
    action character varying(20) DEFAULT 'warn'::character varying NOT NULL,
    allowed_licenses text[] DEFAULT '{}'::text[] NOT NULL,
    denied_licenses text[] DEFAULT '{}'::text[] NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_license_policy_action CHECK (((action)::text = ANY ((ARRAY['warn'::character varying, 'block'::character varying, 'allow'::character varying])::text[])))
);


--
-- Name: TABLE license_policies; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.license_policies IS 'License compliance policies (repo-specific and global)';


--
-- Name: license_results; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.license_results (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    scan_job_id uuid NOT NULL,
    licenses text[] DEFAULT '{}'::text[] NOT NULL,
    spdx_expressions text[] DEFAULT '{}'::text[] NOT NULL,
    quarantined boolean DEFAULT false NOT NULL,
    quarantine_approved boolean DEFAULT false NOT NULL,
    unresolved_licenses text[] DEFAULT '{}'::text[] NOT NULL,
    license_source text DEFAULT 'client_declared'::text NOT NULL,
    CONSTRAINT license_results_license_source_check CHECK ((license_source = ANY (ARRAY['artifact'::text, 'client_declared'::text])))
);


--
-- Name: TABLE license_results; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.license_results IS 'License scan payloads. FK to scan_jobs.';


--
-- Name: COLUMN license_results.quarantined; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.license_results.quarantined IS 'Downloads of the artifact are refused (ARTIFACT_QUARANTINED) while true.';


--
-- Name: COLUMN license_results.quarantine_approved; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.license_results.quarantine_approved IS 'An admin approved the artifact for the recorded licenses; cleared when they change.';


--
-- Name: COLUMN license_results.unresolved_licenses; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.license_results.unresolved_licenses IS 'Entries of licenses that are not SPDX expressions (kept raw, flagged for review).';


--
-- Name: COLUMN license_results.license_source; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.license_results.license_source IS 'artifact: read from the package file; client_declared: only form fields or properties.';


--
-- Name: nix_pending_nars; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.nix_pending_nars (
    repository_id uuid NOT NULL,
    sha256 text NOT NULL,
    nar_file text NOT NULL,
    blob_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE nix_pending_nars; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.nix_pending_nars IS 'Uploaded NAR files waiting for their narinfo; swept by the cleanup worker.';


--
-- Name: nix_signing_keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.nix_signing_keys (
    repository_id uuid NOT NULL,
    key_name character varying(160) NOT NULL,
    public_key text NOT NULL,
    private_key bytea NOT NULL,
    created_by text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE nix_signing_keys; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.nix_signing_keys IS 'Nix narinfo signing keys; private_key is sealed with internal/crypto.';


--
-- Name: osv_advisories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.osv_advisories (
    id text NOT NULL,
    modified timestamp with time zone NOT NULL,
    published timestamp with time zone,
    withdrawn timestamp with time zone,
    summary text DEFAULT ''::text NOT NULL,
    severity jsonb DEFAULT '[]'::jsonb NOT NULL,
    db_severity text DEFAULT ''::text NOT NULL,
    cwe_ids text[] DEFAULT '{}'::text[] NOT NULL,
    refs text[] DEFAULT '{}'::text[] NOT NULL,
    imported_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: osv_advisory_aliases; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.osv_advisory_aliases (
    advisory_id text NOT NULL,
    alias text NOT NULL
);


--
-- Name: osv_affected; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.osv_affected (
    id bigint NOT NULL,
    advisory_id text NOT NULL,
    ecosystem text NOT NULL,
    name text NOT NULL,
    name_norm text NOT NULL,
    ranges jsonb DEFAULT '[]'::jsonb NOT NULL,
    versions text[] DEFAULT '{}'::text[] NOT NULL
);


--
-- Name: osv_affected_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.osv_affected ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.osv_affected_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: osv_mirror_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.osv_mirror_state (
    ecosystem text NOT NULL,
    high_water timestamp with time zone,
    etag text DEFAULT ''::text NOT NULL,
    archive_modified text DEFAULT ''::text NOT NULL,
    source_url text DEFAULT ''::text NOT NULL,
    advisories integer DEFAULT 0 NOT NULL,
    last_checked_at timestamp with time zone,
    last_imported_at timestamp with time zone,
    last_import_seconds double precision DEFAULT 0 NOT NULL,
    last_rematch_seconds double precision DEFAULT 0 NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    rematch_pending boolean DEFAULT false NOT NULL,
    rematched_at timestamp with time zone
);


--
-- Name: package_deltas; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.package_deltas (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    from_sha256 character varying(64) NOT NULL,
    from_filename character varying(512) NOT NULL,
    to_sha256 character varying(64) NOT NULL,
    to_filename character varying(512) NOT NULL,
    delta_sha256 character varying(64) NOT NULL,
    delta_size bigint DEFAULT 0 NOT NULL,
    format character varying(16) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE package_deltas; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.package_deltas IS 'Pre-computed bsdiff patches between package versions for bandwidth-efficient updates';


--
-- Name: permission_assignments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.permission_assignments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    permission_target_id uuid NOT NULL,
    principal_type character varying(8) NOT NULL,
    principal_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT permission_assignments_principal_type_check CHECK (((principal_type)::text = ANY ((ARRAY['user'::character varying, 'group'::character varying])::text[])))
);


--
-- Name: TABLE permission_assignments; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.permission_assignments IS 'Assignments of permission targets to users or groups';


--
-- Name: permission_targets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.permission_targets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(128) NOT NULL,
    repository_filter text NOT NULL,
    path_pattern text NOT NULL,
    actions text[] NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    project_id uuid DEFAULT '00000000-0000-0000-0000-000000000001'::uuid CONSTRAINT permission_targets_org_id_not_null NOT NULL
);


--
-- Name: TABLE permission_targets; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.permission_targets IS 'Permission targets defining allowed actions on repositories. Each target belongs to exactly one project.';


--
-- Name: COLUMN permission_targets.project_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.permission_targets.project_id IS 'Project this permission target belongs to. NOT NULL.';


--
-- Name: presigned_uploads; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.presigned_uploads (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    path text NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 text NOT NULL,
    content_type text DEFAULT ''::text NOT NULL,
    created_by text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    completing_since timestamp with time zone,
    completed_at timestamp with time zone,
    artifact_id text DEFAULT ''::text NOT NULL,
    part_size bigint DEFAULT 0 NOT NULL,
    multipart_id text DEFAULT ''::text NOT NULL,
    CONSTRAINT presigned_uploads_part_size_check CHECK ((part_size >= 0)),
    CONSTRAINT presigned_uploads_sha256_check CHECK ((sha256 ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT presigned_uploads_size_check CHECK ((size_bytes >= 0))
);


--
-- Name: TABLE presigned_uploads; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.presigned_uploads IS 'Presigned uploads awaiting their complete call; the object lives at the staging key staging/<id> in the blob store.';


--
-- Name: COLUMN presigned_uploads.part_size; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.presigned_uploads.part_size IS 'Planned size of each part of a multipart upload; 0 for a single-PUT upload.';


--
-- Name: COLUMN presigned_uploads.multipart_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.presigned_uploads.multipart_id IS 'Backend handle (S3 upload id) of the open multipart upload; empty when the backend needs none.';


--
-- Name: project_groups; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.project_groups (
    project_id uuid NOT NULL,
    group_id uuid NOT NULL,
    role text NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_project_group_role CHECK ((role = ANY (ARRAY['viewer'::text, 'developer'::text, 'maintainer'::text])))
);


--
-- Name: TABLE project_groups; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.project_groups IS 'Groups attached to a project with a role (ownership stays personal).';


--
-- Name: project_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.project_members (
    project_id uuid CONSTRAINT organization_members_org_id_not_null NOT NULL,
    user_id uuid CONSTRAINT organization_members_user_id_not_null NOT NULL,
    role character varying(50) DEFAULT 'member'::character varying CONSTRAINT organization_members_role_not_null NOT NULL,
    created_at timestamp with time zone DEFAULT now() CONSTRAINT organization_members_created_at_not_null NOT NULL,
    updated_at timestamp with time zone DEFAULT now() CONSTRAINT organization_members_updated_at_not_null NOT NULL,
    CONSTRAINT chk_project_member_role CHECK (((role)::text = ANY ((ARRAY['owner'::character varying, 'maintainer'::character varying, 'developer'::character varying, 'viewer'::character varying])::text[])))
);


--
-- Name: TABLE project_members; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.project_members IS 'Project membership with roles (owner, maintainer, developer, viewer)';


--
-- Name: COLUMN project_members.updated_at; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.project_members.updated_at IS 'Auto-updated by trigger on every UPDATE.';


--
-- Name: project_quotas; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.project_quotas (
    project_id uuid NOT NULL,
    max_size_bytes bigint DEFAULT 0 NOT NULL,
    max_artifact_count bigint DEFAULT 0 NOT NULL,
    warn_threshold_percent integer DEFAULT 80 NOT NULL,
    current_size_bytes bigint DEFAULT 0 NOT NULL,
    current_artifact_count bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT project_quotas_max_artifact_count_check CHECK ((max_artifact_count >= 0)),
    CONSTRAINT project_quotas_max_size_bytes_check CHECK ((max_size_bytes >= 0)),
    CONSTRAINT project_quotas_warn_threshold_percent_check CHECK (((warn_threshold_percent >= 0) AND (warn_threshold_percent <= 100)))
);


--
-- Name: TABLE project_quotas; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.project_quotas IS 'Project storage ceiling (0 = unlimited); counters are maintained only while a limit is set';


--
-- Name: projects; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.projects (
    id uuid DEFAULT gen_random_uuid() CONSTRAINT organizations_id_not_null NOT NULL,
    name character varying(255) CONSTRAINT organizations_name_not_null NOT NULL,
    display_name character varying(255),
    description text,
    owner_id uuid,
    created_at timestamp with time zone DEFAULT now() CONSTRAINT organizations_created_at_not_null NOT NULL,
    updated_at timestamp with time zone DEFAULT now() CONSTRAINT organizations_updated_at_not_null NOT NULL,
    slug character varying(64) CONSTRAINT organizations_slug_not_null NOT NULL,
    allowed_formats text[],
    allowed_repo_types text[] DEFAULT '{hosted}'::text[] NOT NULL,
    repo_name_prefix text DEFAULT ''::text NOT NULL,
    allow_public_repos boolean DEFAULT false NOT NULL,
    max_repositories integer DEFAULT 50 NOT NULL,
    token_max_ttl_days integer DEFAULT 365 NOT NULL,
    CONSTRAINT chk_project_max_repositories CHECK ((max_repositories >= 0)),
    CONSTRAINT chk_project_repo_types CHECK ((allowed_repo_types <@ ARRAY['hosted'::text, 'proxy'::text, 'group'::text])),
    CONSTRAINT chk_project_token_max_ttl CHECK ((token_max_ttl_days >= 0))
);


--
-- Name: TABLE projects; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.projects IS 'Projects (formerly organizations): own repositories; members hold a role on them';


--
-- Name: COLUMN projects.slug; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.projects.slug IS 'URL-safe identifier derived from the project name, e.g. "my-project"';


--
-- Name: COLUMN projects.allowed_formats; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.projects.allowed_formats IS 'Formats self-service repositories may use; NULL = every format.';


--
-- Name: COLUMN projects.allowed_repo_types; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.projects.allowed_repo_types IS 'Repository types self-service repositories may use (hosted, proxy, group).';


--
-- Name: COLUMN projects.repo_name_prefix; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.projects.repo_name_prefix IS 'Self-service repository names must start with this prefix.';


--
-- Name: COLUMN projects.allow_public_repos; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.projects.allow_public_repos IS 'Whether project repositories may be made anonymously readable (admin opt-in).';


--
-- Name: COLUMN projects.max_repositories; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.projects.max_repositories IS 'Maximum number of repositories in the project; 0 = unlimited.';


--
-- Name: COLUMN projects.token_max_ttl_days; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.projects.token_max_ttl_days IS 'Maximum lifetime of project tokens in days; 0 = no cap.';


--
-- Name: promotion_pipelines; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.promotion_pipelines (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(255) NOT NULL,
    description text,
    stages jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    creator_id uuid
);


--
-- Name: TABLE promotion_pipelines; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.promotion_pipelines IS 'Named, ordered promotion stage sequences reusable across bundles';


--
-- Name: COLUMN promotion_pipelines.creator_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.promotion_pipelines.creator_id IS 'FK to auth_users.';


--
-- Name: proxy_cache_entries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.proxy_cache_entries (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    cache_key text NOT NULL,
    blob_key text,
    content_type text,
    status_code integer DEFAULT 200 NOT NULL,
    size_bytes bigint DEFAULT 0 NOT NULL,
    hit_count bigint DEFAULT 0 NOT NULL,
    last_hit_at timestamp with time zone,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE proxy_cache_entries; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.proxy_cache_entries IS 'Cache entries for proxy repositories';


--
-- Name: COLUMN proxy_cache_entries.hit_count; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.proxy_cache_entries.hit_count IS 'Cache hit counter; BIGINT to avoid overflow on high-traffic repos';


--
-- Name: quota_pending; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.quota_pending (
    id bigint NOT NULL,
    repository_id uuid NOT NULL,
    size_bytes bigint NOT NULL,
    artifact_count integer NOT NULL,
    kind smallint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE quota_pending; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.quota_pending IS 'Quota usage already in storage_quotas counters but not (yet) backed by package rows, or the reverse; see migration 061';


--
-- Name: quota_pending_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.quota_pending_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: quota_pending_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.quota_pending_id_seq OWNED BY public.quota_pending.id;


--
-- Name: recovery_dirty_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.recovery_dirty_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: recovery_dirty; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.recovery_dirty (
    table_name text NOT NULL,
    row_key text NOT NULL,
    seq bigint DEFAULT nextval('public.recovery_dirty_seq'::regclass) NOT NULL,
    marked_at timestamp with time zone DEFAULT now() NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    retry_at timestamp with time zone,
    last_error text
);


--
-- Name: TABLE recovery_dirty; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.recovery_dirty IS 'Journal work queue: rows whose sidecar must be (re)written or deleted; see migration 068';


--
-- Name: recovery_reconcile; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.recovery_reconcile (
    table_name text NOT NULL,
    stage text DEFAULT 'missing'::text NOT NULL,
    full_pass boolean DEFAULT false NOT NULL,
    cursor text,
    pass_started_at timestamp with time zone,
    last_completed_at timestamp with time zone,
    marked_total bigint DEFAULT 0 NOT NULL,
    CONSTRAINT recovery_reconcile_stage_check CHECK ((stage = ANY (ARRAY['missing'::text, 'stale'::text])))
);


--
-- Name: TABLE recovery_reconcile; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.recovery_reconcile IS 'Keyset progress of the journal reconcile passes; see migration 068';


--
-- Name: recovery_sidecars; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.recovery_sidecars (
    table_name text NOT NULL,
    row_key text NOT NULL,
    row_hash text NOT NULL,
    sidecar_sha256 text NOT NULL,
    pending_sha256 text,
    written_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE recovery_sidecars; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.recovery_sidecars IS 'Tracking of journal sidecar blobs in the blob store; sidecars have no blobs row; see migration 068';


--
-- Name: recovery_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.recovery_state (
    id boolean DEFAULT true NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    last_error text,
    last_error_at timestamp with time zone,
    last_drain_at timestamp with time zone,
    last_reconcile_at timestamp with time zone,
    CONSTRAINT recovery_state_id_check CHECK (id)
);


--
-- Name: TABLE recovery_state; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.recovery_state IS 'Singleton: journal enabled flag (set from config at startup) and worker progress; see migration 068';


--
-- Name: release_bundle_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.release_bundle_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bundle_id uuid NOT NULL,
    source_repo_id uuid NOT NULL,
    sha256 text NOT NULL,
    artifact_path text NOT NULL,
    format character varying(50) NOT NULL,
    size_bytes bigint DEFAULT 0 NOT NULL,
    added_at timestamp with time zone DEFAULT now() NOT NULL,
    artifact_ref_id uuid,
    adder_id uuid
);


--
-- Name: TABLE release_bundle_items; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.release_bundle_items IS 'Artifact references (by SHA-256) that constitute a release bundle';


--
-- Name: COLUMN release_bundle_items.adder_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.release_bundle_items.adder_id IS 'FK to auth_users.';


--
-- Name: release_bundles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.release_bundles (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(255) NOT NULL,
    version character varying(255) NOT NULL,
    description text,
    state character varying(20) DEFAULT 'draft'::character varying NOT NULL,
    pipeline_id uuid,
    current_stage character varying(255),
    sealed_at timestamp with time zone,
    archived_at timestamp with time zone,
    gpg_key_id text,
    gpg_signature text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    creator_id uuid,
    sealer_id uuid,
    archiver_id uuid,
    CONSTRAINT release_bundles_state_check CHECK (((state)::text = ANY ((ARRAY['draft'::character varying, 'sealed'::character varying, 'archived'::character varying])::text[])))
);


--
-- Name: TABLE release_bundles; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.release_bundles IS 'Versioned, named collections of artifacts moving through a promotion pipeline';


--
-- Name: COLUMN release_bundles.creator_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.release_bundles.creator_id IS 'FK to auth_users.';


--
-- Name: COLUMN release_bundles.sealer_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.release_bundles.sealer_id IS 'FK to auth_users.';


--
-- Name: COLUMN release_bundles.archiver_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.release_bundles.archiver_id IS 'FK to auth_users.';


--
-- Name: repositories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repositories (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(255) NOT NULL,
    format character varying(50) NOT NULL,
    description text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    allow_full_unpublish boolean DEFAULT false NOT NULL,
    public boolean DEFAULT false NOT NULL,
    repo_type character varying(20) DEFAULT 'hosted'::character varying NOT NULL,
    upstream_url text,
    upstream_username text,
    upstream_password_enc text,
    cache_ttl_seconds integer DEFAULT 3600 NOT NULL,
    negative_cache_ttl_seconds integer DEFAULT 60 NOT NULL,
    blocked boolean DEFAULT false NOT NULL,
    project_id uuid CONSTRAINT repositories_org_id_not_null NOT NULL,
    max_stale_seconds integer DEFAULT 604800 NOT NULL,
    presign_downloads boolean,
    deleted_at timestamp with time zone,
    deleted_by text,
    trash_retention_days integer,
    immutable_releases boolean DEFAULT false NOT NULL,
    merge_proxy_versions boolean DEFAULT false NOT NULL,
    sign_packages boolean DEFAULT false NOT NULL,
    federated boolean DEFAULT false NOT NULL,
    CONSTRAINT chk_repo_format CHECK (((format)::text = ANY ((ARRAY['rpm'::character varying, 'deb'::character varying, 'maven'::character varying, 'npm'::character varying, 'docker'::character varying, 'helm'::character varying, 'pypi'::character varying, 'nuget'::character varying, 'go'::character varying, 'cargo'::character varying, 'composer'::character varying, 'rubygems'::character varying, 'raw'::character varying, 'conda'::character varying, 'conan'::character varying, 'ivy'::character varying, 'sbt'::character varying, 'terraform'::character varying, 'apk'::character varying, 'pacman'::character varying, 'nix'::character varying, 'ansible'::character varying])::text[]))),
    CONSTRAINT chk_repo_trash_retention CHECK (((trash_retention_days IS NULL) OR (trash_retention_days >= 0))),
    CONSTRAINT chk_repo_type CHECK (((repo_type)::text = ANY ((ARRAY['hosted'::character varying, 'proxy'::character varying, 'group'::character varying])::text[])))
);


--
-- Name: TABLE repositories; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.repositories IS 'Parent table for all repository instances (deb, rpm, maven, npm, etc.)';


--
-- Name: COLUMN repositories.format; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.repositories.format IS 'Package format: rpm, deb, maven, npm, docker, helm, pypi, nuget, go, cargo, composer, rubygems, raw, conda, conan, ivy, sbt, terraform, apk, pacman, nix, ansible. Constrained by chk_repo_format.';


--
-- Name: COLUMN repositories.allow_full_unpublish; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.repositories.allow_full_unpublish IS 'Per-repo override of the 72-hour npm unpublish grace period';


--
-- Name: COLUMN repositories.public; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.repositories.public IS 'Publicly readable without authentication';


--
-- Name: COLUMN repositories.repo_type; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.repositories.repo_type IS 'Repository type: hosted (stores artifacts), proxy (caches upstream), group (aggregates multiple repos). Constrained by chk_repo_type.';


--
-- Name: COLUMN repositories.upstream_username; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.repositories.upstream_username IS 'Proxy upstream username. Stored in plaintext (not sensitive). Pair with upstream_password_enc for authentication.';


--
-- Name: COLUMN repositories.upstream_password_enc; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.repositories.upstream_password_enc IS 'AES-256-GCM encrypted proxy upstream password. Plaintext MUST NOT be stored here. Encrypt before write, decrypt after read.';


--
-- Name: COLUMN repositories.project_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.repositories.project_id IS 'Project this repository belongs to. NOT NULL; the system project means unassigned.';


--
-- Name: COLUMN repositories.presign_downloads; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.repositories.presign_downloads IS 'Per-repository override of storage.presignDownloads: NULL inherits the global setting, true/false force redirect/streaming.';


--
-- Name: COLUMN repositories.deleted_at; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.repositories.deleted_at IS 'Set when the repository was moved to the trash; the name stays reserved (globally unique) until it is purged.';


--
-- Name: COLUMN repositories.deleted_by; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.repositories.deleted_by IS 'Username of the principal that deleted the repository.';


--
-- Name: COLUMN repositories.trash_retention_days; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.repositories.trash_retention_days IS 'Days a deleted item stays restorable. NULL = server default (OPENARTIFACT_TRASH_RETENTION); 0 = delete immediately, no trash.';


--
-- Name: COLUMN repositories.immutable_releases; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.repositories.immutable_releases IS 'When true, released (non-snapshot, non-prerelease) versions cannot be deleted or overwritten without admin force.';


--
-- Name: COLUMN repositories.merge_proxy_versions; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.repositories.merge_proxy_versions IS 'Groups only. false (default): when a hosted member holds a package name, proxy members'' versions and files of that name are hidden. true: versions of all members are merged.';


--
-- Name: COLUMN repositories.sign_packages; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.repositories.sign_packages IS 'Hosted rpm repositories only: sign uploaded RPMs with the active GPG key.';


--
-- Name: repository_attestation_attach; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repository_attestation_attach (
    repository_id uuid NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: repository_dependency_track; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repository_dependency_track (
    repository_id uuid NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    base_url text DEFAULT ''::text NOT NULL,
    api_key text DEFAULT ''::text NOT NULL,
    project_name text DEFAULT ''::text NOT NULL,
    project_version text DEFAULT ''::text NOT NULL,
    auto_push boolean,
    push_vex boolean,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE repository_dependency_track; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.repository_dependency_track IS 'Per-repository Dependency-Track overrides; empty/NULL fields inherit the global settings. api_key is encrypted.';


--
-- Name: repository_group_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repository_group_members (
    group_repo_id uuid NOT NULL,
    member_repo_id uuid NOT NULL,
    priority integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: repository_metadata; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repository_metadata (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    path text NOT NULL,
    blob_sha256 text NOT NULL,
    blob_size bigint NOT NULL,
    last_generated timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: rpm_repos; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.rpm_repos (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    base_arch text NOT NULL,
    metadata_stale boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: sbom_results; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sbom_results (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    scan_job_id uuid NOT NULL,
    sbom_format character varying(20) NOT NULL,
    spec_version character varying(10) NOT NULL,
    blob_id uuid,
    component_count integer DEFAULT 0 NOT NULL
);


--
-- Name: TABLE sbom_results; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.sbom_results IS 'SBOM report payloads. FK to scan_jobs. Content in blobs (not inline).';


--
-- Name: scan_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.scan_jobs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    artifact_ref_id uuid NOT NULL,
    scan_type character varying(20) NOT NULL,
    scanner character varying(50),
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    error_message text,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT scan_jobs_scan_type_check CHECK (((scan_type)::text = ANY ((ARRAY['vuln'::character varying, 'license'::character varying, 'secret'::character varying, 'sbom'::character varying, 'ai'::character varying])::text[]))),
    CONSTRAINT scan_jobs_status_check CHECK (((status)::text = ANY ((ARRAY['pending'::character varying, 'running'::character varying, 'completed'::character varying, 'failed'::character varying, 'partial'::character varying])::text[])))
);


--
-- Name: TABLE scan_jobs; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.scan_jobs IS 'Parent table for all scan work (vuln, license, secret, sbom, ai). Result payloads are in: vuln_results, license_results, secret_results, sbom_results, ai_results.';


--
-- Name: COLUMN scan_jobs.scanner; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.scan_jobs.scanner IS 'Scanner implementation: osv, trivy, grype, internal, etc. NULL = default for type.';


--
-- Name: secret_results; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.secret_results (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    scan_job_id uuid NOT NULL,
    findings jsonb DEFAULT '[]'::jsonb NOT NULL,
    finding_count integer DEFAULT 0 NOT NULL,
    quarantined boolean DEFAULT false NOT NULL
);


--
-- Name: TABLE secret_results; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.secret_results IS 'Secret detection payloads. FK to scan_jobs.';


--
-- Name: vex_statements; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.vex_statements (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    artifact_id text NOT NULL,
    vuln_id text NOT NULL,
    status character varying(24) NOT NULL,
    justification character varying(64) DEFAULT ''::character varying NOT NULL,
    impact_statement text DEFAULT ''::text NOT NULL,
    action_statement text DEFAULT ''::text NOT NULL,
    created_by text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT vex_statements_justification_check CHECK (((justification)::text = ANY ((ARRAY[''::character varying, 'component_not_present'::character varying, 'vulnerable_code_not_present'::character varying, 'vulnerable_code_not_in_execute_path'::character varying, 'vulnerable_code_cannot_be_controlled_by_adversary'::character varying, 'inline_mitigations_already_exist'::character varying])::text[]))),
    CONSTRAINT vex_statements_not_affected_justified CHECK ((((status)::text <> 'not_affected'::text) OR ((justification)::text <> ''::text))),
    CONSTRAINT vex_statements_status_check CHECK (((status)::text = ANY ((ARRAY['affected'::character varying, 'not_affected'::character varying, 'fixed'::character varying, 'under_investigation'::character varying])::text[])))
);


--
-- Name: TABLE vex_statements; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.vex_statements IS 'Admin VEX decisions per (repository, artifact, vulnerability). justification is the OpenVEX enum.';


--
-- Name: vuln_results; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.vuln_results (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    scan_job_id uuid NOT NULL,
    vulnerabilities jsonb DEFAULT '[]'::jsonb NOT NULL,
    vuln_count integer DEFAULT 0 NOT NULL,
    critical_count integer DEFAULT 0 NOT NULL,
    high_count integer DEFAULT 0 NOT NULL,
    medium_count integer DEFAULT 0 NOT NULL,
    low_count integer DEFAULT 0 NOT NULL,
    quarantined boolean DEFAULT false NOT NULL,
    package_name text,
    package_version text,
    ecosystem text,
    failed_scanners text[] DEFAULT '{}'::text[] NOT NULL,
    kev_count integer DEFAULT 0 NOT NULL,
    max_epss double precision
);


--
-- Name: TABLE vuln_results; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.vuln_results IS 'Vulnerability scan payloads. FK to scan_jobs.';


--
-- Name: COLUMN vuln_results.failed_scanners; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.vuln_results.failed_scanners IS 'Scanners that did not produce a result (status failed or partial).';


--
-- Name: COLUMN vuln_results.kev_count; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.vuln_results.kev_count IS 'Findings whose CVE is in the KEV catalog; recomputed after every feed import.';


--
-- Name: COLUMN vuln_results.max_epss; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.vuln_results.max_epss IS 'Highest EPSS score among the findings; NULL when none has one.';


--
-- Name: security_results; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.security_results AS
SELECT u.kind, u.id, u.repository_id, u.artifact_id, u.package_name::text AS package_name,
       u.package_version::text AS package_version, u.scanner,
       u.scan_status, u.critical, u.high, u.medium, u.low, u.quarantined, u.suppressed, u.vex_rank,
       u.is_finding, u.scanned_at,
       CASE WHEN u.critical > 0 THEN 4 WHEN u.high > 0 THEN 3 WHEN u.medium > 0 THEN 2 WHEN u.low > 0 THEN 1 ELSE 0 END AS severity_rank,
       CASE WHEN u.quarantined THEN 'quarantined' WHEN u.suppressed THEN 'suppressed' ELSE 'open' END AS status,
       u.kev_count, u.max_epss
FROM (
    SELECT 'vuln'::text AS kind, vr.id, ref.repository_id, ref.artifact_path AS artifact_id,
           COALESCE(NULLIF(vr.package_name, ''), substring(ref.artifact_path FROM '^(.+)@[^@]*$'), ref.artifact_path) AS package_name,
           COALESCE(NULLIF(vr.package_version, ''), substring(ref.artifact_path FROM '^.+@([^@]*)$'), '') AS package_version,
           COALESCE(sj.scanner, '')::text AS scanner, sj.status::text AS scan_status,
           vr.critical_count AS critical, vr.high_count AS high, vr.medium_count AS medium, vr.low_count AS low,
           vr.quarantined,
           (vr.vuln_count > 0 AND jsonb_array_length(vr.vulnerabilities) > 0
               AND COALESCE(vx.resolved, 0) >= jsonb_array_length(vr.vulnerabilities)) AS suppressed,
           vx.worst AS vex_rank,
           (vr.vuln_count > 0) AS is_finding,
           COALESCE(sj.completed_at, sj.updated_at) AS scanned_at,
           vr.kev_count, vr.max_epss
    FROM vuln_results vr
    JOIN scan_jobs sj ON sj.id = vr.scan_job_id
    JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
    LEFT JOIN LATERAL (
        SELECT count(*) FILTER (WHERE vs.status IN ('not_affected', 'fixed'))::int AS resolved,
               min(CASE vs.status WHEN 'affected' THEN 1 WHEN 'under_investigation' THEN 2 WHEN 'fixed' THEN 3 ELSE 4 END)::int AS worst
        FROM jsonb_array_elements(vr.vulnerabilities) AS v
        JOIN vex_statements vs ON vs.repository_id = ref.repository_id AND vs.artifact_id = ref.artifact_path
                              AND upper(vs.vuln_id) = upper(v ->> 'id')
        WHERE vr.vuln_count > 0
          AND EXISTS (SELECT 1 FROM vex_statements x WHERE x.repository_id = ref.repository_id AND x.artifact_id = ref.artifact_path)
    ) vx ON true
    WHERE jsonb_typeof(vr.vulnerabilities) = 'array'
      AND sj.id = (SELECT s2.id FROM scan_jobs s2
                   WHERE s2.artifact_ref_id = sj.artifact_ref_id AND s2.scan_type = 'vuln'
                   ORDER BY s2.created_at DESC, s2.id LIMIT 1)

    UNION ALL

    SELECT 'license'::text, lr.id, ref.repository_id, ref.artifact_path,
           COALESCE(substring(ref.artifact_path FROM '^(.+)@[^@]*$'), ref.artifact_path),
           COALESCE(substring(ref.artifact_path FROM '^.+@([^@]*)$'), ''),
           ''::text, sj.status::text,
           0, CASE WHEN lr.quarantined THEN 1 ELSE 0 END, 0, cardinality(lr.unresolved_licenses),
           lr.quarantined,
           (NOT lr.quarantined AND lr.quarantine_approved),
           NULL::int,
           (lr.quarantined OR lr.quarantine_approved OR cardinality(lr.unresolved_licenses) > 0),
           COALESCE(sj.completed_at, sj.updated_at),
           0, NULL::double precision
    FROM license_results lr
    JOIN scan_jobs sj ON sj.id = lr.scan_job_id
    JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id

    UNION ALL

    SELECT 'secret'::text, sr.id, ref.repository_id, ref.artifact_path,
           COALESCE(substring(ref.artifact_path FROM '^(.+)@[^@]*$'), ref.artifact_path),
           COALESCE(substring(ref.artifact_path FROM '^.+@([^@]*)$'), ''),
           ''::text, sj.status::text,
           COALESCE(sv.critical, 0), COALESCE(sv.high, 0), COALESCE(sv.medium, 0), COALESCE(sv.total - sv.critical - sv.high - sv.medium, 0),
           sr.quarantined,
           false,
           NULL::int,
           (sr.finding_count > 0 OR sr.quarantined),
           COALESCE(sj.completed_at, sj.updated_at),
           0, NULL::double precision
    FROM secret_results sr
    JOIN scan_jobs sj ON sj.id = sr.scan_job_id
    JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
    LEFT JOIN LATERAL (
        SELECT count(*)::int AS total,
               count(*) FILTER (WHERE lower(f ->> 'severity') = 'critical')::int AS critical,
               count(*) FILTER (WHERE lower(f ->> 'severity') = 'high')::int AS high,
               count(*) FILTER (WHERE lower(f ->> 'severity') = 'medium')::int AS medium
        FROM jsonb_array_elements(sr.findings) AS f
        WHERE sr.finding_count > 0
    ) sv ON true
    WHERE jsonb_typeof(sr.findings) = 'array'

    UNION ALL

    SELECT 'ai'::text, air.id, ref.repository_id, ref.artifact_path,
           COALESCE(NULLIF(air.artifact_name, ''), substring(ref.artifact_path FROM '^(.+)@[^@]*$'), ref.artifact_path),
           COALESCE(NULLIF(air.artifact_version, ''), substring(ref.artifact_path FROM '^.+@([^@]*)$'), ''),
           ''::text, sj.status::text,
           0, CASE WHEN air.quarantine_recommended THEN 1 ELSE 0 END, CASE WHEN air.license_status = 'REVIEW_REQUIRED' THEN 1 ELSE 0 END, 0,
           false,
           false,
           NULL::int,
           (air.quarantine_recommended OR air.license_status = 'REVIEW_REQUIRED'),
           COALESCE(sj.completed_at, sj.updated_at),
           0, NULL::double precision
    FROM ai_results air
    JOIN scan_jobs sj ON sj.id = air.scan_job_id
    JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
) u;


--
-- Name: VIEW security_results; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.security_results IS 'Every vuln/license/secret/AI result in one shape (counts, quarantine, VEX state, KEV and EPSS aggregates); is_finding marks the ones that report something. Read by the security findings list, summary and per-package summary.';


--
-- Name: storage_backends; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.storage_backends (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(128) NOT NULL,
    kind character varying(32) NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    status character varying(20) DEFAULT 'unknown'::character varying NOT NULL,
    status_message text,
    last_checked_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_storage_backend_kind CHECK (((kind)::text = ANY ((ARRAY['filesystem'::character varying, 's3'::character varying, 'garage'::character varying, 'seaweedfs'::character varying, 'oci'::character varying, 'gcs'::character varying, 'azure'::character varying])::text[]))),
    CONSTRAINT chk_storage_backend_status CHECK (((status)::text = ANY ((ARRAY['unknown'::character varying, 'healthy'::character varying, 'degraded'::character varying, 'unreachable'::character varying])::text[])))
);


--
-- Name: TABLE storage_backends; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.storage_backends IS 'Configured storage backends for artifact persistence';


--
-- Name: storage_policies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.storage_policies (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(128) NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    target_tier_id uuid NOT NULL,
    from_tier_id uuid,
    min_age_seconds bigint,
    min_idle_seconds bigint,
    min_size_bytes bigint,
    max_size_bytes bigint,
    repo_glob text,
    formats text[] DEFAULT '{}'::text[] NOT NULL,
    path_glob text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_storage_policy_bounds CHECK ((((min_age_seconds IS NULL) OR (min_age_seconds >= 0)) AND ((min_idle_seconds IS NULL) OR (min_idle_seconds >= 0)) AND ((min_size_bytes IS NULL) OR (min_size_bytes >= 0)) AND ((max_size_bytes IS NULL) OR (max_size_bytes >= 0)) AND ((min_size_bytes IS NULL) OR (max_size_bytes IS NULL) OR (min_size_bytes <= max_size_bytes))))
);


--
-- Name: TABLE storage_policies; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.storage_policies IS 'Ordered lifecycle policies: the first enabled policy whose conditions match a blob names the tier it belongs on.';


--
-- Name: storage_quotas; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.storage_quotas (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    max_size_bytes bigint DEFAULT 0 NOT NULL,
    max_artifact_count bigint DEFAULT 0 NOT NULL,
    warn_threshold_percent integer DEFAULT 80 NOT NULL,
    current_size_bytes bigint DEFAULT 0 NOT NULL,
    current_artifact_count bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE storage_quotas; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.storage_quotas IS 'Storage quotas and usage tracking per repository';


--
-- Name: storage_tier_moves; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.storage_tier_moves (
    sha256 character varying(64) NOT NULL,
    from_backend_id uuid,
    to_backend_id uuid,
    state character varying(16) NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    size_bytes bigint DEFAULT 0 NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    last_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_storage_tier_move_distinct CHECK ((from_backend_id IS DISTINCT FROM to_backend_id)),
    CONSTRAINT chk_storage_tier_move_state CHECK (((state)::text = ANY ((ARRAY['copying'::character varying, 'flipped'::character varying])::text[])))
);


--
-- Name: TABLE storage_tier_moves; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.storage_tier_moves IS 'Crash-recovery journal of the storage tier mover: one row per blob move that has started and not yet finished cleanup.';


--
-- Name: storage_tiers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.storage_tiers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(64) NOT NULL,
    backend_id uuid,
    rank integer DEFAULT 0 NOT NULL,
    promote_on_read boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_storage_tier_promote CHECK (((backend_id IS NOT NULL) OR (NOT promote_on_read)))
);


--
-- Name: TABLE storage_tiers; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.storage_tiers IS 'Named storage tiers. backend_id NULL is the primary (hot) store from the server configuration.';


--
-- Name: system_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.system_settings (
    key character varying(255) NOT NULL,
    value text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: webhook_deliveries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.webhook_deliveries (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    webhook_id uuid NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    response_status integer,
    response_body text,
    attempt_count integer DEFAULT 0 NOT NULL,
    next_retry_at timestamp with time zone,
    delivered_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_webhook_delivery_status CHECK (((status)::text = ANY ((ARRAY['pending'::character varying, 'success'::character varying, 'failed'::character varying])::text[])))
);


--
-- Name: TABLE webhook_deliveries; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.webhook_deliveries IS 'Webhook delivery attempts and history';


--
-- Name: webhooks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.webhooks (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid,
    name character varying(255) NOT NULL,
    url text NOT NULL,
    secret_enc text,
    enabled boolean DEFAULT true NOT NULL,
    events text[] DEFAULT '{}'::text[] NOT NULL,
    content_type character varying(50) DEFAULT 'application/json'::character varying NOT NULL,
    ssl_verify boolean DEFAULT true NOT NULL,
    max_retries integer DEFAULT 3 NOT NULL,
    timeout_seconds integer DEFAULT 30 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    payload_format text DEFAULT 'openartifact'::text NOT NULL,
    CONSTRAINT webhooks_payload_format_check CHECK ((payload_format = ANY (ARRAY['openartifact'::text, 'cloudevents'::text, 'slack'::text, 'teams'::text, 'discord'::text])))
);


--
-- Name: TABLE webhooks; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.webhooks IS 'Webhook subscriptions for artifact lifecycle events';


--
-- Name: COLUMN webhooks.secret_enc; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.webhooks.secret_enc IS 'AES-256-GCM encrypted HMAC-SHA256 signing secret. Plaintext MUST NOT be stored here. Encrypt before write, decrypt after read.';


--
-- Name: COLUMN webhooks.payload_format; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.webhooks.payload_format IS 'Delivery body format: openartifact, cloudevents, slack, teams or discord.';


--
-- Name: worker_requests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.worker_requests (
    kind character varying(64) NOT NULL,
    key text NOT NULL,
    payload jsonb,
    requested_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    claimed_until timestamp with time zone
);


--
-- Name: TABLE worker_requests; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.worker_requests IS 'Work a web process asks a worker process to do. One row per (kind, key); a repeated request refreshes requested_at. Rows are deleted once the work they asked for finished.';


--
-- Name: COLUMN worker_requests.kind; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.worker_requests.kind IS 'What to do, e.g. regen:npm, regen:maven, regen:group, ai:analyze, vuln:scan, dtrack:push.';


--
-- Name: COLUMN worker_requests.key; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.worker_requests.key IS 'What to do it to: a repository or package id, or a hash of the payload when the subject has several parts.';


--
-- Name: COLUMN worker_requests.payload; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.worker_requests.payload IS 'The subject of kinds whose key is only a hash; null otherwise.';


--
-- Name: COLUMN worker_requests.requested_at; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.worker_requests.requested_at IS 'When the work was last requested; a worker deletes only the requests it has seen, so one made while it worked survives.';


--
-- Name: COLUMN worker_requests.claimed_until; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.worker_requests.claimed_until IS 'While in the future, a worker holds the request; a worker that died leaves it to expire and another takes it over. Null: unclaimed.';


--
-- Name: quota_pending id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.quota_pending ALTER COLUMN id SET DEFAULT nextval('public.quota_pending_id_seq'::regclass);


--
-- Name: ai_results ai_results_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ai_results
    ADD CONSTRAINT ai_results_pkey PRIMARY KEY (id);


--
-- Name: ai_results ai_results_scan_job_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ai_results
    ADD CONSTRAINT ai_results_scan_job_id_key UNIQUE (scan_job_id);


--
-- Name: apk_signing_keys apk_signing_keys_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.apk_signing_keys
    ADD CONSTRAINT apk_signing_keys_pkey PRIMARY KEY (repository_id);


--
-- Name: artifact_properties artifact_properties_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.artifact_properties
    ADD CONSTRAINT artifact_properties_pkey PRIMARY KEY (id);


--
-- Name: artifact_refs artifact_refs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.artifact_refs
    ADD CONSTRAINT artifact_refs_pkey PRIMARY KEY (id);


--
-- Name: artifact_refs artifact_refs_repository_id_artifact_path_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.artifact_refs
    ADD CONSTRAINT artifact_refs_repository_id_artifact_path_key UNIQUE (repository_id, artifact_path);


--
-- Name: audit_logs audit_logs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_logs
    ADD CONSTRAINT audit_logs_pkey PRIMARY KEY (id);


--
-- Name: auth_groups auth_groups_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_groups
    ADD CONSTRAINT auth_groups_pkey PRIMARY KEY (id);


--
-- Name: auth_groups auth_groups_project_name_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_groups
    ADD CONSTRAINT auth_groups_project_name_unique UNIQUE (project_id, name);


--
-- Name: auth_saml_assertions auth_saml_assertions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_saml_assertions
    ADD CONSTRAINT auth_saml_assertions_pkey PRIMARY KEY (provider_slug, assertion_id);


--
-- Name: auth_session_sso auth_session_sso_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_session_sso
    ADD CONSTRAINT auth_session_sso_pkey PRIMARY KEY (session_id);


--
-- Name: auth_sessions auth_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_sessions
    ADD CONSTRAINT auth_sessions_pkey PRIMARY KEY (id);


--
-- Name: auth_sessions auth_sessions_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_sessions
    ADD CONSTRAINT auth_sessions_token_hash_key UNIQUE (token_hash);


--
-- Name: auth_sso_providers auth_sso_providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_sso_providers
    ADD CONSTRAINT auth_sso_providers_pkey PRIMARY KEY (id);


--
-- Name: auth_sso_providers auth_sso_providers_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_sso_providers
    ADD CONSTRAINT auth_sso_providers_slug_key UNIQUE (slug);


--
-- Name: auth_tokens auth_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_tokens
    ADD CONSTRAINT auth_tokens_pkey PRIMARY KEY (id);


--
-- Name: auth_tokens auth_tokens_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_tokens
    ADD CONSTRAINT auth_tokens_token_hash_key UNIQUE (token_hash);


--
-- Name: auth_user_groups auth_user_groups_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_user_groups
    ADD CONSTRAINT auth_user_groups_pkey PRIMARY KEY (user_id, group_id);


--
-- Name: auth_users auth_users_email_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_users
    ADD CONSTRAINT auth_users_email_key UNIQUE (email);


--
-- Name: auth_users auth_users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_users
    ADD CONSTRAINT auth_users_pkey PRIMARY KEY (id);


--
-- Name: auth_users auth_users_username_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_users
    ADD CONSTRAINT auth_users_username_key UNIQUE (username);


--
-- Name: backup_jobs backup_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.backup_jobs
    ADD CONSTRAINT backup_jobs_pkey PRIMARY KEY (id);


--
-- Name: backup_leases backup_leases_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.backup_leases
    ADD CONSTRAINT backup_leases_pkey PRIMARY KEY (id);


--
-- Name: blob_deletions blob_deletions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.blob_deletions
    ADD CONSTRAINT blob_deletions_pkey PRIMARY KEY (sha256, scope);


--
-- Name: blob_replication blob_replication_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.blob_replication
    ADD CONSTRAINT blob_replication_pkey PRIMARY KEY (sha256);


--
-- Name: blob_replication_state blob_replication_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.blob_replication_state
    ADD CONSTRAINT blob_replication_state_pkey PRIMARY KEY (id);


--
-- Name: blobs blobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.blobs
    ADD CONSTRAINT blobs_pkey PRIMARY KEY (id);


--
-- Name: blobs blobs_sha256_hex; Type: CHECK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE public.blobs
    ADD CONSTRAINT blobs_sha256_hex CHECK (((sha256)::text ~ '^[0-9a-f]{64}$'::text)) NOT VALID;


--
-- Name: blobs blobs_sha256_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.blobs
    ADD CONSTRAINT blobs_sha256_key UNIQUE (sha256);


--
-- Name: bundle_promotions bundle_promotions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bundle_promotions
    ADD CONSTRAINT bundle_promotions_pkey PRIMARY KEY (id);


--
-- Name: cleanup_policies cleanup_policies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cleanup_policies
    ADD CONSTRAINT cleanup_policies_pkey PRIMARY KEY (id);


--
-- Name: debian_architectures debian_architectures_distribution_id_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.debian_architectures
    ADD CONSTRAINT debian_architectures_distribution_id_name_key UNIQUE (distribution_id, name);


--
-- Name: debian_architectures debian_architectures_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.debian_architectures
    ADD CONSTRAINT debian_architectures_pkey PRIMARY KEY (id);


--
-- Name: debian_components debian_components_distribution_id_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.debian_components
    ADD CONSTRAINT debian_components_distribution_id_name_key UNIQUE (distribution_id, name);


--
-- Name: debian_components debian_components_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.debian_components
    ADD CONSTRAINT debian_components_pkey PRIMARY KEY (id);


--
-- Name: debian_distributions debian_distributions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.debian_distributions
    ADD CONSTRAINT debian_distributions_pkey PRIMARY KEY (id);


--
-- Name: debian_distributions debian_distributions_repository_id_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.debian_distributions
    ADD CONSTRAINT debian_distributions_repository_id_name_key UNIQUE (repository_id, name);


--
-- Name: declarative_objects declarative_objects_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.declarative_objects
    ADD CONSTRAINT declarative_objects_pkey PRIMARY KEY (kind, name);


--
-- Name: dependency_track_pushes dependency_track_pushes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dependency_track_pushes
    ADD CONSTRAINT dependency_track_pushes_pkey PRIMARY KEY (repository_id, artifact_id);


--
-- Name: docker_blob_images docker_blob_images_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.docker_blob_images
    ADD CONSTRAINT docker_blob_images_pkey PRIMARY KEY (repository_id, image, digest);


--
-- Name: docker_blobs docker_blobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.docker_blobs
    ADD CONSTRAINT docker_blobs_pkey PRIMARY KEY (id);


--
-- Name: docker_blobs docker_blobs_repository_id_digest_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.docker_blobs
    ADD CONSTRAINT docker_blobs_repository_id_digest_key UNIQUE (repository_id, digest);


--
-- Name: docker_manifest_refs docker_manifest_refs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.docker_manifest_refs
    ADD CONSTRAINT docker_manifest_refs_pkey PRIMARY KEY (manifest_id, kind, digest);


--
-- Name: docker_manifests docker_manifests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.docker_manifests
    ADD CONSTRAINT docker_manifests_pkey PRIMARY KEY (id);


--
-- Name: docker_tags docker_tags_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.docker_tags
    ADD CONSTRAINT docker_tags_pkey PRIMARY KEY (id);


--
-- Name: docker_uploads docker_uploads_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.docker_uploads
    ADD CONSTRAINT docker_uploads_pkey PRIMARY KEY (id);


--
-- Name: docker_uploads docker_uploads_upload_uuid_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.docker_uploads
    ADD CONSTRAINT docker_uploads_upload_uuid_key UNIQUE (upload_uuid);


--
-- Name: epss_scores epss_scores_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.epss_scores
    ADD CONSTRAINT epss_scores_pkey PRIMARY KEY (cve_id);


--
-- Name: exploit_feeds exploit_feeds_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.exploit_feeds
    ADD CONSTRAINT exploit_feeds_pkey PRIMARY KEY (name);


--
-- Name: federation_deferred federation_deferred_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.federation_deferred
    ADD CONSTRAINT federation_deferred_pkey PRIMARY KEY (peer_id, unit_key);


--
-- Name: federation_instance federation_instance_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.federation_instance
    ADD CONSTRAINT federation_instance_pkey PRIMARY KEY (singleton);


--
-- Name: federation_peer_tokens federation_peer_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.federation_peer_tokens
    ADD CONSTRAINT federation_peer_tokens_pkey PRIMARY KEY (id);


--
-- Name: federation_peer_tokens federation_peer_tokens_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.federation_peer_tokens
    ADD CONSTRAINT federation_peer_tokens_token_hash_key UNIQUE (token_hash);


--
-- Name: federation_peers federation_peers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.federation_peers
    ADD CONSTRAINT federation_peers_pkey PRIMARY KEY (id);


--
-- Name: federation_peers federation_peers_repo_id_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.federation_peers
    ADD CONSTRAINT federation_peers_repo_id_name_key UNIQUE (repo_id, name);


--
-- Name: federation_repos federation_repos_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.federation_repos
    ADD CONSTRAINT federation_repos_pkey PRIMARY KEY (repo_id);


--
-- Name: federation_units federation_units_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.federation_units
    ADD CONSTRAINT federation_units_pkey PRIMARY KEY (repo_id, unit_key);


--
-- Name: gpg_key_repositories gpg_key_repositories_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gpg_key_repositories
    ADD CONSTRAINT gpg_key_repositories_pkey PRIMARY KEY (repository_id);


--
-- Name: gpg_keys gpg_keys_fingerprint_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gpg_keys
    ADD CONSTRAINT gpg_keys_fingerprint_key UNIQUE (fingerprint);


--
-- Name: gpg_keys gpg_keys_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gpg_keys
    ADD CONSTRAINT gpg_keys_name_key UNIQUE (name);


--
-- Name: gpg_keys gpg_keys_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gpg_keys
    ADD CONSTRAINT gpg_keys_pkey PRIMARY KEY (id);


--
-- Name: group_metadata_state group_metadata_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_metadata_state
    ADD CONSTRAINT group_metadata_state_pkey PRIMARY KEY (repository_id);


--
-- Name: group_package_owners group_package_owners_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_package_owners
    ADD CONSTRAINT group_package_owners_pkey PRIMARY KEY (group_repo_id, path);


--
-- Name: kev_entries kev_entries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.kev_entries
    ADD CONSTRAINT kev_entries_pkey PRIMARY KEY (cve_id);


--
-- Name: license_policies license_policies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.license_policies
    ADD CONSTRAINT license_policies_pkey PRIMARY KEY (id);


--
-- Name: license_results license_results_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.license_results
    ADD CONSTRAINT license_results_pkey PRIMARY KEY (id);


--
-- Name: license_results license_results_scan_job_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.license_results
    ADD CONSTRAINT license_results_scan_job_id_key UNIQUE (scan_job_id);


--
-- Name: nix_pending_nars nix_pending_nars_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nix_pending_nars
    ADD CONSTRAINT nix_pending_nars_pkey PRIMARY KEY (repository_id, sha256);


--
-- Name: nix_signing_keys nix_signing_keys_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nix_signing_keys
    ADD CONSTRAINT nix_signing_keys_pkey PRIMARY KEY (repository_id);


--
-- Name: osv_advisories osv_advisories_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.osv_advisories
    ADD CONSTRAINT osv_advisories_pkey PRIMARY KEY (id);


--
-- Name: osv_advisory_aliases osv_advisory_aliases_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.osv_advisory_aliases
    ADD CONSTRAINT osv_advisory_aliases_pkey PRIMARY KEY (advisory_id, alias);


--
-- Name: osv_affected osv_affected_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.osv_affected
    ADD CONSTRAINT osv_affected_pkey PRIMARY KEY (id);


--
-- Name: osv_mirror_state osv_mirror_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.osv_mirror_state
    ADD CONSTRAINT osv_mirror_state_pkey PRIMARY KEY (ecosystem);


--
-- Name: package_deltas package_deltas_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.package_deltas
    ADD CONSTRAINT package_deltas_pkey PRIMARY KEY (id);


--
-- Name: permission_assignments permission_assignments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.permission_assignments
    ADD CONSTRAINT permission_assignments_pkey PRIMARY KEY (id);


--
-- Name: permission_targets permission_targets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.permission_targets
    ADD CONSTRAINT permission_targets_pkey PRIMARY KEY (id);


--
-- Name: permission_targets permission_targets_project_name_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.permission_targets
    ADD CONSTRAINT permission_targets_project_name_unique UNIQUE (project_id, name);


--
-- Name: presigned_uploads presigned_uploads_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.presigned_uploads
    ADD CONSTRAINT presigned_uploads_pkey PRIMARY KEY (id);


--
-- Name: project_groups project_groups_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_groups
    ADD CONSTRAINT project_groups_pkey PRIMARY KEY (project_id, group_id);


--
-- Name: project_members project_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_members
    ADD CONSTRAINT project_members_pkey PRIMARY KEY (project_id, user_id);


--
-- Name: project_quotas project_quotas_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_quotas
    ADD CONSTRAINT project_quotas_pkey PRIMARY KEY (project_id);


--
-- Name: projects projects_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_name_key UNIQUE (name);


--
-- Name: projects projects_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_pkey PRIMARY KEY (id);


--
-- Name: projects projects_slug_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_slug_unique UNIQUE (slug);


--
-- Name: promotion_pipelines promotion_pipelines_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_pipelines
    ADD CONSTRAINT promotion_pipelines_name_key UNIQUE (name);


--
-- Name: promotion_pipelines promotion_pipelines_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_pipelines
    ADD CONSTRAINT promotion_pipelines_pkey PRIMARY KEY (id);


--
-- Name: proxy_cache_entries proxy_cache_entries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proxy_cache_entries
    ADD CONSTRAINT proxy_cache_entries_pkey PRIMARY KEY (id);


--
-- Name: proxy_cache_entries proxy_cache_entries_repo_id_cache_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proxy_cache_entries
    ADD CONSTRAINT proxy_cache_entries_repo_id_cache_key_key UNIQUE (repository_id, cache_key);


--
-- Name: quota_pending quota_pending_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.quota_pending
    ADD CONSTRAINT quota_pending_pkey PRIMARY KEY (id);


--
-- Name: recovery_dirty recovery_dirty_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recovery_dirty
    ADD CONSTRAINT recovery_dirty_pkey PRIMARY KEY (table_name, row_key);


--
-- Name: recovery_reconcile recovery_reconcile_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recovery_reconcile
    ADD CONSTRAINT recovery_reconcile_pkey PRIMARY KEY (table_name);


--
-- Name: recovery_sidecars recovery_sidecars_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recovery_sidecars
    ADD CONSTRAINT recovery_sidecars_pkey PRIMARY KEY (table_name, row_key);


--
-- Name: recovery_state recovery_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recovery_state
    ADD CONSTRAINT recovery_state_pkey PRIMARY KEY (id);


--
-- Name: release_bundle_items release_bundle_items_bundle_id_sha256_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.release_bundle_items
    ADD CONSTRAINT release_bundle_items_bundle_id_sha256_key UNIQUE (bundle_id, sha256);


--
-- Name: release_bundle_items release_bundle_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.release_bundle_items
    ADD CONSTRAINT release_bundle_items_pkey PRIMARY KEY (id);


--
-- Name: release_bundles release_bundles_name_version_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.release_bundles
    ADD CONSTRAINT release_bundles_name_version_key UNIQUE (name, version);


--
-- Name: release_bundles release_bundles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.release_bundles
    ADD CONSTRAINT release_bundles_pkey PRIMARY KEY (id);


--
-- Name: repository_metadata repo_metadata_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_metadata
    ADD CONSTRAINT repo_metadata_pkey PRIMARY KEY (id);


--
-- Name: repository_metadata repo_metadata_repository_id_path_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_metadata
    ADD CONSTRAINT repo_metadata_repository_id_path_key UNIQUE (repository_id, path);


--
-- Name: repositories repositories_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repositories
    ADD CONSTRAINT repositories_pkey PRIMARY KEY (id);


--
-- Name: repositories repositories_project_name_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repositories
    ADD CONSTRAINT repositories_project_name_unique UNIQUE (project_id, name);


--
-- Name: repository_attestation_attach repository_attestation_attach_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_attestation_attach
    ADD CONSTRAINT repository_attestation_attach_pkey PRIMARY KEY (repository_id);


--
-- Name: repository_dependency_track repository_dependency_track_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_dependency_track
    ADD CONSTRAINT repository_dependency_track_pkey PRIMARY KEY (repository_id);


--
-- Name: repository_group_members repository_group_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_group_members
    ADD CONSTRAINT repository_group_members_pkey PRIMARY KEY (group_repo_id, member_repo_id);


--
-- Name: rpm_repos rpm_repos_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.rpm_repos
    ADD CONSTRAINT rpm_repos_pkey PRIMARY KEY (id);


--
-- Name: rpm_repos rpm_repos_repository_id_base_arch_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.rpm_repos
    ADD CONSTRAINT rpm_repos_repository_id_base_arch_key UNIQUE (repository_id, base_arch);


--
-- Name: sbom_results sbom_results_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sbom_results
    ADD CONSTRAINT sbom_results_pkey PRIMARY KEY (id);


--
-- Name: sbom_results sbom_results_scan_job_id_sbom_format_spec_version_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sbom_results
    ADD CONSTRAINT sbom_results_scan_job_id_sbom_format_spec_version_key UNIQUE (scan_job_id, sbom_format, spec_version);


--
-- Name: scan_jobs scan_jobs_artifact_ref_id_scan_type_scanner_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scan_jobs
    ADD CONSTRAINT scan_jobs_artifact_ref_id_scan_type_scanner_key UNIQUE (artifact_ref_id, scan_type, scanner);


--
-- Name: scan_jobs scan_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scan_jobs
    ADD CONSTRAINT scan_jobs_pkey PRIMARY KEY (id);


--
-- Name: secret_results secret_results_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.secret_results
    ADD CONSTRAINT secret_results_pkey PRIMARY KEY (id);


--
-- Name: secret_results secret_results_scan_job_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.secret_results
    ADD CONSTRAINT secret_results_scan_job_id_key UNIQUE (scan_job_id);


--
-- Name: storage_backends storage_backends_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_backends
    ADD CONSTRAINT storage_backends_name_key UNIQUE (name);


--
-- Name: storage_backends storage_backends_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_backends
    ADD CONSTRAINT storage_backends_pkey PRIMARY KEY (id);


--
-- Name: storage_policies storage_policies_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_policies
    ADD CONSTRAINT storage_policies_name_key UNIQUE (name);


--
-- Name: storage_policies storage_policies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_policies
    ADD CONSTRAINT storage_policies_pkey PRIMARY KEY (id);


--
-- Name: storage_quotas storage_quotas_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_quotas
    ADD CONSTRAINT storage_quotas_pkey PRIMARY KEY (id);


--
-- Name: storage_quotas storage_quotas_repo_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_quotas
    ADD CONSTRAINT storage_quotas_repo_id_key UNIQUE (repository_id);


--
-- Name: storage_tier_moves storage_tier_moves_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_tier_moves
    ADD CONSTRAINT storage_tier_moves_pkey PRIMARY KEY (sha256);


--
-- Name: storage_tiers storage_tiers_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_tiers
    ADD CONSTRAINT storage_tiers_name_key UNIQUE (name);


--
-- Name: storage_tiers storage_tiers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_tiers
    ADD CONSTRAINT storage_tiers_pkey PRIMARY KEY (id);


--
-- Name: system_settings system_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.system_settings
    ADD CONSTRAINT system_settings_pkey PRIMARY KEY (key);


--
-- Name: vex_statements vex_statements_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.vex_statements
    ADD CONSTRAINT vex_statements_pkey PRIMARY KEY (id);


--
-- Name: vex_statements vex_statements_target_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.vex_statements
    ADD CONSTRAINT vex_statements_target_key UNIQUE (repository_id, artifact_id, vuln_id);


--
-- Name: vuln_results vuln_results_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.vuln_results
    ADD CONSTRAINT vuln_results_pkey PRIMARY KEY (id);


--
-- Name: vuln_results vuln_results_scan_job_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.vuln_results
    ADD CONSTRAINT vuln_results_scan_job_id_key UNIQUE (scan_job_id);


--
-- Name: webhook_deliveries webhook_deliveries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_pkey PRIMARY KEY (id);


--
-- Name: webhooks webhooks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhooks
    ADD CONSTRAINT webhooks_pkey PRIMARY KEY (id);


--
-- Name: worker_requests worker_requests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_requests
    ADD CONSTRAINT worker_requests_pkey PRIMARY KEY (kind, key);


--
-- Name: auth_users_sso_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX auth_users_sso_unique ON public.auth_users USING btree (external_id, provider_slug) WHERE ((external_id IS NOT NULL) AND (provider_slug IS NOT NULL));


--
-- Name: docker_manifests_live_digest_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX docker_manifests_live_digest_key ON public.docker_manifests USING btree (repository_id, image, digest) WHERE (deleted_at IS NULL);


--
-- Name: docker_tags_live_name_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX docker_tags_live_name_key ON public.docker_tags USING btree (repository_id, image, name) WHERE (deleted_at IS NULL);


--
-- Name: gpg_key_repositories_key_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX gpg_key_repositories_key_idx ON public.gpg_key_repositories USING btree (gpg_key_id);


--
-- Name: gpg_keys_one_global_active; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX gpg_keys_one_global_active ON public.gpg_keys USING btree (is_global_active) WHERE is_global_active;


--
-- Name: idx_ai_results_license_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ai_results_license_status ON public.ai_results USING btree (license_status);


--
-- Name: idx_ai_results_quarantine; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ai_results_quarantine ON public.ai_results USING btree (scan_job_id) WHERE (quarantine_recommended = true);


--
-- Name: idx_ai_results_tags; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ai_results_tags ON public.ai_results USING gin (tags);


--
-- Name: idx_artifact_properties_ref_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_artifact_properties_ref_id ON public.artifact_properties USING btree (artifact_ref_id);


--
-- Name: idx_artifact_properties_repo_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_artifact_properties_repo_key ON public.artifact_properties USING btree (repository_id, key);


--
-- Name: idx_artifact_refs_sha256; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_artifact_refs_sha256 ON public.artifact_refs USING btree (sha256) WHERE (sha256 IS NOT NULL);


--
-- Name: idx_audit_logs_action; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_audit_logs_action ON public.audit_logs USING btree (action);


--
-- Name: idx_audit_logs_actor_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_audit_logs_actor_id ON public.audit_logs USING btree (actor_id) WHERE (actor_id IS NOT NULL);


--
-- Name: idx_audit_logs_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_audit_logs_created_at ON public.audit_logs USING btree (created_at);


--
-- Name: idx_audit_logs_project_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_audit_logs_project_id ON public.audit_logs USING btree (project_id) WHERE (project_id IS NOT NULL);


--
-- Name: idx_audit_logs_repository_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_audit_logs_repository_id ON public.audit_logs USING btree (repository_id) WHERE (repository_id IS NOT NULL);


--
-- Name: idx_audit_logs_resource; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_audit_logs_resource ON public.audit_logs USING btree (resource_type, resource_id) WHERE (resource_type IS NOT NULL);


--
-- Name: idx_auth_saml_assertions_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_auth_saml_assertions_expires_at ON public.auth_saml_assertions USING btree (expires_at);


--
-- Name: idx_auth_sessions_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_auth_sessions_expires_at ON public.auth_sessions USING btree (expires_at);


--
-- Name: idx_auth_sessions_user_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_auth_sessions_user_active ON public.auth_sessions USING btree (user_id, expires_at);


--
-- Name: idx_auth_tokens_created_by; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_auth_tokens_created_by ON public.auth_tokens USING btree (created_by) WHERE (project_id IS NOT NULL);


--
-- Name: idx_auth_tokens_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_auth_tokens_expires_at ON public.auth_tokens USING btree (expires_at) WHERE (expires_at IS NOT NULL);


--
-- Name: idx_auth_tokens_project_name; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_auth_tokens_project_name ON public.auth_tokens USING btree (project_id, name) WHERE ((project_id IS NOT NULL) AND (revoked_at IS NULL));


--
-- Name: idx_auth_tokens_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_auth_tokens_user_id ON public.auth_tokens USING btree (user_id);


--
-- Name: idx_auth_user_groups_group_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_auth_user_groups_group_id ON public.auth_user_groups USING btree (group_id);


--
-- Name: idx_auth_users_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_auth_users_project ON public.auth_users USING btree (project_id) WHERE (project_id IS NOT NULL);


--
-- Name: idx_backup_jobs_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_backup_jobs_status ON public.backup_jobs USING btree (status);


--
-- Name: idx_backup_leases_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_backup_leases_expires_at ON public.backup_leases USING btree (expires_at);


--
-- Name: idx_blob_deletions_not_before; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_blob_deletions_not_before ON public.blob_deletions USING btree (not_before);


--
-- Name: idx_blob_replication_due; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_blob_replication_due ON public.blob_replication USING btree (next_attempt_at);


--
-- Name: idx_blobs_storage_backend; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_blobs_storage_backend ON public.blobs USING btree (storage_backend_id) WHERE (storage_backend_id IS NOT NULL);


--
-- Name: idx_blobs_tier_promote; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_blobs_tier_promote ON public.blobs USING btree (sha256) WHERE ((storage_backend_id IS NOT NULL) AND (last_accessed_at IS NOT NULL) AND ((tier_changed_at IS NULL) OR (last_accessed_at > tier_changed_at)));


--
-- Name: idx_bundle_items_sha256; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bundle_items_sha256 ON public.release_bundle_items USING btree (sha256);


--
-- Name: idx_bundle_items_source_repo_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bundle_items_source_repo_id ON public.release_bundle_items USING btree (source_repo_id);


--
-- Name: idx_bundle_promotions_bundle; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bundle_promotions_bundle ON public.bundle_promotions USING btree (bundle_id);


--
-- Name: idx_bundle_promotions_promoter; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bundle_promotions_promoter ON public.bundle_promotions USING btree (promoter_id) WHERE (promoter_id IS NOT NULL);


--
-- Name: idx_bundle_promotions_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bundle_promotions_status ON public.bundle_promotions USING btree (status) WHERE ((status)::text = 'in_progress'::text);


--
-- Name: idx_cleanup_policies_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cleanup_policies_enabled ON public.cleanup_policies USING btree (enabled) WHERE (enabled = true);


--
-- Name: idx_cleanup_policies_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cleanup_policies_project ON public.cleanup_policies USING btree (project_id) WHERE (project_id IS NOT NULL);


--
-- Name: idx_cleanup_policies_repository_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cleanup_policies_repository_id ON public.cleanup_policies USING btree (repository_id);


--
-- Name: idx_docker_blobs_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_docker_blobs_blob_id ON public.docker_blobs USING btree (blob_id);


--
-- Name: idx_docker_blobs_digest; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_docker_blobs_digest ON public.docker_blobs USING btree (digest);


--
-- Name: idx_docker_manifest_refs_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_docker_manifest_refs_lookup ON public.docker_manifest_refs USING btree (repository_id, kind, digest);


--
-- Name: idx_docker_manifests_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_docker_manifests_blob_id ON public.docker_manifests USING btree (blob_id);


--
-- Name: idx_docker_manifests_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_docker_manifests_deleted ON public.docker_manifests USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_docker_manifests_digest; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_docker_manifests_digest ON public.docker_manifests USING btree (digest);


--
-- Name: idx_docker_manifests_repository_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_docker_manifests_repository_id ON public.docker_manifests USING btree (repository_id);


--
-- Name: idx_docker_manifests_subject; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_docker_manifests_subject ON public.docker_manifests USING btree (repository_id, image, subject_digest) WHERE (subject_digest IS NOT NULL);


--
-- Name: idx_docker_tags_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_docker_tags_deleted ON public.docker_tags USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_docker_tags_manifest_digest; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_docker_tags_manifest_digest ON public.docker_tags USING btree (repository_id, manifest_digest);


--
-- Name: idx_docker_tags_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_docker_tags_name_trgm ON public.docker_tags USING gin (name public.gin_trgm_ops);


--
-- Name: idx_docker_tags_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_docker_tags_repo_created ON public.docker_tags USING btree (repository_id, created_at DESC, id);


--
-- Name: idx_docker_uploads_repository_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_docker_uploads_repository_id ON public.docker_uploads USING btree (repository_id);


--
-- Name: idx_federation_peer_tokens_repo; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_federation_peer_tokens_repo ON public.federation_peer_tokens USING btree (repo_id);


--
-- Name: idx_federation_units_seq; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_federation_units_seq ON public.federation_units USING btree (repo_id, seq);


--
-- Name: idx_group_package_owners_member; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_group_package_owners_member ON public.group_package_owners USING btree (member_repo_id);


--
-- Name: idx_license_policies_repository_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_license_policies_repository_id ON public.license_policies USING btree (repository_id);


--
-- Name: idx_license_results_quarantined; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_license_results_quarantined ON public.license_results USING btree (scan_job_id) WHERE (quarantined = true);


--
-- Name: idx_nix_pending_nars_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_nix_pending_nars_blob_id ON public.nix_pending_nars USING btree (blob_id);


--
-- Name: idx_nix_pending_nars_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_nix_pending_nars_created ON public.nix_pending_nars USING btree (created_at);


--
-- Name: idx_osv_advisory_aliases_alias; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_osv_advisory_aliases_alias ON public.osv_advisory_aliases USING btree (alias);


--
-- Name: idx_osv_affected_advisory; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_osv_affected_advisory ON public.osv_affected USING btree (advisory_id);


--
-- Name: idx_osv_affected_package; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_osv_affected_package ON public.osv_affected USING btree (ecosystem, name_norm);


--
-- Name: idx_package_deltas_delta_sha256; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_package_deltas_delta_sha256 ON public.package_deltas USING btree (delta_sha256);


--
-- Name: idx_package_deltas_from_to; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_package_deltas_from_to ON public.package_deltas USING btree (repository_id, from_sha256, to_sha256);


--
-- Name: idx_package_deltas_to_sha256; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_package_deltas_to_sha256 ON public.package_deltas USING btree (repository_id, to_sha256);


--
-- Name: idx_permission_assignments_principal; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_permission_assignments_principal ON public.permission_assignments USING btree (principal_id, principal_type);


--
-- Name: idx_permission_assignments_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_permission_assignments_unique ON public.permission_assignments USING btree (permission_target_id, principal_type, principal_id);


--
-- Name: idx_presigned_uploads_created_by; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_presigned_uploads_created_by ON public.presigned_uploads USING btree (created_by) WHERE (completed_at IS NULL);


--
-- Name: idx_presigned_uploads_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_presigned_uploads_expires ON public.presigned_uploads USING btree (expires_at);


--
-- Name: idx_presigned_uploads_repository; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_presigned_uploads_repository ON public.presigned_uploads USING btree (repository_id);


--
-- Name: idx_project_groups_group; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_project_groups_group ON public.project_groups USING btree (group_id);


--
-- Name: idx_project_members_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_project_members_user_id ON public.project_members USING btree (user_id);


--
-- Name: idx_proxy_cache_blob_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_proxy_cache_blob_key ON public.proxy_cache_entries USING btree (blob_key) WHERE (blob_key IS NOT NULL);


--
-- Name: idx_proxy_cache_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_proxy_cache_expires ON public.proxy_cache_entries USING btree (expires_at);


--
-- Name: idx_quota_pending_repo; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_quota_pending_repo ON public.quota_pending USING btree (repository_id, size_bytes, artifact_count, id);


--
-- Name: idx_recovery_sidecars_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_recovery_sidecars_pending ON public.recovery_sidecars USING btree (pending_sha256) WHERE (pending_sha256 IS NOT NULL);


--
-- Name: idx_recovery_sidecars_sha; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_recovery_sidecars_sha ON public.recovery_sidecars USING btree (sidecar_sha256);


--
-- Name: idx_release_bundles_creator; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_release_bundles_creator ON public.release_bundles USING btree (creator_id) WHERE (creator_id IS NOT NULL);


--
-- Name: idx_release_bundles_pipeline; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_release_bundles_pipeline ON public.release_bundles USING btree (pipeline_id) WHERE (pipeline_id IS NOT NULL);


--
-- Name: idx_release_bundles_state; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_release_bundles_state ON public.release_bundles USING btree (state);


--
-- Name: idx_repositories_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_repositories_deleted ON public.repositories USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_repositories_format; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_repositories_format ON public.repositories USING btree (format);


--
-- Name: idx_repositories_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_repositories_name_trgm ON public.repositories USING gin (name public.gin_trgm_ops);


--
-- Name: idx_repository_group_members_member; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_repository_group_members_member ON public.repository_group_members USING btree (member_repo_id);


--
-- Name: idx_repository_metadata_blob_sha256; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_repository_metadata_blob_sha256 ON public.repository_metadata USING btree (blob_sha256);


--
-- Name: idx_rpm_repos_stale; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rpm_repos_stale ON public.rpm_repos USING btree (metadata_stale) WHERE (metadata_stale = true);


--
-- Name: idx_sbom_results_blob; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sbom_results_blob ON public.sbom_results USING btree (blob_id) WHERE (blob_id IS NOT NULL);


--
-- Name: idx_scan_jobs_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_scan_jobs_pending ON public.scan_jobs USING btree (scan_type, created_at) WHERE ((status)::text = ANY ((ARRAY['pending'::character varying, 'running'::character varying])::text[]));


--
-- Name: idx_scan_jobs_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_scan_jobs_status ON public.scan_jobs USING btree (status) WHERE ((status)::text = ANY ((ARRAY['pending'::character varying, 'running'::character varying])::text[]));


--
-- Name: idx_scan_jobs_vuln_problems; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_scan_jobs_vuln_problems ON public.scan_jobs USING btree (status, created_at DESC) WHERE (((scan_type)::text = 'vuln'::text) AND ((status)::text = ANY ((ARRAY['failed'::character varying, 'partial'::character varying])::text[])));


--
-- Name: idx_secret_results_quarantined; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_secret_results_quarantined ON public.secret_results USING btree (scan_job_id) WHERE (quarantined = true);


--
-- Name: idx_storage_tiers_backend; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_storage_tiers_backend ON public.storage_tiers USING btree (backend_id) WHERE (backend_id IS NOT NULL);


--
-- Name: idx_storage_tiers_primary; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_storage_tiers_primary ON public.storage_tiers USING btree ((true)) WHERE (backend_id IS NULL);


--
-- Name: idx_vuln_results_critical; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_vuln_results_critical ON public.vuln_results USING btree (critical_count) WHERE (critical_count > 0);


--
-- Name: idx_vuln_results_kev; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_vuln_results_kev ON public.vuln_results USING btree (scan_job_id) WHERE (kev_count > 0);


--
-- Name: idx_vuln_results_quarantined; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_vuln_results_quarantined ON public.vuln_results USING btree (scan_job_id) WHERE (quarantined = true);


--
-- Name: idx_webhook_deliveries_finished_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_webhook_deliveries_finished_created ON public.webhook_deliveries USING btree (created_at) WHERE ((status)::text = ANY ((ARRAY['success'::character varying, 'failed'::character varying])::text[]));


--
-- Name: idx_webhook_deliveries_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_webhook_deliveries_pending ON public.webhook_deliveries USING btree (next_retry_at) WHERE ((status)::text = 'pending'::text);


--
-- Name: idx_webhook_deliveries_retry_worker; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_webhook_deliveries_retry_worker ON public.webhook_deliveries USING btree (webhook_id, next_retry_at) WHERE ((status)::text = ANY ((ARRAY['pending'::character varying, 'failed'::character varying])::text[]));


--
-- Name: idx_webhook_deliveries_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_webhook_deliveries_status ON public.webhook_deliveries USING btree (status) WHERE ((status)::text = ANY ((ARRAY['pending'::character varying, 'failed'::character varying])::text[]));


--
-- Name: idx_webhook_deliveries_webhook_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_webhook_deliveries_webhook_created ON public.webhook_deliveries USING btree (webhook_id, created_at DESC);


--
-- Name: idx_webhooks_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_webhooks_enabled ON public.webhooks USING btree (enabled) WHERE (enabled = true);


--
-- Name: idx_webhooks_repository_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_webhooks_repository_id ON public.webhooks USING btree (repository_id);


--
-- Name: idx_worker_requests_requested_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_worker_requests_requested_at ON public.worker_requests USING btree (requested_at);


--
-- Name: recovery_dirty_seq_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX recovery_dirty_seq_idx ON public.recovery_dirty USING btree (seq);


--
-- Name: repositories_name_global_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX repositories_name_global_unique ON public.repositories USING btree (name);


--
-- Name: docker_manifests federation_capture; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER federation_capture AFTER INSERT OR DELETE OR UPDATE ON public.docker_manifests FOR EACH ROW EXECUTE FUNCTION public.federation_trg_docker_manifests();


--
-- Name: docker_tags federation_capture; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER federation_capture AFTER INSERT OR DELETE OR UPDATE ON public.docker_tags FOR EACH ROW EXECUTE FUNCTION public.federation_trg_docker_tags();


--
-- Name: docker_blobs quota_pending_del; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER quota_pending_del AFTER DELETE ON public.docker_blobs FOR EACH ROW EXECUTE FUNCTION public.quota_pending_docker_blobs();


--
-- Name: docker_manifests quota_pending_del; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER quota_pending_del AFTER DELETE ON public.docker_manifests FOR EACH ROW EXECUTE FUNCTION public.quota_pending_docker_manifests();


--
-- Name: docker_blobs quota_pending_ins; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER quota_pending_ins AFTER INSERT ON public.docker_blobs FOR EACH ROW EXECUTE FUNCTION public.quota_pending_docker_blobs();


--
-- Name: docker_manifests quota_pending_ins; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER quota_pending_ins AFTER INSERT ON public.docker_manifests FOR EACH ROW EXECUTE FUNCTION public.quota_pending_docker_manifests();


--
-- Name: docker_blobs quota_pending_upd; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER quota_pending_upd AFTER UPDATE OF blob_id ON public.docker_blobs FOR EACH ROW WHEN ((old.blob_id IS DISTINCT FROM new.blob_id)) EXECUTE FUNCTION public.quota_pending_docker_blobs();


--
-- Name: docker_manifests quota_pending_upd; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER quota_pending_upd AFTER UPDATE OF blob_id ON public.docker_manifests FOR EACH ROW WHEN ((old.blob_id IS DISTINCT FROM new.blob_id)) EXECUTE FUNCTION public.quota_pending_docker_manifests();


--
-- Name: artifact_properties recovery_journal; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.artifact_properties FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();


--
-- Name: artifact_refs recovery_journal; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.artifact_refs FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();


--
-- Name: blobs recovery_journal; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.blobs FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();


--
-- Name: debian_architectures recovery_journal; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.debian_architectures FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();


--
-- Name: debian_components recovery_journal; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.debian_components FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();


--
-- Name: debian_distributions recovery_journal; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.debian_distributions FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();


--
-- Name: docker_blob_images recovery_journal; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.docker_blob_images FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_docker_blob_images();


--
-- Name: docker_blobs recovery_journal; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.docker_blobs FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();


--
-- Name: docker_manifest_refs recovery_journal; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.docker_manifest_refs FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_docker_manifest_refs();


--
-- Name: docker_manifests recovery_journal; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.docker_manifests FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();


--
-- Name: docker_tags recovery_journal; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.docker_tags FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();


--
-- Name: projects recovery_journal; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.projects FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();


--
-- Name: repositories recovery_journal; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.repositories FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();


--
-- Name: repository_group_members recovery_journal; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.repository_group_members FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_group_members();


--
-- Name: rpm_repos recovery_journal; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.rpm_repos FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();


--
-- Name: auth_users trg_auth_users_revoke_project_tokens; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_auth_users_revoke_project_tokens BEFORE DELETE ON public.auth_users FOR EACH ROW EXECUTE FUNCTION public.revoke_project_tokens_of_deleted_user();


--
-- Name: apk_signing_keys update_apk_signing_keys_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_apk_signing_keys_updated_at BEFORE UPDATE ON public.apk_signing_keys FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: artifact_properties update_artifact_properties_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_artifact_properties_updated_at BEFORE UPDATE ON public.artifact_properties FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: auth_groups update_auth_groups_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_auth_groups_updated_at BEFORE UPDATE ON public.auth_groups FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: auth_sso_providers update_auth_sso_providers_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_auth_sso_providers_updated_at BEFORE UPDATE ON public.auth_sso_providers FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: auth_user_groups update_auth_user_groups_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_auth_user_groups_updated_at BEFORE UPDATE ON public.auth_user_groups FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: auth_users update_auth_users_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_auth_users_updated_at BEFORE UPDATE ON public.auth_users FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: backup_jobs update_backup_jobs_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_backup_jobs_updated_at BEFORE UPDATE ON public.backup_jobs FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: cleanup_policies update_cleanup_policies_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_cleanup_policies_updated_at BEFORE UPDATE ON public.cleanup_policies FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: debian_architectures update_debian_architectures_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_debian_architectures_updated_at BEFORE UPDATE ON public.debian_architectures FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: debian_components update_debian_components_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_debian_components_updated_at BEFORE UPDATE ON public.debian_components FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: debian_distributions update_debian_distributions_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_debian_distributions_updated_at BEFORE UPDATE ON public.debian_distributions FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: docker_manifests update_docker_manifests_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_docker_manifests_updated_at BEFORE UPDATE ON public.docker_manifests FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: docker_tags update_docker_tags_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_docker_tags_updated_at BEFORE UPDATE ON public.docker_tags FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: docker_uploads update_docker_uploads_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_docker_uploads_updated_at BEFORE UPDATE ON public.docker_uploads FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: federation_peers update_federation_peers_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_federation_peers_updated_at BEFORE UPDATE ON public.federation_peers FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: license_policies update_license_policies_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_license_policies_updated_at BEFORE UPDATE ON public.license_policies FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: nix_signing_keys update_nix_signing_keys_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_nix_signing_keys_updated_at BEFORE UPDATE ON public.nix_signing_keys FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: permission_targets update_permission_targets_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_permission_targets_updated_at BEFORE UPDATE ON public.permission_targets FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: project_members update_project_members_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_project_members_updated_at BEFORE UPDATE ON public.project_members FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: project_quotas update_project_quotas_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_project_quotas_updated_at BEFORE UPDATE ON public.project_quotas FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: projects update_projects_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_projects_updated_at BEFORE UPDATE ON public.projects FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: promotion_pipelines update_promotion_pipelines_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_promotion_pipelines_updated_at BEFORE UPDATE ON public.promotion_pipelines FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: proxy_cache_entries update_proxy_cache_entries_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_proxy_cache_entries_updated_at BEFORE UPDATE ON public.proxy_cache_entries FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: release_bundles update_release_bundles_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_release_bundles_updated_at BEFORE UPDATE ON public.release_bundles FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: repositories update_repositories_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_repositories_updated_at BEFORE UPDATE ON public.repositories FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: rpm_repos update_rpm_repos_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_rpm_repos_updated_at BEFORE UPDATE ON public.rpm_repos FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: scan_jobs update_scan_jobs_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_scan_jobs_updated_at BEFORE UPDATE ON public.scan_jobs FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: storage_backends update_storage_backends_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_storage_backends_updated_at BEFORE UPDATE ON public.storage_backends FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: storage_policies update_storage_policies_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_storage_policies_updated_at BEFORE UPDATE ON public.storage_policies FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: storage_quotas update_storage_quotas_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_storage_quotas_updated_at BEFORE UPDATE ON public.storage_quotas FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: storage_tiers update_storage_tiers_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_storage_tiers_updated_at BEFORE UPDATE ON public.storage_tiers FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: system_settings update_system_settings_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_system_settings_updated_at BEFORE UPDATE ON public.system_settings FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: webhook_deliveries update_webhook_deliveries_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_webhook_deliveries_updated_at BEFORE UPDATE ON public.webhook_deliveries FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: webhooks update_webhooks_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_webhooks_updated_at BEFORE UPDATE ON public.webhooks FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();


--
-- Name: ai_results ai_results_scan_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ai_results
    ADD CONSTRAINT ai_results_scan_job_id_fkey FOREIGN KEY (scan_job_id) REFERENCES public.scan_jobs(id) ON DELETE CASCADE;


--
-- Name: apk_signing_keys apk_signing_keys_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.apk_signing_keys
    ADD CONSTRAINT apk_signing_keys_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: artifact_properties artifact_properties_artifact_ref_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.artifact_properties
    ADD CONSTRAINT artifact_properties_artifact_ref_id_fkey FOREIGN KEY (artifact_ref_id) REFERENCES public.artifact_refs(id) ON DELETE CASCADE;


--
-- Name: artifact_properties artifact_properties_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.artifact_properties
    ADD CONSTRAINT artifact_properties_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: artifact_refs artifact_refs_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.artifact_refs
    ADD CONSTRAINT artifact_refs_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: audit_logs audit_logs_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_logs
    ADD CONSTRAINT audit_logs_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.auth_users(id) ON DELETE SET NULL;


--
-- Name: audit_logs audit_logs_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_logs
    ADD CONSTRAINT audit_logs_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE SET NULL;


--
-- Name: audit_logs audit_logs_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_logs
    ADD CONSTRAINT audit_logs_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE SET NULL;


--
-- Name: auth_groups auth_groups_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_groups
    ADD CONSTRAINT auth_groups_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: auth_session_sso auth_session_sso_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_session_sso
    ADD CONSTRAINT auth_session_sso_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.auth_sessions(id) ON DELETE CASCADE;


--
-- Name: auth_sessions auth_sessions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_sessions
    ADD CONSTRAINT auth_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.auth_users(id) ON DELETE CASCADE;


--
-- Name: auth_tokens auth_tokens_created_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_tokens
    ADD CONSTRAINT auth_tokens_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.auth_users(id) ON DELETE SET NULL;


--
-- Name: auth_tokens auth_tokens_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_tokens
    ADD CONSTRAINT auth_tokens_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: auth_tokens auth_tokens_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_tokens
    ADD CONSTRAINT auth_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.auth_users(id) ON DELETE CASCADE;


--
-- Name: auth_user_groups auth_user_groups_group_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_user_groups
    ADD CONSTRAINT auth_user_groups_group_id_fkey FOREIGN KEY (group_id) REFERENCES public.auth_groups(id) ON DELETE CASCADE;


--
-- Name: auth_user_groups auth_user_groups_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_user_groups
    ADD CONSTRAINT auth_user_groups_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.auth_users(id) ON DELETE CASCADE;


--
-- Name: auth_users auth_users_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_users
    ADD CONSTRAINT auth_users_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: blobs blobs_storage_backend_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.blobs
    ADD CONSTRAINT blobs_storage_backend_id_fkey FOREIGN KEY (storage_backend_id) REFERENCES public.storage_backends(id) ON DELETE RESTRICT;


--
-- Name: bundle_promotions bundle_promotions_bundle_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bundle_promotions
    ADD CONSTRAINT bundle_promotions_bundle_id_fkey FOREIGN KEY (bundle_id) REFERENCES public.release_bundles(id) ON DELETE CASCADE;


--
-- Name: bundle_promotions bundle_promotions_promoter_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bundle_promotions
    ADD CONSTRAINT bundle_promotions_promoter_id_fkey FOREIGN KEY (promoter_id) REFERENCES public.auth_users(id) ON DELETE SET NULL;


--
-- Name: bundle_promotions bundle_promotions_target_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bundle_promotions
    ADD CONSTRAINT bundle_promotions_target_repo_id_fkey FOREIGN KEY (target_repo_id) REFERENCES public.repositories(id) ON DELETE RESTRICT;


--
-- Name: cleanup_policies cleanup_policies_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cleanup_policies
    ADD CONSTRAINT cleanup_policies_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: cleanup_policies cleanup_policies_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cleanup_policies
    ADD CONSTRAINT cleanup_policies_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: debian_architectures debian_architectures_distribution_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.debian_architectures
    ADD CONSTRAINT debian_architectures_distribution_id_fkey FOREIGN KEY (distribution_id) REFERENCES public.debian_distributions(id) ON DELETE CASCADE;


--
-- Name: debian_components debian_components_distribution_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.debian_components
    ADD CONSTRAINT debian_components_distribution_id_fkey FOREIGN KEY (distribution_id) REFERENCES public.debian_distributions(id) ON DELETE CASCADE;


--
-- Name: debian_distributions debian_distributions_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.debian_distributions
    ADD CONSTRAINT debian_distributions_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: dependency_track_pushes dependency_track_pushes_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dependency_track_pushes
    ADD CONSTRAINT dependency_track_pushes_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: docker_blob_images docker_blob_images_blob_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.docker_blob_images
    ADD CONSTRAINT docker_blob_images_blob_fkey FOREIGN KEY (repository_id, digest) REFERENCES public.docker_blobs(repository_id, digest) ON DELETE CASCADE;


--
-- Name: docker_blobs docker_blobs_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.docker_blobs
    ADD CONSTRAINT docker_blobs_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: docker_blobs docker_blobs_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.docker_blobs
    ADD CONSTRAINT docker_blobs_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: docker_manifest_refs docker_manifest_refs_manifest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.docker_manifest_refs
    ADD CONSTRAINT docker_manifest_refs_manifest_id_fkey FOREIGN KEY (manifest_id) REFERENCES public.docker_manifests(id) ON DELETE CASCADE;


--
-- Name: docker_manifests docker_manifests_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.docker_manifests
    ADD CONSTRAINT docker_manifests_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: docker_manifests docker_manifests_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.docker_manifests
    ADD CONSTRAINT docker_manifests_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: docker_tags docker_tags_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.docker_tags
    ADD CONSTRAINT docker_tags_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: docker_uploads docker_uploads_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.docker_uploads
    ADD CONSTRAINT docker_uploads_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: federation_deferred federation_deferred_peer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.federation_deferred
    ADD CONSTRAINT federation_deferred_peer_id_fkey FOREIGN KEY (peer_id) REFERENCES public.federation_peers(id) ON DELETE CASCADE;


--
-- Name: federation_peer_tokens federation_peer_tokens_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.federation_peer_tokens
    ADD CONSTRAINT federation_peer_tokens_repo_id_fkey FOREIGN KEY (repo_id) REFERENCES public.federation_repos(repo_id) ON DELETE CASCADE;


--
-- Name: federation_peers federation_peers_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.federation_peers
    ADD CONSTRAINT federation_peers_repo_id_fkey FOREIGN KEY (repo_id) REFERENCES public.federation_repos(repo_id) ON DELETE CASCADE;


--
-- Name: federation_repos federation_repos_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.federation_repos
    ADD CONSTRAINT federation_repos_repo_id_fkey FOREIGN KEY (repo_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: federation_units federation_units_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.federation_units
    ADD CONSTRAINT federation_units_repo_id_fkey FOREIGN KEY (repo_id) REFERENCES public.federation_repos(repo_id) ON DELETE CASCADE;


--
-- Name: repository_metadata fk_repo_metadata_repository; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_metadata
    ADD CONSTRAINT fk_repo_metadata_repository FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: repositories fk_repositories_project; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repositories
    ADD CONSTRAINT fk_repositories_project FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE RESTRICT;


--
-- Name: rpm_repos fk_rpm_repos_repository; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.rpm_repos
    ADD CONSTRAINT fk_rpm_repos_repository FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: gpg_key_repositories gpg_key_repositories_gpg_key_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gpg_key_repositories
    ADD CONSTRAINT gpg_key_repositories_gpg_key_id_fkey FOREIGN KEY (gpg_key_id) REFERENCES public.gpg_keys(id) ON DELETE CASCADE;


--
-- Name: gpg_key_repositories gpg_key_repositories_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gpg_key_repositories
    ADD CONSTRAINT gpg_key_repositories_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: group_metadata_state group_metadata_state_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_metadata_state
    ADD CONSTRAINT group_metadata_state_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: group_package_owners group_package_owners_group_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_package_owners
    ADD CONSTRAINT group_package_owners_group_repo_id_fkey FOREIGN KEY (group_repo_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: group_package_owners group_package_owners_member_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_package_owners
    ADD CONSTRAINT group_package_owners_member_repo_id_fkey FOREIGN KEY (member_repo_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: license_policies license_policies_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.license_policies
    ADD CONSTRAINT license_policies_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: license_results license_results_scan_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.license_results
    ADD CONSTRAINT license_results_scan_job_id_fkey FOREIGN KEY (scan_job_id) REFERENCES public.scan_jobs(id) ON DELETE CASCADE;


--
-- Name: nix_pending_nars nix_pending_nars_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nix_pending_nars
    ADD CONSTRAINT nix_pending_nars_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: nix_pending_nars nix_pending_nars_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nix_pending_nars
    ADD CONSTRAINT nix_pending_nars_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: nix_signing_keys nix_signing_keys_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nix_signing_keys
    ADD CONSTRAINT nix_signing_keys_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: osv_advisory_aliases osv_advisory_aliases_advisory_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.osv_advisory_aliases
    ADD CONSTRAINT osv_advisory_aliases_advisory_fkey FOREIGN KEY (advisory_id) REFERENCES public.osv_advisories(id) ON DELETE CASCADE;


--
-- Name: osv_affected osv_affected_advisory_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.osv_affected
    ADD CONSTRAINT osv_affected_advisory_fkey FOREIGN KEY (advisory_id) REFERENCES public.osv_advisories(id) ON DELETE CASCADE;


--
-- Name: package_deltas package_deltas_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.package_deltas
    ADD CONSTRAINT package_deltas_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: permission_assignments permission_assignments_permission_target_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.permission_assignments
    ADD CONSTRAINT permission_assignments_permission_target_id_fkey FOREIGN KEY (permission_target_id) REFERENCES public.permission_targets(id) ON DELETE CASCADE;


--
-- Name: permission_targets permission_targets_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.permission_targets
    ADD CONSTRAINT permission_targets_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: presigned_uploads presigned_uploads_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.presigned_uploads
    ADD CONSTRAINT presigned_uploads_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: project_groups project_groups_created_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_groups
    ADD CONSTRAINT project_groups_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.auth_users(id) ON DELETE SET NULL;


--
-- Name: project_groups project_groups_group_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_groups
    ADD CONSTRAINT project_groups_group_id_fkey FOREIGN KEY (group_id) REFERENCES public.auth_groups(id) ON DELETE CASCADE;


--
-- Name: project_groups project_groups_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_groups
    ADD CONSTRAINT project_groups_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: project_members project_members_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_members
    ADD CONSTRAINT project_members_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: project_members project_members_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_members
    ADD CONSTRAINT project_members_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.auth_users(id) ON DELETE CASCADE;


--
-- Name: project_quotas project_quotas_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_quotas
    ADD CONSTRAINT project_quotas_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: projects projects_owner_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES public.auth_users(id) ON DELETE SET NULL;


--
-- Name: promotion_pipelines promotion_pipelines_creator_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_pipelines
    ADD CONSTRAINT promotion_pipelines_creator_id_fkey FOREIGN KEY (creator_id) REFERENCES public.auth_users(id) ON DELETE SET NULL;


--
-- Name: proxy_cache_entries proxy_cache_entries_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proxy_cache_entries
    ADD CONSTRAINT proxy_cache_entries_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: release_bundle_items release_bundle_items_adder_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.release_bundle_items
    ADD CONSTRAINT release_bundle_items_adder_id_fkey FOREIGN KEY (adder_id) REFERENCES public.auth_users(id) ON DELETE SET NULL;


--
-- Name: release_bundle_items release_bundle_items_artifact_ref_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.release_bundle_items
    ADD CONSTRAINT release_bundle_items_artifact_ref_id_fkey FOREIGN KEY (artifact_ref_id) REFERENCES public.artifact_refs(id) ON DELETE SET NULL;


--
-- Name: release_bundle_items release_bundle_items_bundle_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.release_bundle_items
    ADD CONSTRAINT release_bundle_items_bundle_id_fkey FOREIGN KEY (bundle_id) REFERENCES public.release_bundles(id) ON DELETE CASCADE;


--
-- Name: release_bundle_items release_bundle_items_source_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.release_bundle_items
    ADD CONSTRAINT release_bundle_items_source_repo_id_fkey FOREIGN KEY (source_repo_id) REFERENCES public.repositories(id) ON DELETE RESTRICT;


--
-- Name: release_bundles release_bundles_archiver_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.release_bundles
    ADD CONSTRAINT release_bundles_archiver_id_fkey FOREIGN KEY (archiver_id) REFERENCES public.auth_users(id) ON DELETE SET NULL;


--
-- Name: release_bundles release_bundles_creator_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.release_bundles
    ADD CONSTRAINT release_bundles_creator_id_fkey FOREIGN KEY (creator_id) REFERENCES public.auth_users(id) ON DELETE SET NULL;


--
-- Name: release_bundles release_bundles_pipeline_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.release_bundles
    ADD CONSTRAINT release_bundles_pipeline_id_fkey FOREIGN KEY (pipeline_id) REFERENCES public.promotion_pipelines(id) ON DELETE SET NULL;


--
-- Name: release_bundles release_bundles_sealer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.release_bundles
    ADD CONSTRAINT release_bundles_sealer_id_fkey FOREIGN KEY (sealer_id) REFERENCES public.auth_users(id) ON DELETE SET NULL;


--
-- Name: repository_attestation_attach repository_attestation_attach_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_attestation_attach
    ADD CONSTRAINT repository_attestation_attach_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: repository_dependency_track repository_dependency_track_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_dependency_track
    ADD CONSTRAINT repository_dependency_track_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: repository_group_members repository_group_members_group_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_group_members
    ADD CONSTRAINT repository_group_members_group_repo_id_fkey FOREIGN KEY (group_repo_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: repository_group_members repository_group_members_member_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_group_members
    ADD CONSTRAINT repository_group_members_member_repo_id_fkey FOREIGN KEY (member_repo_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: sbom_results sbom_results_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sbom_results
    ADD CONSTRAINT sbom_results_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE SET NULL;


--
-- Name: sbom_results sbom_results_scan_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sbom_results
    ADD CONSTRAINT sbom_results_scan_job_id_fkey FOREIGN KEY (scan_job_id) REFERENCES public.scan_jobs(id) ON DELETE CASCADE;


--
-- Name: scan_jobs scan_jobs_artifact_ref_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scan_jobs
    ADD CONSTRAINT scan_jobs_artifact_ref_id_fkey FOREIGN KEY (artifact_ref_id) REFERENCES public.artifact_refs(id) ON DELETE CASCADE;


--
-- Name: secret_results secret_results_scan_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.secret_results
    ADD CONSTRAINT secret_results_scan_job_id_fkey FOREIGN KEY (scan_job_id) REFERENCES public.scan_jobs(id) ON DELETE CASCADE;


--
-- Name: storage_policies storage_policies_from_tier_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_policies
    ADD CONSTRAINT storage_policies_from_tier_id_fkey FOREIGN KEY (from_tier_id) REFERENCES public.storage_tiers(id) ON DELETE RESTRICT;


--
-- Name: storage_policies storage_policies_target_tier_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_policies
    ADD CONSTRAINT storage_policies_target_tier_id_fkey FOREIGN KEY (target_tier_id) REFERENCES public.storage_tiers(id) ON DELETE RESTRICT;


--
-- Name: storage_quotas storage_quotas_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_quotas
    ADD CONSTRAINT storage_quotas_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: storage_tier_moves storage_tier_moves_from_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_tier_moves
    ADD CONSTRAINT storage_tier_moves_from_fkey FOREIGN KEY (from_backend_id) REFERENCES public.storage_backends(id) ON DELETE RESTRICT;


--
-- Name: storage_tier_moves storage_tier_moves_to_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_tier_moves
    ADD CONSTRAINT storage_tier_moves_to_fkey FOREIGN KEY (to_backend_id) REFERENCES public.storage_backends(id) ON DELETE RESTRICT;


--
-- Name: storage_tiers storage_tiers_backend_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.storage_tiers
    ADD CONSTRAINT storage_tiers_backend_id_fkey FOREIGN KEY (backend_id) REFERENCES public.storage_backends(id) ON DELETE RESTRICT;


--
-- Name: vex_statements vex_statements_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.vex_statements
    ADD CONSTRAINT vex_statements_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: vuln_results vuln_results_scan_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.vuln_results
    ADD CONSTRAINT vuln_results_scan_job_id_fkey FOREIGN KEY (scan_job_id) REFERENCES public.scan_jobs(id) ON DELETE CASCADE;


--
-- Name: webhook_deliveries webhook_deliveries_webhook_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_webhook_id_fkey FOREIGN KEY (webhook_id) REFERENCES public.webhooks(id) ON DELETE CASCADE;


--
-- Name: webhooks webhooks_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhooks
    ADD CONSTRAINT webhooks_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
--

--
-- Generic package tables (docs/design/format-plugins.md).
--
-- Every format except docker keeps its packages in packages (one logical
-- package of a repository), package_versions (one version incarnation) and
-- package_files (one stored file: the unit of listing, trash, quota, blob
-- references and scan ids). package_tags holds mutable named pointers to
-- versions (npm dist-tags). A format keeps a side table (<format>_files, keyed
-- by package_files.id) only for typed columns its metadata generation needs.
--

CREATE TABLE public.packages (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id uuid        NOT NULL REFERENCES public.repositories(id) ON DELETE CASCADE,
    format        varchar(16) NOT NULL,
    name          text        NOT NULL,
    name_key      text        NOT NULL,
    attrs         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE public.packages IS 'One logical package of a repository; identity is (repository_id, name_key)';
COMMENT ON COLUMN public.packages.name IS 'Listing and display name';
COMMENT ON COLUMN public.packages.name_key IS 'Identity inside the repository: the normalised name';
COMMENT ON COLUMN public.packages.attrs IS 'Package-level facts of the format (npm scope, apk branch/repo, maven group/artifact)';

CREATE UNIQUE INDEX uq_packages_name ON public.packages (repository_id, name_key);
CREATE INDEX idx_packages_name_trgm ON public.packages USING gin (name public.gin_trgm_ops);

CREATE TABLE public.package_versions (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    package_id    uuid        NOT NULL REFERENCES public.packages(id) ON DELETE CASCADE,
    repository_id uuid        NOT NULL REFERENCES public.repositories(id) ON DELETE CASCADE,
    version       text        NOT NULL,
    version_key   text        NOT NULL,
    metadata      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz,
    deleted_by    text
);

COMMENT ON TABLE public.package_versions IS 'One version incarnation; live iff at least one of its files is live';
COMMENT ON COLUMN public.package_versions.version_key IS 'Identity inside the package: the version plus the coordinates the format keys on; cleanup ranks by it';
COMMENT ON COLUMN public.package_versions.metadata IS 'Version-level metadata of the format (manifest, dependencies, descriptive fields)';

CREATE UNIQUE INDEX uq_package_versions_live ON public.package_versions (package_id, version_key) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_versions_pkg ON public.package_versions (package_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_versions_repo ON public.package_versions (repository_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_versions_deleted ON public.package_versions (package_id) WHERE deleted_at IS NOT NULL;

CREATE TABLE public.package_files (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    version_id     uuid        NOT NULL REFERENCES public.package_versions(id) ON DELETE CASCADE,
    repository_id  uuid        NOT NULL REFERENCES public.repositories(id) ON DELETE CASCADE,
    path           text        NOT NULL,
    filename       text        NOT NULL,
    blob_id        uuid        REFERENCES public.blobs(id) ON DELETE RESTRICT,
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

COMMENT ON TABLE public.package_files IS 'One stored file: the unit of listing, trash, quota, blob references and scan ids';
COMMENT ON COLUMN public.package_files.path IS 'Repository-wide locator, unique while live';
COMMENT ON COLUMN public.package_files.filename IS 'What a client downloads';
COMMENT ON COLUMN public.package_files.size_bytes IS 'Declared size that wins over the blob size when set (rpm, rubygems)';
COMMENT ON COLUMN public.package_files.role IS 'primary counts as one artifact for quota; any other role (go mod) counts none';
COMMENT ON COLUMN public.package_files.listed IS 'true: the file is a unit of the repository listing';
COMMENT ON COLUMN public.package_files.arch IS 'Platform qualifier shown by the listing and part of the retention partition';
COMMENT ON COLUMN public.package_files.part IS 'Extra retention-partition suffix (deb distribution, maven snapshot flag)';
COMMENT ON COLUMN public.package_files.attrs IS 'File-level facts of the format; the listing reads release, epoch, build_time, archive_size, installed_size';
COMMENT ON COLUMN public.package_files.checksums IS 'sha1/md5/sha512/shasum/integrity for checksum search; sha256 is the blob''s';
COMMENT ON COLUMN public.package_files.unit_key IS 'Federation unit key, set on the primary file of a federated unit';

CREATE UNIQUE INDEX uq_package_files_path ON public.package_files (repository_id, path) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_files_listing ON public.package_files (repository_id, created_at DESC, id) WHERE deleted_at IS NULL AND listed;
CREATE INDEX idx_package_files_version ON public.package_files (version_id);
CREATE INDEX idx_package_files_filename ON public.package_files (repository_id, filename) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_files_blob ON public.package_files (blob_id) WHERE blob_id IS NOT NULL;
CREATE INDEX idx_package_files_trash ON public.package_files (repository_id, deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX idx_package_files_sha1 ON public.package_files ((checksums ->> 'sha1')) WHERE checksums ? 'sha1' AND deleted_at IS NULL;
CREATE INDEX idx_package_files_md5 ON public.package_files ((checksums ->> 'md5')) WHERE checksums ? 'md5' AND deleted_at IS NULL;
CREATE INDEX idx_package_files_shasum ON public.package_files ((checksums ->> 'shasum')) WHERE checksums ? 'shasum' AND deleted_at IS NULL;
CREATE INDEX idx_package_files_unit ON public.package_files (repository_id, unit_key) WHERE unit_key IS NOT NULL AND deleted_at IS NULL;
-- Raw prefix listing (path LIKE 'dir/%') is a btree range scan only with text_pattern_ops under non-C collations.
CREATE INDEX idx_package_files_path_prefix ON public.package_files (repository_id, path text_pattern_ops) WHERE deleted_at IS NULL AND path IS NOT NULL;

CREATE TABLE public.package_tags (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    package_id uuid        NOT NULL REFERENCES public.packages(id) ON DELETE CASCADE,
    tag        text        NOT NULL,
    version    text        NOT NULL,
    unit_key   text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (package_id, tag)
);

COMMENT ON TABLE public.package_tags IS 'Mutable named pointers to versions (npm dist-tags); version is resolved at read time';

-- Federation looks a replicated tag up by its unit key.
CREATE INDEX idx_package_tags_unit ON public.package_tags (unit_key) WHERE unit_key IS NOT NULL;

CREATE TRIGGER update_packages_updated_at BEFORE UPDATE ON public.packages
    FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();
CREATE TRIGGER update_package_versions_updated_at BEFORE UPDATE ON public.package_versions
    FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();
-- A download-count or checksum-only update does not bump updated_at.
CREATE TRIGGER update_package_files_updated_at BEFORE UPDATE ON public.package_files
    FOR EACH ROW WHEN (old.checksums IS NOT DISTINCT FROM new.checksums AND old.download_count IS NOT DISTINCT FROM new.download_count)
    EXECUTE FUNCTION public.update_updated_at_column();
CREATE TRIGGER update_package_tags_updated_at BEFORE UPDATE ON public.package_tags
    FOR EACH ROW EXECUTE FUNCTION public.update_updated_at_column();

-- Quota: an upload reserves (size, count) in quota_pending; the insert of the
-- file consumes the exact reservation, a delete returns it (see
-- quota_pending_take / quota_pending_add). Only a primary file counts as an
-- artifact; any other role (the go.mod next to a module zip) adds its size
-- with count 0.
CREATE FUNCTION public.quota_pending_package_files() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP IN ('DELETE', 'UPDATE') THEN
        PERFORM quota_pending_add(OLD.repository_id, quota_blob_size(OLD.blob_id), CASE WHEN OLD.role = 'primary' THEN 1 ELSE 0 END);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        PERFORM quota_pending_take(NEW.repository_id, quota_blob_size(NEW.blob_id), CASE WHEN NEW.role = 'primary' THEN 1 ELSE 0 END, false);
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER quota_pending_ins AFTER INSERT ON public.package_files
    FOR EACH ROW EXECUTE FUNCTION public.quota_pending_package_files();
CREATE TRIGGER quota_pending_del AFTER DELETE ON public.package_files
    FOR EACH ROW EXECUTE FUNCTION public.quota_pending_package_files();
CREATE TRIGGER quota_pending_upd AFTER UPDATE OF blob_id ON public.package_files
    FOR EACH ROW WHEN (old.blob_id IS DISTINCT FROM new.blob_id)
    EXECUTE FUNCTION public.quota_pending_package_files();

-- Federation capture: the unit of a federated repository is identified by
-- package_files.unit_key (set on the primary file of the unit) and, for a
-- dist-tag, package_tags.unit_key. federation_units keeps no row image, so
-- capturing needs only the key, the liveness and the content reference (the
-- blob digest).
CREATE FUNCTION public.federation_trg_package_files() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    o jsonb;
    n jsonb;
    v_repo uuid;
BEGIN
    IF TG_OP = 'DELETE' THEN v_repo := OLD.repository_id; ELSE v_repo := NEW.repository_id; END IF;

    IF NOT ((TG_OP <> 'INSERT' AND OLD.unit_key IS NOT NULL) OR (TG_OP <> 'DELETE' AND NEW.unit_key IS NOT NULL))
       OR NOT federation_active(v_repo) THEN
        RETURN NULL;
    END IF;

    IF TG_OP <> 'INSERT' THEN o := to_jsonb(OLD); END IF;
    IF TG_OP <> 'DELETE' THEN n := to_jsonb(NEW); END IF;

    PERFORM federation_capture(v_repo, TG_OP, 'artifact',
        CASE WHEN o IS NULL THEN NULL ELSE OLD.unit_key END,
        CASE WHEN n IS NULL THEN NULL ELSE NEW.unit_key END,
        o, n,
        CASE WHEN o IS NULL THEN NULL ELSE federation_blob_sha(OLD.blob_id) END,
        CASE WHEN n IS NULL THEN NULL ELSE federation_blob_sha(NEW.blob_id) END,
        '{updated_at,deleted_at,deleted_by,download_count}'::text[]);

    RETURN NULL;
END
$$;

CREATE TRIGGER federation_capture AFTER INSERT OR DELETE OR UPDATE ON public.package_files
    FOR EACH ROW EXECUTE FUNCTION public.federation_trg_package_files();

-- A version-level metadata change (an npm deprecation) changes the unit the
-- version's primary file carries.
CREATE FUNCTION public.federation_trg_package_versions() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    f record;
BEGIN
    IF NOT federation_active(NEW.repository_id) THEN
        RETURN NULL;
    END IF;

    SELECT unit_key, blob_id INTO f
      FROM package_files
     WHERE version_id = NEW.id AND unit_key IS NOT NULL AND deleted_at IS NULL
     LIMIT 1;

    IF f.unit_key IS NOT NULL THEN
        PERFORM federation_record(NEW.repository_id, f.unit_key, 'artifact', 'metadata_changed',
                                  federation_blob_sha(f.blob_id), false);
    END IF;

    RETURN NULL;
END
$$;

CREATE TRIGGER federation_capture AFTER UPDATE ON public.package_versions
    FOR EACH ROW WHEN (old.metadata IS DISTINCT FROM new.metadata AND old.deleted_at IS NOT DISTINCT FROM new.deleted_at)
    EXECUTE FUNCTION public.federation_trg_package_versions();

CREATE FUNCTION public.federation_trg_package_tags() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    o jsonb;
    n jsonb;
    v_repo uuid;
BEGIN
    SELECT repository_id INTO v_repo
      FROM packages WHERE id = CASE WHEN TG_OP = 'DELETE' THEN OLD.package_id ELSE NEW.package_id END;

    IF v_repo IS NULL OR NOT federation_active(v_repo)
       OR (CASE WHEN TG_OP = 'DELETE' THEN OLD.unit_key ELSE NEW.unit_key END) IS NULL THEN
        RETURN NULL;
    END IF;

    IF TG_OP <> 'INSERT' THEN o := to_jsonb(OLD); END IF;
    IF TG_OP <> 'DELETE' THEN n := to_jsonb(NEW); END IF;

    PERFORM federation_capture(v_repo, TG_OP, 'ref',
        CASE WHEN o IS NULL THEN NULL ELSE OLD.unit_key END,
        CASE WHEN n IS NULL THEN NULL ELSE NEW.unit_key END,
        o, n,
        o->>'version', n->>'version',
        '{updated_at}'::text[]);

    RETURN NULL;
END
$$;

CREATE TRIGGER federation_capture AFTER INSERT OR DELETE OR UPDATE ON public.package_tags
    FOR EACH ROW EXECUTE FUNCTION public.federation_trg_package_tags();

-- Disaster-recovery journal (internal/recovery/tables.go registers the tables).
CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.packages
    FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.package_versions
    FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.package_files
    FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.package_tags
    FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();

--
-- Format side tables: typed columns a format's metadata generation needs,
-- keyed by (and deleted with) the package_files row they extend. A format
-- without one keeps everything in package_versions.metadata and
-- package_files.attrs.
--

CREATE TABLE public.conan_files (
    id               uuid        PRIMARY KEY REFERENCES public.package_files (id) ON DELETE CASCADE,
    package_id       varchar(64) NOT NULL DEFAULT '',
    package_revision varchar(64) NOT NULL DEFAULT ''
);

COMMENT ON TABLE public.conan_files IS 'conan package coordinates of a package_files row (one row per stored file)';
COMMENT ON COLUMN public.conan_files.package_id IS 'Package id (binary configuration hash); empty for a file of the recipe revision itself';
COMMENT ON COLUMN public.conan_files.package_revision IS 'Package revision (hash of the package manifest); empty for a file of the recipe revision itself';

CREATE TABLE public.deb_files (
    id              uuid  PRIMARY KEY REFERENCES public.package_files(id) ON DELETE CASCADE,
    distribution_id uuid  NOT NULL REFERENCES public.debian_distributions(id) ON DELETE CASCADE,
    component_id    uuid  NOT NULL REFERENCES public.debian_components(id) ON DELETE CASCADE,
    control_fields  jsonb NOT NULL DEFAULT '{}'::jsonb
);

COMMENT ON TABLE public.deb_files IS 'Deb side of package_files: the distribution and component the .deb is published in, and its control stanza';
COMMENT ON COLUMN public.deb_files.control_fields IS 'Packages-stanza map of the .deb (bulk, up to MBs, derived from the file)';

CREATE INDEX idx_deb_files_dist_comp ON public.deb_files (distribution_id, component_id);
CREATE INDEX idx_deb_files_component ON public.deb_files (component_id);

CREATE TABLE public.maven_files (
    id                    uuid    PRIMARY KEY REFERENCES public.package_files (id) ON DELETE CASCADE,
    classifier            text    NOT NULL DEFAULT '',
    extension             text    NOT NULL,
    snapshot_timestamp    text,
    snapshot_build_number integer NOT NULL DEFAULT 0,
    is_snapshot           boolean NOT NULL DEFAULT false,
    md5                   text    NOT NULL DEFAULT '',
    sha1                  text    NOT NULL DEFAULT '',
    sha512                text    NOT NULL DEFAULT ''
);

COMMENT ON TABLE public.maven_files IS 'maven coordinates of a package_files row (one row per stored file)';
COMMENT ON COLUMN public.maven_files.snapshot_timestamp IS 'Snapshot timestamp string (e.g. "20240101.120000"); NULL for release artifacts';

CREATE INDEX idx_maven_files_snapshot ON public.maven_files (snapshot_timestamp DESC NULLS LAST, snapshot_build_number DESC) WHERE is_snapshot;

CREATE TABLE public.pacman_files (
    id               uuid        PRIMARY KEY REFERENCES public.package_files(id) ON DELETE CASCADE,
    file_list        text,
    signature        text        NOT NULL DEFAULT '',
    signature_source varchar(8)  NOT NULL DEFAULT '',
    signature_key    varchar(64) NOT NULL DEFAULT '',
    CONSTRAINT pacman_files_signature_source_check CHECK (signature_source IN ('', 'uploader', 'server'))
);

COMMENT ON TABLE public.pacman_files IS 'Pacman side of package_files: the files-database file list and the detached signature';
COMMENT ON COLUMN public.pacman_files.file_list IS 'Newline-separated file list; NULL = not read from the package yet (never coalesce to an empty string); bulk, derivable from the blob';
COMMENT ON COLUMN public.pacman_files.signature_source IS 'Empty (unsigned), uploader (publisher-uploaded) or server (signed with the repository key)';

CREATE INDEX idx_pacman_files_no_list ON public.pacman_files (id) WHERE file_list IS NULL;

CREATE TABLE public.rpm_files (
    id           uuid   PRIMARY KEY REFERENCES public.package_files(id) ON DELETE CASCADE,
    header_start bigint NOT NULL DEFAULT 0,
    header_end   bigint NOT NULL DEFAULT 0,
    rpm_extra    jsonb  NOT NULL DEFAULT '{}'::jsonb
);

COMMENT ON TABLE public.rpm_files IS 'RPM side of package_files: header byte range and parsed header extras';
COMMENT ON COLUMN public.rpm_files.rpm_extra IS 'Parsed header extras (dependencies, files[], changelogs[], sizes_parsed): KB-MB per row, derived from the file, never re-parse when copying';

-- Nix lookups by NAR file name go through the version metadata.
CREATE INDEX idx_package_versions_nar_file
    ON public.package_versions (repository_id, (metadata ->> 'nar_file'), created_at, id)
    WHERE deleted_at IS NULL AND metadata ? 'nar_file';

-- Journal triggers of the side tables.
CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.conan_files
    FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.deb_files
    FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.maven_files
    FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.pacman_files
    FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();
CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.rpm_files
    FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();

--
-- Seed rows the application relies on.
--

INSERT INTO public.projects (id, name, slug, display_name, description, allowed_repo_types)
VALUES ('00000000-0000-0000-0000-000000000001', 'system', 'system', 'System',
        'Default organization for repositories created before multi-tenancy was introduced', '{hosted}');

INSERT INTO public.permission_targets (name, repository_filter, path_pattern, actions, project_id)
VALUES ('admin-all', '.*', '.*', '{read,write,delete,manage,annotate}', '00000000-0000-0000-0000-000000000001'),
       ('anonymous-read', '.*', '.*', '{read}', '00000000-0000-0000-0000-000000000001');

INSERT INTO public.storage_tiers (name, backend_id, rank) VALUES ('hot', NULL, 0);
INSERT INTO public.blob_replication_state (id) VALUES (1);
INSERT INTO public.recovery_state (id) VALUES (true);
INSERT INTO public.federation_instance DEFAULT VALUES;
