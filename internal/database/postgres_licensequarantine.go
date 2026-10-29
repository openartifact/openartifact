// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
)

// ErrLicenseScanNotFound is returned when a license scan result does not exist in the repository.
var ErrLicenseScanNotFound = errors.New("license scan result not found")

// SetLicenseScanQuarantine quarantines (true) or approves (false) the artifact
// of exactly one license scan result (by ID, scoped to repoID). Approving
// records the decision for the licenses currently stored, so a later scan that
// finds the same licenses does not quarantine the artifact again; quarantining
// clears it. Returns ErrLicenseScanNotFound when nothing matched.
func (p *PostgresDB) SetLicenseScanQuarantine(ctx context.Context, repoID, id string, quarantined bool) error {
	return p.execMatching(ctx, "set license scan quarantine", ErrLicenseScanNotFound, `
		UPDATE license_results lr SET quarantined = $3, quarantine_approved = NOT $3
		FROM scan_jobs sj, artifact_refs ref
		WHERE lr.id = $2 AND sj.id = lr.scan_job_id AND sj.scan_type = 'license'
		  AND ref.id = sj.artifact_ref_id AND ref.repository_id = $1`, repoID, id, quarantined)
}
