// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Sources of the licenses of a LicenseScanResult.
const (
	// LicenseSourceArtifact: read from the package file itself.
	LicenseSourceArtifact = "artifact"
	// LicenseSourceClientDeclared: only what a client declared (license
	// properties); the package file declared no license or cannot be read.
	LicenseSourceClientDeclared = "client_declared"
)

// LicenseScanResult represents a license scan result for an artifact.
type LicenseScanResult struct {
	ID              string
	RepositoryID    string
	ArtifactID      string
	Licenses        []string
	SPDXExpressions []string
	// UnresolvedLicenses are the entries of Licenses that are not SPDX
	// expressions: raw text read from the artifact ("BSD", "BSD License"),
	// kept as written and flagged for review.
	UnresolvedLicenses []string
	// LicenseSource is "artifact" when Licenses include ones read from the
	// package file, "client_declared" when they only come from what a client
	// declared (license properties) because the file declared or allowed none.
	LicenseSource string
	ScanStatus    string // "pending", "completed", "failed"
	ScanError     string
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
