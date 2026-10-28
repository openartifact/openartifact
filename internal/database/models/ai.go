// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// AIAnalysis stores the AI-generated analysis for an artifact.
type AIAnalysis struct {
	ID                    string    `json:"id"`
	RepositoryID          string    `json:"repository_id"`
	ArtifactID            string    `json:"artifact_id"`     // generic reference (component ID, npm version ID, etc.)
	ArtifactFormat        string    `json:"artifact_format"` // "deb", "helm", "maven", "npm", "pypi", "rpm", etc.
	ArtifactName          string    `json:"artifact_name"`
	ArtifactVersion       string    `json:"artifact_version"`
	Summary               string    `json:"summary"`
	Tags                  []string  `json:"tags"`           // stored as JSONB
	LicenseStatus         string    `json:"license_status"` // "SAFE", "REVIEW_REQUIRED", "UNKNOWN"
	LicenseName           string    `json:"license_name"`
	SecurityNotes         string    `json:"security_notes"`
	QuarantineRecommended bool      `json:"quarantine_recommended"`
	Status                string    `json:"status"` // "pending", "processing", "completed", "failed"
	ErrorMessage          string    `json:"error_message"`
	ModelUsed             string    `json:"model_used"`
	PromptTokens          int       `json:"prompt_tokens"`
	CompletionTokens      int       `json:"completion_tokens"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}
