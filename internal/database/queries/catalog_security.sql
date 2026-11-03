-- Security and quota aggregates of the repository catalog: the stats of
-- GET /api/v1/repositories (stored in repo_stats, see RepoSecurityCounts) and
-- the per-version columns of the package listing.
--
-- A scan of one artifact can exist more than once per type: scan_jobs is unique
-- on (artifact_ref_id, scan_type, scanner) and the scanner column is the
-- configured scanner list, so changing the configuration adds a job. Only the
-- latest job of each type counts, as in the single-artifact scan reads.
--
-- Counts are raw: VEX statements and ignore rules do not suppress anything here.
-- "Flagged" means a critical or high vulnerability, a secret finding, or a
-- quarantined result of any type; the same rule, as a predicate, is
-- FlaggedArtifactPredicate in internal/database/catalog_sql.go.

-- name: RepoSecurityCounts :many
-- One row per requested repository (all zero for one never scanned): the scan
-- figures the repo stats worker stores in repo_stats. The list endpoint never
-- runs it. Only the latest job of each type counts (scan_jobs.is_latest).
WITH latest AS (
    SELECT ref.repository_id, sj.id AS job_id, sj.scan_type
    FROM artifact_refs ref
    JOIN scan_jobs sj ON sj.artifact_ref_id = ref.id AND sj.is_latest
    WHERE ref.repository_id = ANY(sqlc.arg(repo_ids)::uuid[])
      AND sj.scan_type IN ('vuln', 'secret', 'license')
), vuln_agg AS (
    SELECT l.repository_id,
           SUM(vr.critical_count)::bigint AS critical, SUM(vr.high_count)::bigint AS high,
           SUM(vr.medium_count)::bigint AS medium, SUM(vr.low_count)::bigint AS low,
           COUNT(*) FILTER (WHERE vr.quarantined)::bigint AS quarantined
    FROM latest l
    JOIN vuln_results vr ON vr.scan_job_id = l.job_id
    WHERE l.scan_type = 'vuln'
    GROUP BY l.repository_id
), secret_agg AS (
    SELECT l.repository_id,
           SUM(sr.finding_count)::bigint AS secrets,
           COUNT(*) FILTER (WHERE sr.quarantined)::bigint AS quarantined
    FROM latest l
    JOIN secret_results sr ON sr.scan_job_id = l.job_id
    WHERE l.scan_type = 'secret'
    GROUP BY l.repository_id
), license_agg AS (
    SELECT l.repository_id,
           COUNT(*) FILTER (WHERE lr.quarantined)::bigint AS quarantined
    FROM latest l
    JOIN license_results lr ON lr.scan_job_id = l.job_id
    WHERE l.scan_type = 'license'
    GROUP BY l.repository_id
), scanned AS (
    SELECT ref.repository_id, MAX(sj.completed_at)::timestamptz AS scanned_at
    FROM artifact_refs ref
    JOIN scan_jobs sj ON sj.artifact_ref_id = ref.id
    WHERE ref.repository_id = ANY(sqlc.arg(repo_ids)::uuid[])
      AND sj.scan_type IN ('vuln', 'secret', 'license')
      AND sj.status IN ('completed', 'partial')
      AND sj.completed_at IS NOT NULL
    GROUP BY ref.repository_id
)
SELECT r.id::uuid AS repository_id,
       COALESCE(v.critical, 0)::bigint AS critical,
       COALESCE(v.high, 0)::bigint AS high,
       COALESCE(v.medium, 0)::bigint AS medium,
       COALESCE(v.low, 0)::bigint AS low,
       COALESCE(s.secrets, 0)::bigint AS secrets,
       COALESCE(l.quarantined, 0)::bigint AS license_violations,
       (COALESCE(v.quarantined, 0) + COALESCE(s.quarantined, 0) + COALESCE(l.quarantined, 0))::bigint AS quarantined,
       sc.scanned_at AS scanned_at
FROM repositories r
LEFT JOIN vuln_agg v ON v.repository_id = r.id
LEFT JOIN secret_agg s ON s.repository_id = r.id
LEFT JOIN license_agg l ON l.repository_id = r.id
LEFT JOIN scanned sc ON sc.repository_id = r.id
WHERE r.id = ANY(sqlc.arg(repo_ids)::uuid[]);

-- name: RepoCatalogStats :many
-- What the repository list shows per repository: the stored size and quota (the
-- counters kept up to date on every upload) and the figures of repo_stats (see
-- the repo stats worker; a repository it has not reached yet reads as zeros).
SELECT r.id::uuid AS repository_id,
       COALESCE(q.max_size_bytes, 0)::bigint AS max_size_bytes,
       COALESCE(q.current_size_bytes, 0)::bigint AS current_size_bytes,
       COALESCE(s.package_count, 0)::bigint AS package_count,
       s.last_published_at AS last_published_at,
       COALESCE(s.vuln_critical, 0)::bigint AS critical,
       COALESCE(s.vuln_high, 0)::bigint AS high,
       COALESCE(s.vuln_medium, 0)::bigint AS medium,
       COALESCE(s.vuln_low, 0)::bigint AS low,
       COALESCE(s.secrets, 0)::bigint AS secrets,
       COALESCE(s.license_violations, 0)::bigint AS license_violations,
       COALESCE(s.quarantined, 0)::bigint AS quarantined,
       s.scanned_at AS scanned_at
FROM repositories r
LEFT JOIN storage_quotas q ON q.repository_id = r.id
LEFT JOIN repo_stats s ON s.repository_id = r.id
WHERE r.id = ANY(sqlc.arg(repo_ids)::uuid[]);

-- name: ArtifactSecurityByPath :many
-- The scan state of the artifacts of one repository, by artifact_refs.artifact_path
-- (the package row id). Artifacts never scanned have no row.
SELECT ref.artifact_path::text AS artifact_path,
       COALESCE(v.critical_count, 0)::int AS critical,
       COALESCE(v.high_count, 0)::int AS high,
       COALESCE(v.medium_count, 0)::int AS medium,
       COALESCE(v.low_count, 0)::int AS low,
       COALESCE(s.finding_count, 0)::int AS secrets,
       (COALESCE(v.quarantined, false) OR COALESCE(s.quarantined, false) OR COALESCE(l.quarantined, false))::boolean AS quarantined,
       sc.scanned_at AS scanned_at
FROM artifact_refs ref
LEFT JOIN LATERAL (
    SELECT vr.critical_count, vr.high_count, vr.medium_count, vr.low_count, vr.quarantined
    FROM scan_jobs sj JOIN vuln_results vr ON vr.scan_job_id = sj.id
    WHERE sj.artifact_ref_id = ref.id AND sj.scan_type = 'vuln'
    ORDER BY sj.created_at DESC LIMIT 1
) v ON true
LEFT JOIN LATERAL (
    SELECT sr.finding_count, sr.quarantined
    FROM scan_jobs sj JOIN secret_results sr ON sr.scan_job_id = sj.id
    WHERE sj.artifact_ref_id = ref.id AND sj.scan_type = 'secret'
    ORDER BY sj.created_at DESC LIMIT 1
) s ON true
LEFT JOIN LATERAL (
    SELECT lr.quarantined
    FROM scan_jobs sj JOIN license_results lr ON lr.scan_job_id = sj.id
    WHERE sj.artifact_ref_id = ref.id AND sj.scan_type = 'license'
    ORDER BY sj.created_at DESC LIMIT 1
) l ON true
LEFT JOIN LATERAL (
    SELECT sj.completed_at AS scanned_at
    FROM scan_jobs sj
    WHERE sj.artifact_ref_id = ref.id
      AND sj.scan_type IN ('vuln', 'secret', 'license')
      AND sj.status IN ('completed', 'partial')
      AND sj.completed_at IS NOT NULL
    ORDER BY sj.completed_at DESC LIMIT 1
) sc ON true
WHERE ref.repository_id = sqlc.arg(repo_id)::uuid
  AND ref.artifact_path = ANY(sqlc.arg(artifact_paths)::text[]);
