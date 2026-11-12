// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// The batch availability check (api.ExplainAvailabilityBatch) asks about many
// package versions across the hosted members of a repository or group at once.
// A package and a version are matched like PackageVersionHolds does: the given
// name against packages.name or name_key, the given version against
// package_versions.version or version_key. A version counts as stored when it
// is live and has a live file.

// HostedNamesHeld returns, for the names of a batch lookup, which of the hosted
// repositories repoIDs hold a stored version of the package: the "hosted wins"
// ownership of a group. Index is the position in names. One query for every
// repository and name.
//
// This must agree with serving, which decides ownership differently: a hosted
// member holds a name when it answers the format's package document with 200
// (internal/group/hostedwins.go, PkgRef). A member with a live version that has
// a live file answers that document, and one without does not, so the two
// match today. A change to either side (what a hosted handler serves for a
// package with no live file, or this predicate) must keep them equal, or the
// batch availability answer stops following the group.
func (p *QuarantineStore) HostedNamesHeld(ctx context.Context, repoIDs, names []string) ([]models.HeldName, error) {
	if len(repoIDs) == 0 || len(names) == 0 {
		return nil, nil
	}

	rows, err := p.db.QueryContext(ctx, `
		SELECT DISTINCT q.idx - 1, pk.repository_id::text
		FROM unnest($2::text[]) WITH ORDINALITY AS q(name, idx)
		JOIN packages pk ON pk.repository_id = ANY($1::uuid[]) AND (pk.name = q.name OR pk.name_key = q.name)
		WHERE EXISTS (
			SELECT 1 FROM package_versions pv
			JOIN package_files pf ON pf.version_id = pv.id AND pf.deleted_at IS NULL
			WHERE pv.package_id = pk.id AND pv.deleted_at IS NULL
		)`, repoIDs, names)
	if err != nil {
		return nil, fmt.Errorf("list hosted names held: %w", err)
	}
	defer rows.Close()

	var out []models.HeldName

	for rows.Next() {
		var h models.HeldName
		if err := rows.Scan(&h.Index, &h.RepoID); err != nil {
			return nil, fmt.Errorf("scan hosted name held: %w", err)
		}

		out = append(out, h)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list hosted names held: %w", err)
	}

	return out, nil
}

// HostedVersionsStored returns the stored versions among the package versions
// of a batch lookup in the hosted repositories repoIDs, each with the
// quarantine and license-policy holds PackageVersionHolds reports for it.
// Index is the position in versions. One query for every repository and
// version.
func (p *QuarantineStore) HostedVersionsStored(
	ctx context.Context, repoIDs []string, versions []models.NameVersion,
) ([]models.StoredVersion, error) {
	if len(repoIDs) == 0 || len(versions) == 0 {
		return nil, nil
	}

	names := make([]string, len(versions))
	vers := make([]string, len(versions))

	for i, v := range versions {
		names[i], vers[i] = v.Name, v.Version
	}

	rows, err := p.db.QueryContext(ctx, `
		WITH q AS (
			SELECT q.name, q.version, q.idx FROM unnest($2::text[], $3::text[]) WITH ORDINALITY AS q(name, version, idx)
		), stored AS (
			SELECT q.idx, pk.repository_id, array_agg(pv.id) AS version_ids
			FROM q
			JOIN packages pk ON pk.repository_id = ANY($1::uuid[]) AND (pk.name = q.name OR pk.name_key = q.name)
			JOIN package_versions pv ON pv.package_id = pk.id AND pv.deleted_at IS NULL
				AND (pv.version = q.version OR pv.version_key = q.version)
			WHERE EXISTS (SELECT 1 FROM package_files pf WHERE pf.version_id = pv.id AND pf.deleted_at IS NULL)
			GROUP BY q.idx, pk.repository_id
		), keyed AS (
			SELECT s.idx, s.repository_id, k
			FROM stored s
			JOIN package_files pf ON pf.version_id = ANY(s.version_ids) AND pf.deleted_at IS NULL
			CROSS JOIN LATERAL unnest(ARRAY[pf.id::text, pf.path, '/' || pf.path, pf.filename]) AS k
		), jobs AS (
			SELECT DISTINCT kd.idx, kd.repository_id, sj.id
			FROM keyed kd
			JOIN artifact_refs ref ON ref.repository_id = kd.repository_id AND ref.artifact_path = kd.k
			JOIN scan_jobs sj ON sj.artifact_ref_id = ref.id
		)
		SELECT s.idx - 1, s.repository_id::text,
			EXISTS (
				SELECT 1 FROM jobs j
				WHERE j.idx = s.idx AND j.repository_id = s.repository_id
				  AND (EXISTS (SELECT 1 FROM vuln_results vr WHERE vr.scan_job_id = j.id AND vr.quarantined)
				    OR EXISTS (SELECT 1 FROM secret_results sr WHERE sr.scan_job_id = j.id AND sr.quarantined))
			),
			EXISTS (
				SELECT 1 FROM jobs j
				WHERE j.idx = s.idx AND j.repository_id = s.repository_id
				  AND EXISTS (SELECT 1 FROM license_results lr WHERE lr.scan_job_id = j.id AND lr.quarantined)
			)
		FROM stored s`, repoIDs, names, vers)
	if err != nil {
		return nil, fmt.Errorf("list hosted versions stored: %w", err)
	}
	defer rows.Close()

	var out []models.StoredVersion

	for rows.Next() {
		var v models.StoredVersion
		if err := rows.Scan(&v.Index, &v.RepoID, &v.Quarantined, &v.LicenseBlocked); err != nil {
			return nil, fmt.Errorf("scan hosted version stored: %w", err)
		}

		out = append(out, v)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list hosted versions stored: %w", err)
	}

	return out, nil
}
