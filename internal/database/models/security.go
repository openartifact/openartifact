// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Finding types of the cross-type security findings list.
const (
	FindingVuln    = "vuln"
	FindingLicense = "license"
	FindingSecret  = "secret"
	FindingAI      = "ai"
)

// FindingTypes lists every finding type, the default of a findings query.
func FindingTypes() []string {
	return []string{FindingVuln, FindingLicense, FindingSecret, FindingAI}
}

// Finding statuses: quarantined (the result's quarantine flag), suppressed (VEX
// decisions or an admin approval resolve it) or open.
const (
	FindingStatusOpen        = "open"
	FindingStatusQuarantined = "quarantined"
	FindingStatusSuppressed  = "suppressed"
)

// Severities of a finding, highest first. FindingSeverityNone marks a result
// that reports something without a severity (an approved license, say).
const (
	FindingSeverityCritical = "critical"
	FindingSeverityHigh     = "high"
	FindingSeverityMedium   = "medium"
	FindingSeverityLow      = "low"
	FindingSeverityNone     = "none"
)

// FindingScope limits a findings query to the repositories a caller may see.
type FindingScope struct {
	// All sees every repository (a global admin).
	All bool
	// ProjectIDs are the projects whose repositories are visible when All is false.
	ProjectIDs []string
}

// FindingFilter narrows findings by repository and project name (empty: any)
// within a scope.
type FindingFilter struct {
	Scope   FindingScope
	Repo    string
	Project string
}

// FindingListParams selects one keyset page of findings. Empty filters match
// everything.
type FindingListParams struct {
	FindingFilter
	// Types are the finding types to include (never empty: FindingTypes()).
	Types []string
	// Severity is the highest severity of the finding: critical, high, medium or low.
	Severity string
	// Status is open, quarantined or suppressed.
	Status string
	// Q matches the repository name and "name@version" case-insensitively as a
	// substring.
	Q     string
	Limit int
	// After, when set, returns only findings strictly after this position.
	After *FindingCursor
}

// FindingCursor is a keyset position in the findings order (severity rank
// DESC, ScannedAt DESC, ID ASC).
type FindingCursor struct {
	SeverityRank int
	ScannedAt    time.Time
	ID           string
}

// Finding is one stored scan result that reports something.
type Finding struct {
	ID             string
	Type           string
	RepositoryID   string
	Repository     string
	Format         string
	ArtifactID     string
	PackageName    string
	PackageVersion string
	Scanner        string
	ScanStatus     string
	Critical       int
	High           int
	Medium         int
	Low            int
	Quarantined    bool
	Status         string
	// VEXStatus is the least resolved VEX decision on the vulnerabilities of a
	// vuln finding (affected, under_investigation, fixed, not_affected); empty
	// when none applies.
	VEXStatus string
	// SeverityRank is 4 critical, 3 high, 2 medium, 1 low, 0 none.
	SeverityRank int
	// Title is a one-line summary: the worst vulnerability id, the licenses, the
	// worst secret finding or the AI note.
	Title     string
	ScannedAt time.Time
}

// Severity is the highest severity of the finding, FindingSeverityNone when it
// has none.
func (f *Finding) Severity() string {
	switch f.SeverityRank {
	case 4:
		return FindingSeverityCritical
	case 3:
		return FindingSeverityHigh
	case 2:
		return FindingSeverityMedium
	case 1:
		return FindingSeverityLow
	}

	return FindingSeverityNone
}

// FindingPage is one page of findings; Next is set when more follow.
type FindingPage struct {
	Findings []*Finding
	Next     *FindingCursor
}

// FindingTypeSummary counts the findings of one type; the severities count by
// each finding's highest severity.
type FindingTypeSummary struct {
	Total       int
	Critical    int
	High        int
	Medium      int
	Low         int
	Quarantined int
}

// FindingsSummary counts findings per type and how much of the scope was scanned.
type FindingsSummary struct {
	ByType map[string]FindingTypeSummary
	// ScannedRepositories of TotalRepositories (repositories that hold artifacts)
	// have a completed scan of any type.
	ScannedRepositories int
	TotalRepositories   int
	// LastScanAt is when the latest scan of the scope finished; nil when none did.
	LastScanAt *time.Time
}

// SecurityHealthStats are the instance-wide scan queue and coverage numbers.
type SecurityHealthStats struct {
	ScansPending          int
	ScansFailed24h        int
	LastScanAt            *time.Time
	UnscannedRepositories int
}

// RepoSecuritySummary is the counts-only security state of a repository, a
// package or one version: no ids, titles or other finding details.
type RepoSecuritySummary struct {
	// ScannedAt is the latest completed scan; nil when nothing was scanned.
	ScannedAt               *time.Time
	VulnCritical            int
	VulnHigh                int
	VulnMedium              int
	VulnLow                 int
	LicenseViolations       int
	SecretCount             int
	AIQuarantineRecommended int
	AIReviewRequired        int
	// Quarantined counts the results whose quarantine flag is set.
	Quarantined int
}

// DependencyTrackLastPush is the outcome of the most recent finished
// Dependency-Track push of any artifact.
type DependencyTrackLastPush struct {
	OK bool
	At time.Time
}
