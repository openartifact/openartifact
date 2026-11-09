// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// VEX statuses recorded in VEXStatement.Status (the OpenVEX status labels).
const (
	VEXAffected           = "affected"
	VEXNotAffected        = "not_affected"
	VEXFixed              = "fixed"
	VEXUnderInvestigation = "under_investigation"
)

// VEX justifications (the OpenVEX enum); required for VEXNotAffected.
const (
	VEXComponentNotPresent       = "component_not_present"
	VEXVulnerableCodeNotPresent  = "vulnerable_code_not_present"
	VEXVulnerableCodeNotInExec   = "vulnerable_code_not_in_execute_path"
	VEXVulnerableCodeNotControl  = "vulnerable_code_cannot_be_controlled_by_adversary"
	VEXInlineMitigationsExisting = "inline_mitigations_already_exist"
)

// VEX statement sources (VEXStatement.SourceKind).
const (
	VEXSourceLocal = "local" // operator decision
	VEXSourceCSAF  = "csaf"  // materialized from a vendor CSAF VEX feed
	VEXSourcePeer  = "peer"  // decision of a federated peer
)

// States of a peer's VEX suggestion (VEXSuggestion.State).
const (
	VEXSuggestionPending   = "pending"
	VEXSuggestionAccepted  = "accepted"
	VEXSuggestionDismissed = "dismissed"
)

// VEXStatement is a decision about one vulnerability in one artifact: an
// operator's (local), a vendor feed's (csaf) or a federated peer's (peer).
type VEXStatement struct {
	ID              string    `json:"id"`
	RepositoryID    string    `json:"repository_id"`
	ArtifactID      string    `json:"artifact_id"`
	VulnID          string    `json:"vuln_id"`
	Status          string    `json:"status"`
	Justification   string    `json:"justification"`
	ImpactStatement string    `json:"impact_statement"`
	ActionStatement string    `json:"action_statement"`
	CreatedBy       string    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	// SourceKind is VEXSourceLocal, VEXSourceCSAF or VEXSourcePeer; empty is
	// stored as local.
	SourceKind string `json:"source_kind"`
	// SourceName is the feed or peer name; empty for local.
	SourceName string `json:"source_name"`
	// SourceRef is the CSAF document URL (csaf) or the peer base URL (peer).
	SourceRef string `json:"source_ref"`
	// FeedID and PeerID are the feed / peer the statement came from, "" when none.
	FeedID string `json:"feed_id,omitempty"`
	PeerID string `json:"peer_id,omitempty"`
}

// VEXFeed is a vendor CSAF 2.0 VEX provider and its import state.
type VEXFeed struct {
	ID               string
	Name             string
	URL              string
	Enabled          bool
	RequireSignature bool
	PGPKeys          string // armored OpenPGP public keys pinned for the feed
	IntervalHours    int
	CursorAt         *time.Time
	LastAttemptAt    *time.Time
	LastSuccessAt    *time.Time
	LastError        string
	DocumentsTotal   int
	RejectedTotal    int
	// StatementCount and AppliedCount are read-only: the rows of the feed in
	// vex_feed_statements and in vex_statements.
	StatementCount int
	AppliedCount   int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// VEXFeedStatement is one not_affected / fixed statement of an imported CSAF
// document for one product, identified by a normalized PURL or else a CPE.
type VEXFeedStatement struct {
	DocumentURL     string
	TrackingID      string
	VulnID          string
	PURL            string
	CPE             string
	Status          string
	Justification   string
	ImpactStatement string
	ActionStatement string
	ReleasedAt      *time.Time
}

// VEXFeedSyncResult is the outcome of one sync of a feed.
type VEXFeedSyncResult struct {
	Success bool
	// Error is a client-safe summary; "" on a clean run.
	Error string
	// CursorAt is the new cursor; nil keeps the current one.
	CursorAt  *time.Time
	Documents int
	Rejected  int
}

// VEXTarget is the latest completed or partial vulnerability scan, with
// findings, of one artifact.
type VEXTarget struct {
	RepositoryID     string
	RepositoryFormat string
	ArtifactID       string
	Vulns            []Vulnerability
}

// VEXSuggestion is a federated peer's VEX decision about an artifact of the
// pulling repository, advisory until accepted or dismissed.
type VEXSuggestion struct {
	ID              string
	RepositoryID    string
	PeerID          string
	PeerName        string
	PeerBaseURL     string
	ArtifactID      string
	VulnID          string
	Status          string
	Justification   string
	ImpactStatement string
	ActionStatement string
	PeerUpdatedAt   *time.Time
	State           string
	DecidedBy       string
	DecidedAt       *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// LocalStatus and LocalSourceKind describe the vex_statements row of the
	// same (repository, artifact, vulnerability), "" when none (read-only).
	LocalStatus     string
	LocalSourceKind string
}
