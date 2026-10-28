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
	// Aliases are the other ids the scanner knows the vulnerability by (a GHSA
	// id lists its CVE); the exploitation feeds are keyed by CVE id.
	Aliases []string `json:"aliases,omitempty"`
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
	// KEVCount is the number of findings whose CVE is in the CISA KEV catalog
	// and MaxEPSS the highest EPSS score among them (nil: none scored), as of
	// the last scan or feed import.
	KEVCount  int
	MaxEPSS   *float64
	ScannedAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// VulnScanCursor is a keyset position in the overview order (critical_count
// DESC, high_count DESC, medium_count DESC, low_count DESC, SortAt DESC, id ASC),
// or, for the priority sort, the same keys led by the priority Rank DESC.
type VulnScanCursor struct {
	// Rank is the priority rank (6..0) of the scan; only the priority sort uses it.
	Rank          int
	CriticalCount int
	HighCount     int
	MediumCount   int
	LowCount      int
	// SortAt is the scan's completion time, or its creation time while it has
	// none.
	SortAt time.Time
	ID     string
}

// Sort orders of the scan overview (VulnScanListParams.Sort).
const (
	VulnSortSeverity = "severity"
	VulnSortPriority = "priority"
)

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
	// Sort is "" or "severity" (worst severity counts first) or "priority"
	// (priority rank first, then the severity order).
	Sort string
	// KEV, when set, keeps scans with (true) or without (false) a finding in
	// the CISA KEV catalog.
	KEV *bool
	// EPSSMin, when set, keeps scans whose highest EPSS score is at least it.
	EPSSMin *float64
	// Priority, when set (kev, epss, critical, high, medium or low), keeps
	// scans whose priority rank equals that of the named priority.
	Priority string
	// EPSSThreshold is the EPSS score from which a scan ranks as "epss"; the
	// priority sort and filter use it.
	EPSSThreshold float64
	Limit         int
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
