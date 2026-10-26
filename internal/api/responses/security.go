// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package responses

// SecurityFindingCounts counts what a finding holds per severity.
type SecurityFindingCounts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
}

// SecurityFindingResponse is one finding of the cross-type findings list: one
// stored scan result (a package version of one type) that reports something.
type SecurityFindingResponse struct {
	ID string `json:"id"`
	// Type is vuln, license, secret or ai.
	Type           string `json:"type"`
	Repository     string `json:"repository"`
	Format         string `json:"format"`
	PackageName    string `json:"package_name"`
	PackageVersion string `json:"package_version"`
	// ArtifactID is the artifact the result belongs to, "name@version".
	ArtifactID string `json:"artifact_id"`
	// Severity is the highest severity of the finding: critical, high, medium,
	// low, or none when it has none.
	Severity string                `json:"severity"`
	Counts   SecurityFindingCounts `json:"counts"`
	// Title is a one-line summary: the worst vulnerability id, the licenses,
	// the worst secret finding or the AI note.
	Title string `json:"title"`
	// Scanner is the scanner that produced the result; empty when the type has no named scanner.
	Scanner string `json:"scanner"`
	// Status is open, quarantined or suppressed.
	Status      string `json:"status"`
	Quarantined bool   `json:"quarantined"`
	// VEXStatus is the least resolved VEX decision of a vuln finding (affected,
	// under_investigation, fixed, not_affected); absent when none applies.
	VEXStatus string `json:"vex_status,omitempty"`
	ScannedAt string `json:"scanned_at"`
}

// SecurityFindingsResponse is one keyset page of findings.
type SecurityFindingsResponse struct {
	Findings []SecurityFindingResponse `json:"findings"`
	HasMore  bool                      `json:"has_more"`
	// NextCursor is the opaque cursor of the next page, present when HasMore.
	NextCursor *string `json:"next_cursor,omitempty"`
}

// SecurityTypeSummaryResponse counts the findings of one type; the severities
// count by each finding's highest severity.
type SecurityTypeSummaryResponse struct {
	Total       int `json:"total"`
	Critical    int `json:"critical"`
	High        int `json:"high"`
	Medium      int `json:"medium"`
	Low         int `json:"low"`
	Quarantined int `json:"quarantined"`
}

// SecurityByTypeResponse holds the summary of every finding type.
type SecurityByTypeResponse struct {
	Vuln    SecurityTypeSummaryResponse `json:"vuln"`
	License SecurityTypeSummaryResponse `json:"license"`
	Secret  SecurityTypeSummaryResponse `json:"secret"`
	AI      SecurityTypeSummaryResponse `json:"ai"`
}

// SecuritySummaryResponse counts findings per type and the scan coverage of the scope.
type SecuritySummaryResponse struct {
	ByType SecurityByTypeResponse `json:"by_type"`
	// ScannedRepositories of TotalRepositories (repositories that hold
	// artifacts) have a completed scan of any type.
	ScannedRepositories int `json:"scanned_repositories"`
	TotalRepositories   int `json:"total_repositories"`
	// LastScanAt is when the latest scan of the scope finished; null when none did.
	LastScanAt *string `json:"last_scan_at"`
}

// SecurityHealthScannerResponse is the readiness of one vulnerability scanner.
type SecurityHealthScannerResponse struct {
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
	// Message tells an operator what to do; absent for a ready scanner.
	Message string `json:"message,omitempty"`
	// DocsURL points at the installation guide of a scanner that is not ready.
	DocsURL string `json:"docs_url,omitempty"`
}

// SecurityHealthDependencyTrackResponse is the state of the Dependency-Track integration.
type SecurityHealthDependencyTrackResponse struct {
	Enabled bool `json:"enabled"`
	// LastPushOK is whether the latest finished push succeeded; absent when none finished.
	LastPushOK *bool `json:"last_push_ok,omitempty"`
	// LastPushAt is when that push finished; absent when none did.
	LastPushAt string `json:"last_push_at,omitempty"`
}

// SecurityHealthAIResponse is the state of the AI analysis.
type SecurityHealthAIResponse struct {
	Enabled bool `json:"enabled"`
}

// SecurityHealthResponse aggregates what the security pages need to tell an
// admin that scanning works.
type SecurityHealthResponse struct {
	Scanners []SecurityHealthScannerResponse `json:"scanners"`
	// LastScanAt is when the latest scan finished; null when none did.
	LastScanAt      *string                               `json:"last_scan_at"`
	ScansPending    int                                   `json:"scans_pending"`
	ScansFailed24h  int                                   `json:"scans_failed_24h"`
	DependencyTrack SecurityHealthDependencyTrackResponse `json:"dependency_track"`
	AI              SecurityHealthAIResponse              `json:"ai"`
	// UnscannedRepositories counts the repositories that hold artifacts and
	// have no completed scan of any type.
	UnscannedRepositories int `json:"unscanned_repositories"`
}

// RepoSecurityLicenseResponse counts the license findings of a repository summary.
type RepoSecurityLicenseResponse struct {
	// Violations counts the license findings that are not suppressed.
	Violations int `json:"violations"`
}

// RepoSecuritySecretResponse counts the secret findings of a repository summary.
type RepoSecuritySecretResponse struct {
	Count int `json:"count"`
}

// RepoSecurityAIResponse counts the AI findings of a repository summary.
type RepoSecurityAIResponse struct {
	QuarantineRecommended int `json:"quarantine_recommended"`
	ReviewRequired        int `json:"review_required"`
}

// RepoSecuritySummaryResponse is the counts-only security state of a repository,
// a package or one version: no ids, titles or other finding details.
type RepoSecuritySummaryResponse struct {
	// ScannedAt is the latest completed scan; null when nothing was scanned.
	ScannedAt *string                     `json:"scanned_at"`
	Vuln      SecurityFindingCounts       `json:"vuln"`
	License   RepoSecurityLicenseResponse `json:"license"`
	Secret    RepoSecuritySecretResponse  `json:"secret"`
	AI        RepoSecurityAIResponse      `json:"ai"`
	// Quarantined is true when a result is quarantined.
	Quarantined bool `json:"quarantined"`
}
