// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
)

// IsArtifactQuarantined reports whether any of keys (artifact_refs.artifact_path
// values) identifies an artifact of the repository whose vulnerability or secret
// scan result is flagged quarantined.
func (p *PostgresDB) IsArtifactQuarantined(ctx context.Context, repoID string, keys []string) (bool, error) {
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
			  )
		)`, repoID, keys).Scan(&quarantined)
	if err != nil {
		return false, fmt.Errorf("check artifact quarantine: %w", err)
	}

	return quarantined, nil
}
