// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"time"
)

type CargoCrate struct {
	ID            string
	RepositoryID  string
	Name          string
	Version       string
	Description   string
	Homepage      string
	Documentation string
	Repository    string
	License       string
	Keywords      []string
	Categories    []string
	Authors       []string
	Readme        string
	MetadataJSON  []byte
	BlobKey       string
	Checksum      string
	SizeBytes     int64 // size of the crate archive; recorded on the shared blobs row
	Yanked        bool
	DownloadCount int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CargoCrateSummary is one crate in a search result, aggregated over all of
// its live versions.
type CargoCrateSummary struct {
	Name        string
	Description string // of the newest version
	Downloads   int64  // summed over every version
	// Versions lists every live version, yanked ones included, oldest first.
	Versions []string
	// LiveVersions lists the versions that are not yanked, oldest first.
	LiveVersions []string
}
