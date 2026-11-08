// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/quarantine"
)

// QuarantineStore owns the quarantine flag of secret-scanned artifacts.
type QuarantineStore struct{ *core }

var _ quarantine.ReasonChecker = (*QuarantineStore)(nil)

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

// ArtifactHoldReason says why the quarantined artifact identified by keys is
// held: quarantine.ReasonLicense when a license result flags it and no
// vulnerability or secret result does, quarantine.ReasonScan otherwise.
func (p *QuarantineStore) ArtifactHoldReason(ctx context.Context, repoID string, keys []string) (quarantine.Reason, error) {
	var scan bool

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
		)`, repoID, keys).Scan(&scan)
	if err != nil {
		return "", fmt.Errorf("check artifact hold reason: %w", err)
	}

	if scan {
		return quarantine.ReasonScan, nil
	}

	return quarantine.ReasonLicense, nil
}

// QuarantinedKeys is IsArtifactQuarantined for many artifacts at once: it
// returns the subset of keys that identify a quarantined artifact of the
// repository, in one query (a listing checks every version it advertises).
func (p *QuarantineStore) QuarantinedKeys(ctx context.Context, repoID string, keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	rows, err := p.db.QueryContext(ctx, `
		SELECT DISTINCT ref.artifact_path
		FROM artifact_refs ref
		JOIN scan_jobs sj ON sj.artifact_ref_id = ref.id
		WHERE ref.repository_id = $1
		  AND ref.artifact_path = ANY($2::text[])
		  AND (
			EXISTS (SELECT 1 FROM vuln_results vr WHERE vr.scan_job_id = sj.id AND vr.quarantined)
			OR EXISTS (SELECT 1 FROM secret_results sr WHERE sr.scan_job_id = sj.id AND sr.quarantined)
			OR EXISTS (SELECT 1 FROM license_results lr WHERE lr.scan_job_id = sj.id AND lr.quarantined)
		  )`, repoID, keys)
	if err != nil {
		return nil, fmt.Errorf("list quarantined keys: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []string

	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("scan quarantined key: %w", err)
		}

		out = append(out, k)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list quarantined keys: %w", err)
	}

	return out, nil
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

// PackageVersionHolds reports the scan holds of the live files of one version of
// a package of a repository whose format uses the generic package tables.
// Scan results hang on artifact_refs, keyed by the path a file is fetched from
// or its id, so a version is matched through its files' id, path and filename.
// pkg matches the package's display name or its identity key, version the
// version or its version key. A version without files, or a repository of a
// format outside the generic tables (docker), has no holds here.
func (p *QuarantineStore) PackageVersionHolds(ctx context.Context, repoID, pkg, version string) (models.VersionHolds, error) {
	var holds models.VersionHolds

	err := p.db.QueryRowContext(ctx, `
		WITH keys AS (
			SELECT k
			FROM packages pk
			JOIN package_versions pv ON pv.package_id = pk.id AND pv.deleted_at IS NULL
			JOIN package_files pf ON pf.version_id = pv.id AND pf.deleted_at IS NULL
			CROSS JOIN LATERAL unnest(ARRAY[pf.id::text, pf.path, '/' || pf.path, pf.filename]) AS k
			WHERE pk.repository_id = $1
			  AND (pk.name = $2 OR pk.name_key = $2)
			  AND (pv.version = $3 OR pv.version_key = $3)
		), jobs AS (
			SELECT sj.id
			FROM artifact_refs ref
			JOIN scan_jobs sj ON sj.artifact_ref_id = ref.id
			WHERE ref.repository_id = $1 AND ref.artifact_path IN (SELECT k FROM keys)
		)
		SELECT
			EXISTS (
				SELECT 1 FROM jobs j
				WHERE EXISTS (SELECT 1 FROM vuln_results vr WHERE vr.scan_job_id = j.id AND vr.quarantined)
				   OR EXISTS (SELECT 1 FROM secret_results sr WHERE sr.scan_job_id = j.id AND sr.quarantined)
			),
			EXISTS (
				SELECT 1 FROM jobs j
				WHERE EXISTS (SELECT 1 FROM license_results lr WHERE lr.scan_job_id = j.id AND lr.quarantined)
			)`, repoID, pkg, version).Scan(&holds.Quarantined, &holds.LicenseBlocked)
	if err != nil {
		return models.VersionHolds{}, fmt.Errorf("check package version holds: %w", err)
	}

	return holds, nil
}
