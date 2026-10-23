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

// VEXStatement is an admin decision about one vulnerability in one artifact.
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
}
