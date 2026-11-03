-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Two read-side rollups that keep the repository list and the security views
-- from scanning every package and every scan result per request:
--
-- 1. repo_stats: the per-repository figures of GET /api/v1/repositories
--    (package count, newest publication, scan counts), recomputed in the
--    background for the repositories whose source rows changed. A write marks
--    its repository in repo_stats_dirty (append-only, so writers never wait for
--    each other or for the worker); the reconcile worker claims the marks,
--    recomputes and upserts repo_stats. Both tables are derived and rebuildable,
--    so neither is journaled (internal/recovery NotJournaled).
-- 2. scan_jobs.is_latest: the newest job of each (artifact, scan type), kept by
--    trigger, so "the latest job" is a flag instead of a correlated subquery.
SET LOCAL lock_timeout = '5s';

-- ---------------------------------------------------------------------------
-- scan_jobs.is_latest
-- ---------------------------------------------------------------------------

-- Metadata-only: every existing row starts as the latest job, the backfill below
-- demotes the superseded ones (usually few: a job is added per scanner
-- configuration change).
ALTER TABLE public.scan_jobs ADD COLUMN is_latest boolean DEFAULT true NOT NULL;

COMMENT ON COLUMN public.scan_jobs.is_latest IS 'True for the newest job (created_at DESC, id) of its (artifact_ref_id, scan_type); kept by the scan_jobs_latest trigger.';

-- Demoting a superseded job is not a change of the job: leave updated_at alone.
CREATE OR REPLACE TRIGGER update_scan_jobs_updated_at BEFORE UPDATE ON public.scan_jobs
    FOR EACH ROW WHEN (old.is_latest IS NOT DISTINCT FROM new.is_latest) EXECUTE FUNCTION public.update_updated_at_column();

UPDATE public.scan_jobs sj SET is_latest = false
FROM (
    SELECT id, row_number() OVER (PARTITION BY artifact_ref_id, scan_type ORDER BY created_at DESC, id) AS rn
    FROM public.scan_jobs
    WHERE (artifact_ref_id, scan_type) IN (
        SELECT artifact_ref_id, scan_type FROM public.scan_jobs GROUP BY artifact_ref_id, scan_type HAVING count(*) > 1)
) ranked
WHERE ranked.id = sj.id AND ranked.rn > 1;

-- Re-elects the latest job of the (artifact, scan type) a job was added to or
-- removed from. Jobs only ever gain a newer sibling or lose one, never change
-- created_at, so the pair has at most one stale flag to fix.
CREATE FUNCTION public.scan_jobs_elect_latest() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    ref uuid := COALESCE(NEW.artifact_ref_id, OLD.artifact_ref_id);
    typ text := COALESCE(NEW.scan_type, OLD.scan_type);
BEGIN
    UPDATE public.scan_jobs j SET is_latest = (j.id = w.id)
    FROM (SELECT id FROM public.scan_jobs WHERE artifact_ref_id = ref AND scan_type = typ ORDER BY created_at DESC, id LIMIT 1) w
    WHERE j.artifact_ref_id = ref AND j.scan_type = typ AND j.is_latest IS DISTINCT FROM (j.id = w.id);

    RETURN NULL;
END
$$;

CREATE TRIGGER scan_jobs_latest AFTER INSERT OR DELETE ON public.scan_jobs
    FOR EACH ROW EXECUTE FUNCTION public.scan_jobs_elect_latest();

-- The view keeps only the results whose payload is a JSON array. Without
-- statistics the planner assumes that filter keeps about 0.5% of the rows (99 of
-- 84k), starts every plan at the results table and applies a repository filter
-- last: a summary of one repository then read every result of the instance. The
-- expression statistics give it the real selectivity, so it starts at the
-- repository's artifacts.
CREATE STATISTICS public.vuln_results_vulnerabilities_type ON (jsonb_typeof(vulnerabilities)) FROM public.vuln_results;
CREATE STATISTICS public.secret_results_findings_type ON (jsonb_typeof(findings)) FROM public.secret_results;
ANALYZE public.vuln_results;
ANALYZE public.secret_results;

-- Same columns as before; the vuln branch reads the flag instead of electing
-- the latest job per row.
CREATE OR REPLACE VIEW public.security_results AS
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
      AND sj.is_latest

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

-- ---------------------------------------------------------------------------
-- repo_stats
-- ---------------------------------------------------------------------------

CREATE TABLE public.repo_stats (
    repository_id uuid NOT NULL,
    package_count bigint DEFAULT 0 NOT NULL,
    last_published_at timestamp with time zone,
    vuln_critical bigint DEFAULT 0 NOT NULL,
    vuln_high bigint DEFAULT 0 NOT NULL,
    vuln_medium bigint DEFAULT 0 NOT NULL,
    vuln_low bigint DEFAULT 0 NOT NULL,
    secrets bigint DEFAULT 0 NOT NULL,
    license_violations bigint DEFAULT 0 NOT NULL,
    quarantined bigint DEFAULT 0 NOT NULL,
    scanned_at timestamp with time zone,
    refreshed_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT repo_stats_pkey PRIMARY KEY (repository_id),
    CONSTRAINT repo_stats_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE
);

COMMENT ON TABLE public.repo_stats IS 'Derived per-repository figures of the repository list, recomputed by the repo stats worker from the package, scan and cache tables; a missing row reads as zeros. Not journaled: rebuildable.';
COMMENT ON COLUMN public.repo_stats.refreshed_at IS 'When the row was last recomputed; rows older than the worker''s drift age are recomputed whether or not they were marked.';

-- One row per change of a repository's source rows (see repo_stats_mark). The
-- worker deletes the rows it claims in the transaction that stores the
-- recomputed figures, so a failed pass keeps them. No foreign key: a mark must
-- not lock the repository row, and the mark of a deleted repository is dropped
-- by the worker.
CREATE TABLE public.repo_stats_dirty (
    repository_id uuid NOT NULL,
    marked_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE INDEX idx_repo_stats_dirty_repository ON public.repo_stats_dirty (repository_id);

COMMENT ON TABLE public.repo_stats_dirty IS 'Append-only marks of repositories whose repo_stats row is out of date; claimed (deleted) by the repo stats worker. Not journaled: derived.';

-- Marks a repository. A transaction marks the repository it touched last once
-- (bulk loads would otherwise write a row per file); marking appends instead of
-- updating a shared row, so concurrent writers never wait for each other and a
-- mark committed after the worker claimed the earlier ones stays for the next pass.
CREATE FUNCTION public.repo_stats_mark(p_repository_id uuid) RETURNS void
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF p_repository_id IS NULL OR current_setting('openartifact.repo_stats_marked', true) = p_repository_id::text THEN
        RETURN;
    END IF;

    PERFORM set_config('openartifact.repo_stats_marked', p_repository_id::text, true);
    INSERT INTO public.repo_stats_dirty (repository_id) VALUES (p_repository_id);
END
$$;

-- Triggers of the tables that carry repository_id.
CREATE FUNCTION public.repo_stats_trg_repository() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM public.repo_stats_mark(CASE WHEN TG_OP = 'DELETE' THEN OLD.repository_id ELSE NEW.repository_id END);

    RETURN NULL;
END
$$;

-- repositories itself (a restore from the trash changes what its rows count for).
CREATE FUNCTION public.repo_stats_trg_self() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM public.repo_stats_mark(NEW.id);

    RETURN NULL;
END
$$;

-- scan_jobs: the repository is that of the artifact.
CREATE FUNCTION public.repo_stats_trg_scan_job() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM public.repo_stats_mark((SELECT repository_id FROM public.artifact_refs
        WHERE id = CASE WHEN TG_OP = 'DELETE' THEN OLD.artifact_ref_id ELSE NEW.artifact_ref_id END));

    RETURN NULL;
END
$$;

-- vuln_results, secret_results, license_results: the repository is that of the
-- job's artifact.
CREATE FUNCTION public.repo_stats_trg_scan_result() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM public.repo_stats_mark((SELECT ref.repository_id FROM public.scan_jobs sj
        JOIN public.artifact_refs ref ON ref.id = sj.artifact_ref_id
        WHERE sj.id = CASE WHEN TG_OP = 'DELETE' THEN OLD.scan_job_id ELSE NEW.scan_job_id END));

    RETURN NULL;
END
$$;

-- What the figures derive from. An UPDATE OF list names the columns whose
-- change moves a figure, so a download counter or a cache refresh marks nothing.
CREATE TRIGGER repo_stats_mark AFTER INSERT OR DELETE OR UPDATE OF repository_id, name_key ON public.packages
    FOR EACH ROW EXECUTE FUNCTION public.repo_stats_trg_repository();
CREATE TRIGGER repo_stats_mark AFTER INSERT OR DELETE OR UPDATE OF repository_id, package_id, deleted_at ON public.package_versions
    FOR EACH ROW EXECUTE FUNCTION public.repo_stats_trg_repository();
CREATE TRIGGER repo_stats_mark AFTER INSERT OR DELETE OR UPDATE OF repository_id, deleted_at, listed, created_at ON public.package_files
    FOR EACH ROW EXECUTE FUNCTION public.repo_stats_trg_repository();
CREATE TRIGGER repo_stats_mark AFTER INSERT OR DELETE OR UPDATE OF repository_id, image, deleted_at ON public.docker_tags
    FOR EACH ROW EXECUTE FUNCTION public.repo_stats_trg_repository();
CREATE TRIGGER repo_stats_mark AFTER INSERT OR DELETE OR UPDATE OF repository_id, cache_key, status_code, blob_key, created_at ON public.proxy_cache_entries
    FOR EACH ROW EXECUTE FUNCTION public.repo_stats_trg_repository();
-- A new artifact has no scan yet; the ones that go take their jobs and results along.
CREATE TRIGGER repo_stats_mark AFTER DELETE ON public.artifact_refs
    FOR EACH ROW EXECUTE FUNCTION public.repo_stats_trg_repository();
CREATE TRIGGER repo_stats_mark AFTER INSERT OR DELETE OR UPDATE OF status, completed_at ON public.scan_jobs
    FOR EACH ROW EXECUTE FUNCTION public.repo_stats_trg_scan_job();
CREATE TRIGGER repo_stats_mark AFTER INSERT OR DELETE OR UPDATE ON public.vuln_results
    FOR EACH ROW EXECUTE FUNCTION public.repo_stats_trg_scan_result();
CREATE TRIGGER repo_stats_mark AFTER INSERT OR DELETE OR UPDATE ON public.secret_results
    FOR EACH ROW EXECUTE FUNCTION public.repo_stats_trg_scan_result();
CREATE TRIGGER repo_stats_mark AFTER INSERT OR DELETE OR UPDATE ON public.license_results
    FOR EACH ROW EXECUTE FUNCTION public.repo_stats_trg_scan_result();
CREATE TRIGGER repo_stats_mark AFTER INSERT OR UPDATE OF deleted_at ON public.repositories
    FOR EACH ROW EXECUTE FUNCTION public.repo_stats_trg_self();

-- Every existing repository is computed by the first pass of the worker.
INSERT INTO public.repo_stats_dirty (repository_id) SELECT id FROM public.repositories;
