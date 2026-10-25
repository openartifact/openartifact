-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- One row per stored scan result of the four finding types (vulnerability,
-- license, secret, AI analysis), in a common shape. The cross-type security
-- findings list, its summary and the per-package security summary read it
-- (docs/security/findings.md); it replaces nothing and no code writes to it.
--
-- An artifact scanned by several scanner sets has several vuln jobs; only the
-- newest one counts (the rule of GetVulnScanResult).
--
-- A row is a *finding* (is_finding) when the result reports something:
--   vuln     vuln_count > 0
--   license  quarantined by a blocking policy, approved by an admin after such
--            a quarantine, or licenses that are not SPDX expressions
--   secret   finding_count > 0 or quarantined
--   ai       quarantine recommended or license review required
--
-- critical/high/medium/low count what the result holds: the stored vulnerability
-- counts; secret findings by their severity (an unknown one counts as low); for
-- a license a blocking-policy quarantine is one high and every unresolved
-- license one low; for AI a quarantine recommendation is one high and a license
-- review one medium.
--
-- status is quarantined (the result's quarantine flag), suppressed (a vuln
-- result whose every vulnerability has a not_affected or fixed VEX decision, or
-- a license result an admin approved) or open. vex_rank is the least resolved
-- VEX decision on the vulnerabilities of a vuln result: 1 affected,
-- 2 under_investigation, 3 fixed, 4 not_affected, NULL when none applies.
--
-- Only a new view over existing tables: no table is touched. Dropping or
-- retyping a column it reads needs this view redefined first.

SET LOCAL lock_timeout = '10s';

CREATE VIEW security_results AS
SELECT u.kind, u.id, u.repository_id, u.artifact_id, u.package_name::text AS package_name,
       u.package_version::text AS package_version, u.scanner,
       u.scan_status, u.critical, u.high, u.medium, u.low, u.quarantined, u.suppressed, u.vex_rank,
       u.is_finding, u.scanned_at,
       CASE WHEN u.critical > 0 THEN 4 WHEN u.high > 0 THEN 3 WHEN u.medium > 0 THEN 2 WHEN u.low > 0 THEN 1 ELSE 0 END AS severity_rank,
       CASE WHEN u.quarantined THEN 'quarantined' WHEN u.suppressed THEN 'suppressed' ELSE 'open' END AS status
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
           COALESCE(sj.completed_at, sj.updated_at) AS scanned_at
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
           COALESCE(sj.completed_at, sj.updated_at)
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
           COALESCE(sj.completed_at, sj.updated_at)
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
           COALESCE(sj.completed_at, sj.updated_at)
    FROM ai_results air
    JOIN scan_jobs sj ON sj.id = air.scan_job_id
    JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
) u;

COMMENT ON VIEW security_results IS 'Every vuln/license/secret/AI result in one shape (counts, quarantine, VEX state); is_finding marks the ones that report something. Read by the security findings list, summary and per-package summary.';
