-- name: CreateSBOMReport :one
WITH ref AS (
    INSERT INTO artifact_refs (repository_id, artifact_path, format)
    VALUES ($1, $2, $3)
    ON CONFLICT (repository_id, artifact_path) DO UPDATE SET format = EXCLUDED.format
    RETURNING id
), job AS (
    INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, completed_at)
    SELECT ref.id, 'sbom', '', 'completed', $4 FROM ref
    ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET
        status = EXCLUDED.status, completed_at = EXCLUDED.completed_at, updated_at = now()
    RETURNING id, created_at, updated_at
), ins AS (
    INSERT INTO sbom_results (scan_job_id, sbom_format, spec_version, blob_id, component_count)
    SELECT job.id, $3, $5, $7, $6 FROM job
    ON CONFLICT (scan_job_id, sbom_format, spec_version) DO UPDATE SET
        component_count = EXCLUDED.component_count, blob_id = EXCLUDED.blob_id
    RETURNING id, scan_job_id
)
SELECT ins.id, job.created_at, job.updated_at FROM ins JOIN job ON job.id = ins.scan_job_id;

-- name: ListSBOMReports :many
SELECT sr.id, ref.repository_id, ref.artifact_path, sr.sbom_format, sr.spec_version,
       sr.component_count, b.sha256 AS blob_sha256, sj.completed_at, sj.created_at, sj.updated_at
FROM sbom_results sr
JOIN scan_jobs sj ON sj.id = sr.scan_job_id
JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
LEFT JOIN blobs b ON b.id = sr.blob_id
WHERE sj.scan_type = 'sbom' AND ref.repository_id = $1 ORDER BY sj.created_at DESC;

-- name: CreateLicenseScanResult :one
WITH ref AS (
    INSERT INTO artifact_refs (repository_id, artifact_path, format)
    VALUES ($1, $2, COALESCE((SELECT format FROM repositories WHERE id = $1), 'unknown'))
    ON CONFLICT (repository_id, artifact_path) DO UPDATE SET artifact_path = EXCLUDED.artifact_path
    RETURNING id
), job AS (
    INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, error_message, completed_at)
    SELECT ref.id, 'license', '', $3, NULLIF($4, ''), $5 FROM ref
    ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET
        status = EXCLUDED.status, error_message = EXCLUDED.error_message,
        completed_at = EXCLUDED.completed_at, updated_at = now()
    RETURNING id
)
INSERT INTO license_results (scan_job_id, licenses, spdx_expressions)
SELECT job.id, $6, $7 FROM job
ON CONFLICT (scan_job_id) DO UPDATE SET
    licenses = EXCLUDED.licenses, spdx_expressions = EXCLUDED.spdx_expressions
RETURNING id;

-- name: GetLicenseScanResult :one
SELECT lr.id, ref.repository_id, ref.artifact_path, lr.licenses, lr.spdx_expressions,
       sj.status, sj.error_message, sj.completed_at, sj.created_at, sj.updated_at
FROM license_results lr
JOIN scan_jobs sj ON sj.id = lr.scan_job_id
JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
WHERE sj.scan_type = 'license' AND ref.repository_id = $1 AND ref.artifact_path = $2;

-- name: ListLicenseScanResults :many
SELECT lr.id, ref.repository_id, ref.artifact_path, lr.licenses, lr.spdx_expressions,
       sj.status, sj.error_message, sj.completed_at, sj.created_at, sj.updated_at
FROM license_results lr
JOIN scan_jobs sj ON sj.id = lr.scan_job_id
JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
WHERE sj.scan_type = 'license' AND ref.repository_id = $1 ORDER BY sj.created_at DESC;

-- name: CreateLicensePolicy :one
INSERT INTO license_policies (repository_id, name, description, action, allowed_licenses, denied_licenses, enabled, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id;

-- name: GetLicensePolicy :one
SELECT id, repository_id, name, description, action, allowed_licenses, denied_licenses, enabled, created_at, updated_at
FROM license_policies WHERE id = $1;

-- name: UpdateLicensePolicy :exec
UPDATE license_policies
SET name = $1, description = $2, action = $3, allowed_licenses = $4, denied_licenses = $5, enabled = $6, updated_at = $7
WHERE id = $8;

-- name: DeleteLicensePolicy :execrows
DELETE FROM license_policies WHERE id = $1;

-- name: ListLicensePolicies :many
SELECT id, repository_id, name, description, action, allowed_licenses, denied_licenses, enabled, created_at, updated_at
FROM license_policies WHERE repository_id = $1 OR repository_id IS NULL ORDER BY created_at DESC;

-- name: CreateSecretScanResult :one
WITH ref AS (
    INSERT INTO artifact_refs (repository_id, artifact_path, format)
    VALUES ($1, $2, COALESCE((SELECT format FROM repositories WHERE id = $1), 'unknown'))
    ON CONFLICT (repository_id, artifact_path) DO UPDATE SET artifact_path = EXCLUDED.artifact_path
    RETURNING id
), job AS (
    INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, error_message, completed_at)
    SELECT ref.id, 'secret', '', $3, NULLIF($4, ''), $5 FROM ref
    ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET
        status = EXCLUDED.status, error_message = EXCLUDED.error_message,
        completed_at = EXCLUDED.completed_at, updated_at = now()
    RETURNING id
)
INSERT INTO secret_results (scan_job_id, findings, finding_count, quarantined)
SELECT job.id, $6, $7, $8 FROM job
ON CONFLICT (scan_job_id) DO UPDATE SET
    findings = EXCLUDED.findings, finding_count = EXCLUDED.finding_count, quarantined = EXCLUDED.quarantined
RETURNING id;

-- name: GetSecretScanResult :one
SELECT sr.id, ref.repository_id, ref.artifact_path, sj.status, sr.findings, sr.finding_count, sr.quarantined,
       sj.error_message, sj.completed_at, sj.created_at, sj.updated_at
FROM secret_results sr
JOIN scan_jobs sj ON sj.id = sr.scan_job_id
JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
WHERE sj.scan_type = 'secret' AND ref.repository_id = $1 AND ref.artifact_path = $2;

-- name: ListSecretScanResults :many
SELECT sr.id, ref.repository_id, ref.artifact_path, sj.status, sr.findings, sr.finding_count, sr.quarantined,
       sj.error_message, sj.completed_at, sj.created_at, sj.updated_at
FROM secret_results sr
JOIN scan_jobs sj ON sj.id = sr.scan_job_id
JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
WHERE sj.scan_type = 'secret' AND ref.repository_id = $1 ORDER BY sj.created_at DESC;

-- name: QuarantineArtifact :exec
WITH target AS (
    SELECT sr.id AS result_id, sj.id AS job_id
    FROM secret_results sr
    JOIN scan_jobs sj ON sj.id = sr.scan_job_id
    JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
    WHERE sj.scan_type = 'secret' AND ref.repository_id = $3 AND ref.artifact_path = $4
), upd1 AS (
    UPDATE secret_results SET quarantined = $1
    WHERE id = (SELECT result_id FROM target)
    RETURNING id
)
UPDATE scan_jobs SET updated_at = $2 WHERE id = (SELECT job_id FROM target);

-- name: CreateVulnScanResult :one
WITH ref AS (
    INSERT INTO artifact_refs (repository_id, artifact_path, format)
    VALUES ($1, $2, COALESCE((SELECT format FROM repositories WHERE id = $1), 'unknown'))
    ON CONFLICT (repository_id, artifact_path) DO UPDATE SET artifact_path = EXCLUDED.artifact_path
    RETURNING id
), job AS (
    INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, error_message, completed_at)
    SELECT ref.id, 'vuln', $3, $4, NULLIF($5, ''), $6 FROM ref
    ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET
        status = EXCLUDED.status, error_message = EXCLUDED.error_message,
        completed_at = EXCLUDED.completed_at, updated_at = now()
    RETURNING id
)
INSERT INTO vuln_results (scan_job_id, vulnerabilities, vuln_count, critical_count, high_count, medium_count, low_count, quarantined, package_name, package_version, ecosystem)
SELECT job.id, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16 FROM job
ON CONFLICT (scan_job_id) DO UPDATE SET
    vulnerabilities = EXCLUDED.vulnerabilities, vuln_count = EXCLUDED.vuln_count,
    critical_count = EXCLUDED.critical_count, high_count = EXCLUDED.high_count,
    medium_count = EXCLUDED.medium_count, low_count = EXCLUDED.low_count,
    quarantined = EXCLUDED.quarantined, package_name = EXCLUDED.package_name,
    package_version = EXCLUDED.package_version, ecosystem = EXCLUDED.ecosystem
RETURNING id;

-- name: GetVulnScanResult :one
SELECT vr.id, ref.repository_id, ref.artifact_path, vr.package_name, vr.package_version, vr.ecosystem,
       sj.scanner, sj.status, vr.vulnerabilities, vr.vuln_count, vr.critical_count, vr.high_count, vr.medium_count, vr.low_count,
       vr.quarantined, sj.error_message, sj.completed_at, sj.created_at, sj.updated_at
FROM vuln_results vr
JOIN scan_jobs sj ON sj.id = vr.scan_job_id
JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
WHERE sj.scan_type = 'vuln' AND ref.repository_id = $1 AND ref.artifact_path = $2
ORDER BY sj.created_at DESC
LIMIT 1;

-- name: UpdateVulnScanResult :exec
WITH res AS (
    UPDATE vuln_results AS vr SET vulnerabilities = $1, vuln_count = $2, critical_count = $3,
           high_count = $4, medium_count = $5, low_count = $6, quarantined = $7
    WHERE vr.id = $11
    RETURNING scan_job_id
)
UPDATE scan_jobs sj SET scanner = $8, status = $9, error_message = NULLIF($10, ''), completed_at = $12, updated_at = now()
FROM res WHERE sj.id = res.scan_job_id;

-- name: ListVulnScanResults :many
SELECT vr.id, ref.repository_id, ref.artifact_path, vr.package_name, vr.package_version, vr.ecosystem,
       sj.scanner, sj.status, vr.vulnerabilities, vr.vuln_count, vr.critical_count, vr.high_count, vr.medium_count, vr.low_count,
       vr.quarantined, sj.error_message, sj.completed_at, sj.created_at, sj.updated_at
FROM vuln_results vr
JOIN scan_jobs sj ON sj.id = vr.scan_job_id
JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
WHERE sj.scan_type = 'vuln' AND ref.repository_id = $1 ORDER BY sj.created_at DESC;

-- name: QuarantineVulnArtifact :exec
WITH target AS (
    SELECT vr.id AS result_id, sj.id AS job_id
    FROM vuln_results vr
    JOIN scan_jobs sj ON sj.id = vr.scan_job_id
    JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
    WHERE sj.scan_type = 'vuln' AND ref.repository_id = $3 AND ref.artifact_path = $4
), upd1 AS (
    UPDATE vuln_results SET quarantined = $1
    WHERE id = (SELECT result_id FROM target)
    RETURNING id
)
UPDATE scan_jobs SET updated_at = $2 WHERE id = (SELECT job_id FROM target);
