// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"encoding/json"
	"time"
)

// OSVAdvisory is one record of the local OSV mirror (https://osv.dev, CC-BY 4.0).
type OSVAdvisory struct {
	ID        string
	Modified  time.Time
	Published *time.Time
	// Withdrawn is set when OSV withdrew the record: it matches nothing.
	Withdrawn *time.Time
	Summary   string
	// Severity is the record's severity array as published.
	Severity json.RawMessage
	// DBSeverity is database_specific.severity, "" when absent.
	DBSeverity string
	CWEIDs     []string
	Refs       []string
	// Aliases are the other ids of the record (CVE, GHSA, ...), without its own id.
	Aliases  []string
	Affected []OSVAffected
}

// OSVAffected is one affected package of an advisory: the ranges and explicit
// versions as OSV publishes them.
type OSVAffected struct {
	Ecosystem string
	Name      string
	// NameNorm is Name normalized for matching.
	NameNorm string
	// Ranges is the JSON array of OSV ranges ([{"type":"SEMVER","events":[...]}]).
	Ranges   json.RawMessage
	Versions []string
}

// OSVPackageKey identifies an affected package of the mirror.
type OSVPackageKey struct {
	Ecosystem string
	NameNorm  string
}

// OSVAdvisoryMatch is an advisory with the affected entries that name one
// package: what a version is matched against.
type OSVAdvisoryMatch struct {
	Advisory OSVAdvisory
	Affected []OSVAffected
}

// OSVMirrorState is the import state of one ecosystem of the mirror.
type OSVMirrorState struct {
	Ecosystem string
	// HighWater is the largest `modified` imported; nil before the first import.
	HighWater *time.Time
	// ETag and ArchiveModified validate the next download (conditional request).
	ETag            string
	ArchiveModified string
	SourceURL       string
	// Advisories is the number of advisories that affect the ecosystem.
	Advisories         int
	LastCheckedAt      *time.Time
	LastImportedAt     *time.Time
	LastImportSeconds  float64
	LastRematchSeconds float64
	LastError          string
	// RematchPending is set while advisory changes are applied and not yet
	// matched against the stored packages.
	RematchPending bool
	// RematchedAt is when the last completed re-match started; nil before the first.
	RematchedAt *time.Time
}

// StoredPackage is a package version held by a repository (hosted rows or the
// proxy cache): what a re-match evaluates.
type StoredPackage struct {
	RepositoryID string
	// ArtifactID is the row id the version is scanned under (the package
	// listing's id; the lowest one when a version has several files).
	ArtifactID string
	Name       string
	Version    string
	// AddedAt is when the newest row of the version was stored.
	AddedAt time.Time
}

// ProxyCacheKey is a cached upstream file of a proxy repository: the key its
// package coordinates are derived from.
type ProxyCacheKey struct {
	ID           string
	RepositoryID string
	CacheKey     string
	CreatedAt    time.Time
}
