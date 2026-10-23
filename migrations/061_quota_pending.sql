-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Exact storage-quota usage while uploads and deletes are in flight.
--
-- storage_quotas counters move in two steps that cannot share one transaction:
-- an upload reserves quota, stores its blob and only then inserts the package
-- row; a delete removes the row and only then returns the quota. A recount
-- from the package rows during either gap misreported the counter by that
-- operation until the next reconcile.
--
-- quota_pending records the gap. A reservation adds an entry (kind 0) in the
-- same transaction as the counter change. A package row insert consumes the
-- matching entry and a package row delete adds one (kind 1), both in the
-- transaction of the row change, via the triggers below; returning a
-- reservation or a deleted row's usage consumes the entry together with the
-- counter change. The reconcile counts the rows plus the entries that are
-- still fresh, so a recount is exact at every instant. Entries that never
-- found their partner (a crashed process) expire: kind 0 after one hour, kind
-- 1 after two minutes.

SET LOCAL lock_timeout = '10s';

CREATE TABLE quota_pending (
    id             bigserial PRIMARY KEY,
    repository_id  uuid        NOT NULL,
    size_bytes     bigint      NOT NULL,
    artifact_count integer     NOT NULL,
    kind           smallint    NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE quota_pending IS 'Quota usage already in storage_quotas counters but not (yet) backed by package rows, or the reverse; see migration 061';

-- quota_pending_add records usage that the counters still carry although the
-- package row is gone (kind 1). Zero amounts and unknown repositories are skipped.
CREATE FUNCTION quota_pending_add(p_repo uuid, p_size bigint, p_count integer)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    IF p_repo IS NULL OR (COALESCE(p_size, 0) <= 0 AND COALESCE(p_count, 0) <= 0) THEN
        RETURN;
    END IF;

    INSERT INTO quota_pending (repository_id, size_bytes, artifact_count, kind)
    VALUES (p_repo, COALESCE(p_size, 0), COALESCE(p_count, 0), 1);
END
$$;

-- quota_pending_take consumes the entry that matches (p_size, p_count) exactly.
-- With p_greedy, and no exact match, it consumes the oldest entries that fit
-- inside the amounts instead (a batch release that returns many rows at once).
CREATE FUNCTION quota_pending_take(p_repo uuid, p_size bigint, p_count integer, p_greedy boolean)
RETURNS void LANGUAGE plpgsql AS $$
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

CREATE FUNCTION quota_blob_size(p_blob uuid) RETURNS bigint
LANGUAGE sql STABLE AS $$
    SELECT COALESCE((SELECT size_bytes FROM blobs WHERE id = p_blob), 0)
$$;

-- One trigger function per package table, following the rows the reconcile
-- counts (quotaUsageRowsSQL in internal/database/postgres_quota.go): the
-- repository, the blob and the artifact count n of a row.
DO $$
DECLARE
    spec record;
BEGIN
    FOR spec IN
        SELECT * FROM (VALUES
            ('deb_packages',      'repository_id', 'blob_id', 1),
            ('rpm_packages',      'repository_id', 'blob_id', 1),
            ('maven_artifacts',   'repository_id', 'blob_id', 1),
            ('helm_charts',       'repository_id', 'blob_id', 1),
            ('rubygems_gems',     'repository_id', 'blob_id', 1),
            ('pypi_packages',     'repository_id', 'blob_id', 1),
            ('cargo_crates',      'repository_id', 'blob_id', 1),
            ('nuget_packages',    'repository_id', 'blob_id', 1),
            ('conda_packages',    'repository_id', 'blob_id', 1),
            ('conan_packages',    'repository_id', 'blob_id', 1),
            ('ivy_artifacts',     'repository_id', 'blob_id', 1),
            ('sbt_artifacts',     'repository_id', 'blob_id', 1),
            ('raw_artifacts',     'repository_id', 'blob_id', 1),
            ('docker_manifests',  'repository_id', 'blob_id', 1),
            ('docker_blobs',      'repository_id', 'blob_id', 0)
        ) AS t(tbl, repo_col, blob_col, n)
    LOOP
        EXECUTE format($f$
            CREATE FUNCTION %1$I() RETURNS trigger LANGUAGE plpgsql AS $body$
            BEGIN
                IF TG_OP IN ('DELETE', 'UPDATE') THEN
                    PERFORM quota_pending_add(OLD.%2$I, quota_blob_size(OLD.%3$I), %4$s);
                END IF;
                IF TG_OP IN ('INSERT', 'UPDATE') THEN
                    PERFORM quota_pending_take(NEW.%2$I, quota_blob_size(NEW.%3$I), %4$s, false);
                END IF;
                RETURN NULL;
            END
            $body$
        $f$, 'quota_pending_' || spec.tbl, spec.repo_col, spec.blob_col, spec.n);

        EXECUTE format('CREATE TRIGGER quota_pending_ins AFTER INSERT ON %1$I FOR EACH ROW EXECUTE FUNCTION %2$I()',
                       spec.tbl, 'quota_pending_' || spec.tbl);
        EXECUTE format('CREATE TRIGGER quota_pending_del AFTER DELETE ON %1$I FOR EACH ROW EXECUTE FUNCTION %2$I()',
                       spec.tbl, 'quota_pending_' || spec.tbl);
        EXECUTE format('CREATE TRIGGER quota_pending_upd AFTER UPDATE OF %3$I ON %1$I FOR EACH ROW WHEN (OLD.%3$I IS DISTINCT FROM NEW.%3$I) EXECUTE FUNCTION %2$I()',
                       spec.tbl, 'quota_pending_' || spec.tbl, spec.blob_col);
    END LOOP;
END
$$;

-- npm versions reach their repository through the package row.
CREATE FUNCTION quota_pending_npm_versions() RETURNS trigger LANGUAGE plpgsql AS $body$
BEGIN
    IF TG_OP IN ('DELETE', 'UPDATE') THEN
        PERFORM quota_pending_add((SELECT repository_id FROM npm_packages WHERE id = OLD.package_id), quota_blob_size(OLD.blob_id), 1);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        PERFORM quota_pending_take((SELECT repository_id FROM npm_packages WHERE id = NEW.package_id), quota_blob_size(NEW.blob_id), 1, false);
    END IF;
    RETURN NULL;
END
$body$;

CREATE TRIGGER quota_pending_ins AFTER INSERT ON npm_versions FOR EACH ROW EXECUTE FUNCTION quota_pending_npm_versions();
CREATE TRIGGER quota_pending_del AFTER DELETE ON npm_versions FOR EACH ROW EXECUTE FUNCTION quota_pending_npm_versions();
CREATE TRIGGER quota_pending_upd AFTER UPDATE OF blob_id ON npm_versions FOR EACH ROW
    WHEN (OLD.blob_id IS DISTINCT FROM NEW.blob_id) EXECUTE FUNCTION quota_pending_npm_versions();

-- A Go module is one artifact holding two blobs (zip and go.mod), reserved as
-- one amount.
CREATE FUNCTION quota_pending_gomod_modules() RETURNS trigger LANGUAGE plpgsql AS $body$
BEGIN
    IF TG_OP IN ('DELETE', 'UPDATE') THEN
        PERFORM quota_pending_add(OLD.repository_id, quota_blob_size(OLD.zip_blob_id) + quota_blob_size(OLD.mod_blob_id), 1);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        PERFORM quota_pending_take(NEW.repository_id, quota_blob_size(NEW.zip_blob_id) + quota_blob_size(NEW.mod_blob_id), 1, false);
    END IF;
    RETURN NULL;
END
$body$;

CREATE TRIGGER quota_pending_ins AFTER INSERT ON gomod_modules FOR EACH ROW EXECUTE FUNCTION quota_pending_gomod_modules();
CREATE TRIGGER quota_pending_del AFTER DELETE ON gomod_modules FOR EACH ROW EXECUTE FUNCTION quota_pending_gomod_modules();
CREATE TRIGGER quota_pending_upd AFTER UPDATE OF zip_blob_id, mod_blob_id ON gomod_modules FOR EACH ROW
    WHEN (OLD.zip_blob_id IS DISTINCT FROM NEW.zip_blob_id OR OLD.mod_blob_id IS DISTINCT FROM NEW.mod_blob_id)
    EXECUTE FUNCTION quota_pending_gomod_modules();
