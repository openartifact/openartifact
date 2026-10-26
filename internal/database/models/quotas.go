// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

type StorageQuota struct {
	ID                   string
	RepositoryID         string
	MaxSizeBytes         int64
	MaxArtifactCount     int
	WarnThresholdPercent int
	CurrentSizeBytes     int64
	CurrentArtifactCount int
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (q *StorageQuota) IsExceeded() bool {
	if q.MaxSizeBytes > 0 && q.CurrentSizeBytes >= q.MaxSizeBytes {
		return true
	}

	if q.MaxArtifactCount > 0 && q.CurrentArtifactCount >= q.MaxArtifactCount {
		return true
	}

	return false
}

func (q *StorageQuota) UsagePercent() float64 {
	if q.MaxSizeBytes == 0 {
		return 0
	}

	return float64(q.CurrentSizeBytes) / float64(q.MaxSizeBytes) * 100
}

// ProjectQuota is the storage ceiling over all repositories of a project (a
// limit of 0 means unlimited). The Current* counters are only maintained while
// a limit is set; Usage reports the live sums of the repository counters.
type ProjectQuota struct {
	ProjectID            string
	MaxSizeBytes         int64
	MaxArtifactCount     int64
	WarnThresholdPercent int
	CurrentSizeBytes     int64
	CurrentArtifactCount int64
	UpdatedAt            time.Time
}

// Limited reports whether the ceiling is enforced (a non-zero limit is set).
func (q *ProjectQuota) Limited() bool {
	return q != nil && (q.MaxSizeBytes > 0 || q.MaxArtifactCount > 0)
}

// ProjectRepoUsage is one repository's line in a project usage report.
type ProjectRepoUsage struct {
	RepositoryID         string
	Name                 string
	Format               string
	RepoType             string
	SizeBytes            int64
	ArtifactCount        int64
	MaxSizeBytes         int64
	MaxArtifactCount     int64
	WarnThresholdPercent int
}

// ProjectUsage is the usage of a project: the sum over its repositories plus
// the ceiling.
type ProjectUsage struct {
	Quota         ProjectQuota
	SizeBytes     int64
	ArtifactCount int64
	Repositories  []ProjectRepoUsage
}

// ProjectStats is what the project list shows next to each project: direct
// members, live usage (the sum of its repositories' counters, trashed ones
// included, as ProjectUsage) and the size ceiling (0: no quota).
type ProjectStats struct {
	MemberCount  int
	SizeBytes    int64
	MaxSizeBytes int64
}

// Quota scopes of QuotaOverview.
const (
	QuotaScopeRepository = "repository"
	QuotaScopeProject    = "project"
)

// QuotaListParams selects a page of the instance-wide quota overview.
type QuotaListParams struct {
	// Scope keeps one scope only ("" for both).
	Scope string
	// LimitedOnly keeps only quotas that enforce a limit.
	LimitedOnly bool
	// Query is a case-insensitive substring of the repository or project name.
	Query string
	Page  int
	Limit int
}

// QuotaOverview is one line of the instance-wide quota overview: a
// repository's or a project's limits next to its live usage. A limit of 0 means
// unlimited.
type QuotaOverview struct {
	Scope                string
	Name                 string
	Project              string // owning project of a repository ("" when none)
	MaxSizeBytes         int64
	MaxArtifactCount     int64
	WarnThresholdPercent int
	UsedBytes            int64
	ArtifactCount        int64
}

// RepositoryUsage summarises what a repository holds.
type RepositoryUsage struct {
	PackageCount    int64
	VersionCount    int64
	LastPublishedAt *time.Time
}
