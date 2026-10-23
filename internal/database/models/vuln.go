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

// VulnScanCursor is a keyset position in the overview order (critical_count
// DESC, high_count DESC, medium_count DESC, low_count DESC, SortAt DESC, id ASC).
type VulnScanCursor struct {
	CriticalCount int
	HighCount     int
	MediumCount   int
	LowCount      int
	// SortAt is the scan's completion time, or its creation time while it has
	// none.
	SortAt time.Time
	ID     string
}

// VulnScanListParams selects a page of the cross-repository scan overview.
// Empty filters match everything.
type VulnScanListParams struct {
	// Repo is a repository name.
	Repo string
	// Severity keeps scans with at least one finding of that severity or worse:
	// critical, high, medium or low.
	Severity string
	// Status is completed, partial, failed or scanning.
	Status string
	// Scanner is osv, trivy or grype; a scan run by several matches each.
	Scanner string
	// Quarantined, when set, keeps scans whose quarantine flag equals it.
	Quarantined *bool
	Limit       int
	// After, when set, returns only scans strictly after this position.
	After *VulnScanCursor
}

// VulnScanOverviewRow is a scan result without its findings, with the
// repository it belongs to.
type VulnScanOverviewRow struct {
	VulnScanResult

	RepositoryName   string
	RepositoryFormat string
	// SortAt is the row's keyset time (see VulnScanCursor).
	SortAt time.Time
}

// VulnScanOverviewPage is one page of the overview.
type VulnScanOverviewPage struct {
	Scans []*VulnScanOverviewRow
	// Next is the position of the last scan when more follow, nil on the last
	// page.
	Next *VulnScanCursor
}

// VulnScanSummary counts the scans of the overview.
type VulnScanSummary struct {
	Total       int
	Completed   int
	Partial     int
	Failed      int
	Scanning    int
	Quarantined int
	// With<Severity> counts scans that have at least one finding of that
	// severity or worse (the Severity filter's meaning).
	WithCritical int
	WithHigh     int
	WithMedium   int
	WithLow      int
}
