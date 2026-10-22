// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/cron"
)

// DefaultCleanupCron is the schedule applied when a policy has none (daily at 02:00 UTC).
const DefaultCleanupCron = "0 2 * * *"

// ErrInvalidCleanupPolicy is wrapped by every CleanupPolicy.Validate error.
var ErrInvalidCleanupPolicy = errors.New("invalid cleanup policy")

type CleanupPolicy struct {
	ID string `json:"id"`
	// RepositoryID is "" for a project default policy, whose ProjectID is set.
	RepositoryID            string     `json:"repository_id"`
	ProjectID               string     `json:"project_id,omitempty"`
	Name                    string     `json:"name"`
	Enabled                 bool       `json:"enabled"`
	KeepLastN               *int       `json:"keep_last_n"`
	KeepSinceDays           *int       `json:"keep_since_days"`
	KeepDownloadedSinceDays *int       `json:"keep_downloaded_since_days"`
	NamePattern             string     `json:"name_pattern"`
	ScheduleCron            string     `json:"schedule_cron"`
	LastRunAt               *time.Time `json:"last_run_at"`
	LastRunDeleted          *int       `json:"last_run_deleted"`
	LastRunError            string     `json:"last_run_error"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

// Validate checks that the policy can safely be executed: at least one
// retention criterion, strictly positive values (zero or negative values would
// select every artifact for deletion), a compilable name pattern and a valid
// cron schedule. keep_downloaded_since_days is rejected because download
// timestamps are not tracked, so it could never be honoured.
func (p *CleanupPolicy) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidCleanupPolicy)
	}

	if p.KeepLastN != nil && *p.KeepLastN < 1 {
		return fmt.Errorf("%w: keep_last_n must be >= 1", ErrInvalidCleanupPolicy)
	}

	if p.KeepSinceDays != nil && *p.KeepSinceDays < 1 {
		return fmt.Errorf("%w: keep_since_days must be >= 1", ErrInvalidCleanupPolicy)
	}

	if p.KeepDownloadedSinceDays != nil {
		return fmt.Errorf("%w: keep_downloaded_since_days is not supported (download times are not tracked)", ErrInvalidCleanupPolicy)
	}

	if p.KeepLastN == nil && p.KeepSinceDays == nil {
		return fmt.Errorf("%w: at least one of keep_last_n or keep_since_days is required", ErrInvalidCleanupPolicy)
	}

	if p.NamePattern != "" {
		if _, err := regexp.Compile(p.NamePattern); err != nil {
			return fmt.Errorf("%w: name_pattern is not a valid regular expression: %w", ErrInvalidCleanupPolicy, err)
		}
	}

	if p.ScheduleCron != "" {
		if _, err := cron.Parse(p.ScheduleCron); err != nil {
			return fmt.Errorf("%w: schedule_cron: %w", ErrInvalidCleanupPolicy, err)
		}
	}

	return nil
}
