// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// RepositoryDependencyTrack is a repository's Dependency-Track override of the
// global settings. Empty strings and nil booleans inherit the global value;
// APIKey is stored encrypted.
type RepositoryDependencyTrack struct {
	RepositoryID   string    `json:"repository_id"`
	Enabled        bool      `json:"enabled"`
	BaseURL        string    `json:"base_url"`
	APIKey         string    `json:"-"`
	ProjectName    string    `json:"project_name"`
	ProjectVersion string    `json:"project_version"`
	AutoPush       *bool     `json:"auto_push"`
	PushVEX        *bool     `json:"push_vex"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Dependency-Track push states.
const (
	DTrackPushQueued  = "queued"
	DTrackPushRunning = "running"
	DTrackPushDone    = "done"
	DTrackPushSkipped = "skipped"
	DTrackPushFailed  = "failed"
)

// DependencyTrackPush is the last BOM / VEX push of one artifact.
type DependencyTrackPush struct {
	RepositoryID   string     `json:"repository_id"`
	ArtifactID     string     `json:"artifact_id"`
	State          string     `json:"state"`
	Detail         string     `json:"detail"`
	ProjectUUID    string     `json:"project_uuid"`
	ProjectName    string     `json:"project_name"`
	ProjectVersion string     `json:"project_version"`
	Components     int        `json:"components"`
	VEXStatements  int        `json:"vex_statements"`
	VEXApplied     int        `json:"vex_applied"`
	BOMPushedAt    *time.Time `json:"bom_pushed_at"`
	VEXPushedAt    *time.Time `json:"vex_pushed_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}
