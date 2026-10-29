// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
)

// QuarantineStore owns the quarantine flag of secret-scanned artifacts.
type QuarantineStore struct{ *core }

// IsArtifactQuarantined reports whether any of keys (artifact_refs.artifact_path
// values) identifies an artifact of the repository whose vulnerability, secret
// or license scan result is flagged quarantined.
func (p *QuarantineStore) IsArtifactQuarantined(ctx context.Context, repoID string, keys []string) (bool, error) {
	if len(keys) == 0 {
		return false, nil
	}

	var quarantined bool

	err := p.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM artifact_refs ref
			JOIN scan_jobs sj ON sj.artifact_ref_id = ref.id
			WHERE ref.repository_id = $1
			  AND ref.artifact_path = ANY($2::text[])
			  AND (
				EXISTS (SELECT 1 FROM vuln_results vr WHERE vr.scan_job_id = sj.id AND vr.quarantined)
				OR EXISTS (SELECT 1 FROM secret_results sr WHERE sr.scan_job_id = sj.id AND sr.quarantined)
				OR EXISTS (SELECT 1 FROM license_results lr WHERE lr.scan_job_id = sj.id AND lr.quarantined)
			  )
		)`, repoID, keys).Scan(&quarantined)
	if err != nil {
		return false, fmt.Errorf("check artifact quarantine: %w", err)
	}

	return quarantined, nil
}

// QuarantineArtifact updates the quarantine status of an artifact.
func (p *QuarantineStore) QuarantineArtifact(ctx context.Context, repoID, artifactID string, quarantined bool) error {
	_, err := p.db.ExecContext(ctx, `
		WITH target AS (
			SELECT sr.id AS result_id, sj.id AS job_id
			FROM secret_results sr
			JOIN scan_jobs sj ON sj.id = sr.scan_job_id
			JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
			WHERE sj.scan_type = 'secret' AND ref.repository_id = $2 AND ref.artifact_path = $3
		), upd1 AS (
			UPDATE secret_results SET quarantined = $1
			WHERE id = (SELECT result_id FROM target)
			RETURNING id
		)
		UPDATE scan_jobs SET updated_at = now() WHERE id = (SELECT job_id FROM target)`,
		quarantined, repoID, artifactID,
	)
	if err != nil {
		return fmt.Errorf("failed to quarantine artifact: %w", err)
	}

	return nil
}
