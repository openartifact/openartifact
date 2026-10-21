// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// LicenseScanResult represents a license scan result for an artifact.
type LicenseScanResult struct {
	ID              string
	RepositoryID    string
	ArtifactID      string
	Licenses        []string
	SPDXExpressions []string
	ScanStatus      string // "pending", "completed", "failed"
	ScanError       string
	// Quarantined makes downloads of the artifact answer 403 until approved.
	Quarantined bool
	// QuarantineApproved records that an admin approved the artifact for
	// Licenses; it is cleared when a re-scan finds different licenses.
	QuarantineApproved bool
	ScannedAt          *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// LicensePolicy represents a license compliance policy.
type LicensePolicy struct {
	ID              string
	RepositoryID    *string // nil = global policy
	Name            string
	Description     string
	Action          string // "warn", "block", "allow"
	AllowedLicenses []string
	DeniedLicenses  []string
	Enabled         bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
