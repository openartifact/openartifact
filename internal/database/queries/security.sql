-- Cross-type security findings (view security_results, migration 084).

-- name: ListSecurityFindings :many
-- One keyset page of findings, worst first: severity rank DESC, scanned_at DESC,
-- id ASC. The caller asks for one row more than the page size. Titles are read
-- for the page rows only.
SELECT p.kind, p.id, p.repository_id, p.repository_name, p.repository_format, p.artifact_id,
       p.package_name, p.package_version, p.scanner, p.scan_status, p.critical, p.high, p.medium, p.low,
       p.quarantined, p.status, p.vex_rank, p.severity_rank, p.scanned_at,
       COALESCE((CASE p.kind
            WHEN 'vuln' THEN (SELECT v ->> 'id'
                              FROM vuln_results vr, jsonb_array_elements(vr.vulnerabilities) AS v
                              WHERE vr.id = p.id
                              ORDER BY CASE upper(v ->> 'severity') WHEN 'CRITICAL' THEN 0 WHEN 'HIGH' THEN 1 WHEN 'MEDIUM' THEN 2 WHEN 'LOW' THEN 3 ELSE 4 END,
                                       COALESCE(NULLIF(v ->> 'cvss', '')::numeric, 0) DESC, v ->> 'id'
                              LIMIT 1)
            WHEN 'license' THEN (SELECT array_to_string(CASE WHEN cardinality(lr.unresolved_licenses) > 0 THEN lr.unresolved_licenses ELSE lr.licenses END, ', ')
                                 FROM license_results lr WHERE lr.id = p.id)
            WHEN 'secret' THEN (SELECT COALESCE(NULLIF(f ->> 'description', ''), f ->> 'type')
                                FROM secret_results sr, jsonb_array_elements(sr.findings) AS f
                                WHERE sr.id = p.id
                                ORDER BY CASE lower(f ->> 'severity') WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 ELSE 3 END, f ->> 'type'
                                LIMIT 1)
            ELSE (SELECT left(split_part(COALESCE(NULLIF(air.security_notes, ''), air.summary), E'\n', 1), 160)
                  FROM ai_results air WHERE air.id = p.id)
        END)::text, ''::text)::text AS title
FROM (
    SELECT sr.kind, sr.id, sr.repository_id, r.name AS repository_name, r.format AS repository_format, sr.artifact_id,
           sr.package_name, sr.package_version, sr.scanner, sr.scan_status, sr.critical, sr.high, sr.medium, sr.low,
           sr.quarantined, sr.status, COALESCE(sr.vex_rank, 0)::int AS vex_rank, sr.severity_rank, sr.scanned_at
    FROM security_results sr
    JOIN repositories r ON r.id = sr.repository_id
    JOIN projects pr ON pr.id = r.project_id
    WHERE sr.is_finding
      AND sr.kind = ANY(@kinds::text[])
      AND (@scope_all::boolean OR r.project_id = ANY(@scope_projects::uuid[]))
      AND (sqlc.narg('repo')::text IS NULL OR r.name = sqlc.narg('repo')::text)
      AND (sqlc.narg('project')::text IS NULL OR pr.name = sqlc.narg('project')::text)
      AND (sqlc.narg('severity_rank')::int IS NULL OR sr.severity_rank = sqlc.narg('severity_rank')::int)
      AND (sqlc.narg('status')::text IS NULL OR sr.status = sqlc.narg('status')::text)
      AND (sqlc.narg('q')::text IS NULL
           OR (sr.package_name || '@' || sr.package_version) ILIKE sqlc.narg('q')::text
           OR r.name ILIKE sqlc.narg('q')::text)
      AND (NOT @has_cursor::boolean
           OR sr.severity_rank < @after_rank::int
           OR (sr.severity_rank = @after_rank::int
               AND (sr.scanned_at < @after_at::timestamptz
                    OR (sr.scanned_at = @after_at::timestamptz AND sr.id > @after_id::uuid))))
    ORDER BY sr.severity_rank DESC, sr.scanned_at DESC, sr.id
    LIMIT @page_limit::int
) p
ORDER BY p.severity_rank DESC, p.scanned_at DESC, p.id;

-- name: SecurityFindingsSummary :many
-- Findings per type: total, by highest severity and quarantined.
SELECT sr.kind,
       count(*)::bigint AS total,
       (count(*) FILTER (WHERE sr.severity_rank = 4))::bigint AS critical,
       (count(*) FILTER (WHERE sr.severity_rank = 3))::bigint AS high,
       (count(*) FILTER (WHERE sr.severity_rank = 2))::bigint AS medium,
       (count(*) FILTER (WHERE sr.severity_rank = 1))::bigint AS low,
       (count(*) FILTER (WHERE sr.quarantined))::bigint AS quarantined
FROM security_results sr
JOIN repositories r ON r.id = sr.repository_id
JOIN projects pr ON pr.id = r.project_id
WHERE sr.is_finding
  AND (@scope_all::boolean OR r.project_id = ANY(@scope_projects::uuid[]))
  AND (sqlc.narg('repo')::text IS NULL OR r.name = sqlc.narg('repo')::text)
  AND (sqlc.narg('project')::text IS NULL OR pr.name = sqlc.narg('project')::text)
GROUP BY sr.kind;

-- name: SecurityScanCoverage :one
-- How many repositories that can hold artifacts (everything but group
-- repositories) of the scope have a completed scan of any finding type, and when
-- the last one finished.
WITH scoped AS (
    SELECT r.id
    FROM repositories r
    JOIN projects pr ON pr.id = r.project_id
    WHERE r.repo_type <> 'group'
      AND (@scope_all::boolean OR r.project_id = ANY(@scope_projects::uuid[]))
      AND (sqlc.narg('repo')::text IS NULL OR r.name = sqlc.narg('repo')::text)
      AND (sqlc.narg('project')::text IS NULL OR pr.name = sqlc.narg('project')::text)
), scanned AS (
    SELECT ref.repository_id, max(COALESCE(sj.completed_at, sj.updated_at)) AS last_at
    FROM artifact_refs ref
    JOIN scan_jobs sj ON sj.artifact_ref_id = ref.id
    WHERE ref.repository_id IN (SELECT id FROM scoped)
      AND sj.scan_type IN ('vuln', 'license', 'secret', 'ai')
      AND sj.status IN ('completed', 'partial')
    GROUP BY ref.repository_id
)
SELECT (SELECT count(*) FROM scoped)::bigint AS total_repositories,
       (SELECT count(*) FROM scanned)::bigint AS scanned_repositories,
       COALESCE((SELECT max(last_at) FROM scanned), 'epoch'::timestamptz)::timestamptz AS last_scan_at;

-- name: SecurityHealthStats :one
-- Instance-wide scan queue and coverage numbers for the security health page.
SELECT (SELECT count(*) FROM scan_jobs
        WHERE scan_type IN ('vuln', 'license', 'secret', 'ai') AND status IN ('pending', 'running'))::bigint AS scans_pending,
       (SELECT count(*) FROM scan_jobs
        WHERE scan_type IN ('vuln', 'license', 'secret', 'ai') AND status = 'failed'
          AND updated_at >= now() - interval '24 hours')::bigint AS scans_failed_24h,
       COALESCE((SELECT max(COALESCE(completed_at, updated_at)) FROM scan_jobs
                 WHERE scan_type IN ('vuln', 'license', 'secret', 'ai') AND status IN ('completed', 'partial')),
                'epoch'::timestamptz)::timestamptz AS last_scan_at,
       (SELECT count(*) FROM repositories r
        WHERE r.repo_type <> 'group'
          AND NOT EXISTS (SELECT 1 FROM artifact_refs ref
                          JOIN scan_jobs sj ON sj.artifact_ref_id = ref.id
                          WHERE ref.repository_id = r.id
                            AND sj.scan_type IN ('vuln', 'license', 'secret', 'ai')
                            AND sj.status IN ('completed', 'partial')))::bigint AS unscanned_repositories;

-- name: LatestFinishedDependencyTrackPush :one
SELECT state, updated_at
FROM dependency_track_pushes
WHERE state IN ('done', 'failed')
ORDER BY updated_at DESC
LIMIT 1;

-- name: RepoSecuritySummary :one
-- Counts only, never finding details: the numbers of one repository, narrowed
-- to a package (and version) when given.
SELECT COALESCE(max(sr.scanned_at) FILTER (WHERE sr.scan_status IN ('completed', 'partial')), 'epoch'::timestamptz)::timestamptz AS scanned_at,
       COALESCE(sum(sr.critical) FILTER (WHERE sr.kind = 'vuln'), 0)::bigint AS vuln_critical,
       COALESCE(sum(sr.high) FILTER (WHERE sr.kind = 'vuln'), 0)::bigint AS vuln_high,
       COALESCE(sum(sr.medium) FILTER (WHERE sr.kind = 'vuln'), 0)::bigint AS vuln_medium,
       COALESCE(sum(sr.low) FILTER (WHERE sr.kind = 'vuln'), 0)::bigint AS vuln_low,
       (count(*) FILTER (WHERE sr.kind = 'license' AND sr.is_finding AND sr.status <> 'suppressed'))::bigint AS license_violations,
       COALESCE(sum(sr.critical + sr.high + sr.medium + sr.low) FILTER (WHERE sr.kind = 'secret'), 0)::bigint AS secret_count,
       (count(*) FILTER (WHERE sr.kind = 'ai' AND sr.high > 0))::bigint AS ai_quarantine_recommended,
       (count(*) FILTER (WHERE sr.kind = 'ai' AND sr.medium > 0))::bigint AS ai_review_required,
       (count(*) FILTER (WHERE sr.quarantined))::bigint AS quarantined
FROM security_results sr
WHERE sr.repository_id = @repository_id::uuid
  AND (sqlc.narg('package_name')::text IS NULL OR sr.package_name = sqlc.narg('package_name')::text)
  AND (sqlc.narg('package_version')::text IS NULL OR sr.package_version = sqlc.narg('package_version')::text);
