// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// SecurityCounts are vulnerability counts by severity (raw: VEX statements and
// ignore rules do not suppress any).
type SecurityCounts struct {
	Critical int64
	High     int64
	Medium   int64
	Low      int64
}

// RepoSecurity is the security state of a repository: the vulnerability counts
// of the latest scan of every artifact summed, the secret findings, the
// quarantined license results and the time of the latest completed scan.
type RepoSecurity struct {
	SecurityCounts
	Secrets int64
	// LicenseViolations is the number of artifacts whose license scan is
	// quarantined; the server stores no other license violation count.
	LicenseViolations int64
	// Quarantined is the number of quarantined scan results of any type.
	Quarantined int64
	ScannedAt   *time.Time
}

// Flagged reports whether the repository holds anything that needs attention:
// a critical or high vulnerability, a secret finding or a quarantined result.
func (s RepoSecurity) Flagged() bool {
	return s.Critical+s.High > 0 || s.Secrets > 0 || s.Quarantined > 0
}

// RepoQuotaUsage is a repository's storage use against its quota.
type RepoQuotaUsage struct {
	// LimitBytes is the size quota; 0 means unlimited.
	LimitBytes int64
	UsedBytes  int64
}

// RepoStats are the catalog figures of one repository.
type RepoStats struct {
	// SizeBytes is the stored size, from the quota counter kept up to date on
	// every upload (it equals Quota.UsedBytes).
	SizeBytes       int64
	LastPublishedAt *time.Time
	Security        RepoSecurity
	Quota           RepoQuotaUsage
}

// NearQuota reports whether the repository has a size quota and has used at
// least threshold (0..1) of it.
func (s RepoStats) NearQuota(threshold float64) bool {
	return s.Quota.LimitBytes > 0 && float64(s.Quota.UsedBytes) >= threshold*float64(s.Quota.LimitBytes)
}

// ArtifactSecurity is the security state of one package row (one file): its
// latest vulnerability counts, secret findings, quarantine state and the time of
// its latest completed scan.
type ArtifactSecurity struct {
	SecurityCounts
	Secrets     int64
	Quarantined bool
	ScannedAt   *time.Time
}

// PackageRollupOptions select and order the per-package rollup of one
// repository.
type PackageRollupOptions struct {
	RepoID string
	// RepoFormat is the repository's format; when it names a package table
	// only that table (and the proxy cache) is read.
	RepoFormat string
	// Format, Name (exact) and Query (case-insensitive substring of the name or
	// version) restrict the matched package rows, as in ComponentListOptions.
	Format string
	Name   string
	Query  string
	// Flagged keeps the packages whose latest version has a critical or high
	// vulnerability, a secret finding or a quarantined scan result.
	Flagged bool
	// SortByName orders by name; otherwise the most recently published first.
	SortByName bool
	Page       int
	Limit      int
}

// PackageRollup is one package (repository, format, name) across its versions.
type PackageRollup struct {
	Format        string
	Name          string
	LatestVersion string
	VersionCount  int
	// SizeBytes is the size of the latest version, all its files summed.
	SizeBytes int64
	// CreatedAt is when the latest version was published; UpdatedAt the latest
	// modification of any version.
	CreatedAt time.Time
	UpdatedAt time.Time
	// LatestIDs are the listing row ids (one per file) of the latest version.
	LatestIDs []string
}

// RecentPackage is a recently published package version.
type RecentPackage struct {
	Repository string
	Format     string
	Name       string
	Version    string
	CreatedAt  time.Time
}

// ComponentDetail is one package row with the metadata its table stores.
type ComponentDetail struct {
	ID          string
	Format      string
	Name        string
	Version     string
	Arch        string
	Filename    string
	SHA256      string
	MD5         string
	SHA1        string
	SizeBytes   int64
	Summary     string
	Description string
	License     string
	Homepage    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// Info holds the format-specific display fields (empty values dropped).
	Info map[string]string
	// Deps is the raw column dependencies are decoded from; its shape depends
	// on Format.
	Deps []byte
}
