// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// DebianDistribution represents a Debian distribution (e.g., focal, jammy).
type DebianDistribution struct {
	ID           string
	RepositoryID string
	Name         string // e.g., "focal", "jammy"
	Description  string
	Codename     string // e.g., "20.04", "22.04"
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// DebianComponent represents a component within a distribution (main, universe, etc.)
type DebianComponent struct {
	ID             string
	DistributionID string
	Name           string // e.g., "main", "universe", "restricted"
	Description    string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// DebianArchitecture represents a supported architecture.
type DebianArchitecture struct {
	ID             string
	DistributionID string
	Name           string // e.g., "amd64", "i386", "arm64"
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// DebPackage represents a Debian package published on the generic package tables (package_files and deb_files).
type DebPackage struct {
	ID             string
	RepositoryID   string
	DistributionID string
	ComponentID    string
	BlobID         string
	Name           string
	Version        string
	Arch           string
	Summary        string
	Description    string
	License        string
	Maintainer     string
	Homepage       string
	Filename       string
	InstalledSize  int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
