// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Vulnerability represents a single vulnerability finding.
type Vulnerability struct {
	ID          string   `json:"id"` // CVE-2021-1234 or GHSA-xxxx
	Summary     string   `json:"summary"`
	Severity    string   `json:"severity"` // CRITICAL, HIGH, MEDIUM, LOW, UNKNOWN
	CVSS        float64  `json:"cvss"`
	FixedIn     string   `json:"fixed_in"` // version that fixes it
	References  []string `json:"references"`
	PublishedAt string   `json:"published_at"`
	// CWEs are the weakness ids ("CWE-79") the scanner reported; empty when it gave none.
	CWEs []string `json:"cwes,omitempty"`
}

// VulnScanResult represents a vulnerability scan result for an artifact.
type VulnScanResult struct {
	ID              string
	RepositoryID    string
	ArtifactID      string
	PackageName     string
	PackageVersion  string
	Ecosystem       string
	Scanner         string // "osv", "trivy", "grype", or several joined by "," (sorted)
	ScanStatus      string // "pending", "scanning", "completed", "partial", "failed"
	Vulnerabilities []Vulnerability
	VulnCount       int
	CriticalCount   int
	HighCount       int
	MediumCount     int
	LowCount        int
	Quarantined     bool
	ScanError       string // sanitized: never contains raw scanner or upstream error text
	FailedScanners  []string
	ScannedAt       *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
