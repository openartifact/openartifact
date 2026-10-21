-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- 1. Scan failures. A scan whose scanner could not run is recorded as 'failed'
--    and one where only some of several scanners ran as 'partial', with the
--    failing scanners listed in vuln_results.failed_scanners.
ALTER TABLE scan_jobs DROP CONSTRAINT scan_jobs_status_check;
ALTER TABLE scan_jobs ADD CONSTRAINT scan_jobs_status_check
    CHECK (status IN ('pending', 'running', 'completed', 'failed', 'partial'));

ALTER TABLE vuln_results ADD COLUMN failed_scanners text[] DEFAULT '{}'::text[] NOT NULL;

COMMENT ON COLUMN vuln_results.failed_scanners IS 'Scanners that did not produce a result (status failed or partial).';

-- Failed vulnerability scans used to store the raw scanner error (command
-- lines, paths, upstream bodies), which the API returned verbatim. The stored
-- text is replaced by a neutral message; new failures store a sanitized one.
UPDATE scan_jobs
SET error_message = 'scan failed'
WHERE scan_type = 'vuln' AND status = 'failed' AND error_message IS NOT NULL;

-- 2. License policy `block`. A license scan whose licenses violate a blocking
--    policy quarantines the artifact (downloads answer 403) until an admin
--    approves it. quarantine_approved remembers that approval so a re-scan with
--    the same licenses does not quarantine the artifact again.
ALTER TABLE license_results
    ADD COLUMN quarantined boolean DEFAULT false NOT NULL,
    ADD COLUMN quarantine_approved boolean DEFAULT false NOT NULL;

CREATE INDEX idx_license_results_quarantined ON license_results (scan_job_id) WHERE (quarantined = true);

COMMENT ON COLUMN license_results.quarantined IS 'Downloads of the artifact are refused (ARTIFACT_QUARANTINED) while true.';
COMMENT ON COLUMN license_results.quarantine_approved IS 'An admin approved the artifact for the recorded licenses; cleared when they change.';
