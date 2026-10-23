// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// nonNilStrings returns s, or an empty slice for nil: the NOT NULL text[]
// columns reject the NULL a nil slice is sent as.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}

// vulnResultColumns is the column list scanVulnResult reads. Queries select it
// FROM vuln_results vr JOIN scan_jobs sj JOIN artifact_refs ref.
const vulnResultColumns = `vr.id, ref.repository_id, ref.artifact_path, vr.package_name, vr.package_version, vr.ecosystem,
	       sj.scanner, sj.status, vr.vulnerabilities, vr.vuln_count, vr.critical_count, vr.high_count, vr.medium_count, vr.low_count,
	       vr.quarantined, sj.error_message, vr.failed_scanners, sj.completed_at, sj.created_at, sj.updated_at`

// rowScanner is satisfied by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanVulnResult reads one row selected with vulnResultColumns.
func scanVulnResult(row rowScanner) (*models.VulnScanResult, error) {
	result := &models.VulnScanResult{}

	var (
		scannedAt sql.NullTime
		errMsg    sql.NullString
		vulnsRaw  []byte
		jobStatus string
	)

	if err := row.Scan(&result.ID, &result.RepositoryID, &result.ArtifactID, &result.PackageName, &result.PackageVersion, &result.Ecosystem,
		&result.Scanner, &jobStatus, &vulnsRaw, &result.VulnCount, &result.CriticalCount, &result.HighCount, &result.MediumCount, &result.LowCount,
		&result.Quarantined, &errMsg, &result.FailedScanners, &scannedAt, &result.CreatedAt, &result.UpdatedAt); err != nil {
		return nil, err //nolint:wrapcheck // callers wrap with their own context and test sql.ErrNoRows
	}

	result.ScanStatus = vulnScanStatusFromJob(jobStatus)
	result.ScanError = errMsg.String

	if scannedAt.Valid {
		result.ScannedAt = &scannedAt.Time
	}

	if len(vulnsRaw) > 0 {
		if err := json.Unmarshal(vulnsRaw, &result.Vulnerabilities); err != nil {
			return nil, fmt.Errorf("failed to unmarshal vulnerabilities: %w", err)
		}
	}

	return result, nil
}

// licenseResultColumns is the column list scanLicenseResult reads. Queries
// select it FROM license_results lr JOIN scan_jobs sj JOIN artifact_refs ref.
const licenseResultColumns = `lr.id, ref.repository_id, ref.artifact_path, lr.licenses, lr.spdx_expressions,
	       sj.status, sj.error_message, lr.quarantined, lr.quarantine_approved, sj.completed_at, sj.created_at, sj.updated_at,
	       lr.unresolved_licenses, lr.license_source`

// scanLicenseResult reads one row selected with licenseResultColumns.
func scanLicenseResult(row rowScanner) (*models.LicenseScanResult, error) {
	result := &models.LicenseScanResult{}

	var (
		scannedAt sql.NullTime
		errMsg    sql.NullString
	)

	if err := row.Scan(&result.ID, &result.RepositoryID, &result.ArtifactID, &result.Licenses, &result.SPDXExpressions,
		&result.ScanStatus, &errMsg, &result.Quarantined, &result.QuarantineApproved, &scannedAt, &result.CreatedAt, &result.UpdatedAt,
		&result.UnresolvedLicenses, &result.LicenseSource); err != nil {
		return nil, err //nolint:wrapcheck // callers wrap with their own context and test sql.ErrNoRows
	}

	result.ScanError = errMsg.String
	if scannedAt.Valid {
		result.ScannedAt = &scannedAt.Time
	}

	return result, nil
}
