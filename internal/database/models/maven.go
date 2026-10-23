// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"time"

	pkgmaven "github.com/openartifact/openartifact/pkg/maven"
)

type MavenArtifact struct {
	ID                  string
	RepositoryID        string
	BlobID              string
	GroupID             string
	ArtifactCoord       string
	Version             string
	Classifier          string
	Extension           string
	SnapshotTimestamp   string
	SnapshotBuildNumber int
	IsSnapshot          bool
	RepoPath            string
	MD5                 string
	SHA1                string
	SHA256              string
	SHA512              string
	// BlobSHA256/BlobSizeBytes are populated only by ListMavenArtifactsByRepo
	// (a JOIN with blobs, used for backup export) since BlobID above is a
	// live blobs.id UUID that doesn't survive a restore into a fresh DB.
	BlobSHA256    string
	BlobSizeBytes int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// MavenGARefs is what maven-metadata.xml regeneration needs to know about one
// group:artifact, without the ids, paths and checksums of the full artifact rows.
type MavenGARefs struct {
	// Versions holds every distinct live version, in version (string) order.
	Versions []string
	// Snapshots holds one entry per live file of every -SNAPSHOT version.
	Snapshots []MavenSnapshotRef
}

// MavenSnapshotRef is one live file of a -SNAPSHOT version.
type MavenSnapshotRef struct {
	Version     string
	Classifier  string
	Extension   string
	Timestamp   string
	BuildNumber int
}

func (m *MavenArtifact) Coordinate() pkgmaven.Coordinate {
	return pkgmaven.Coordinate{
		GroupID:    m.GroupID,
		ArtifactID: m.ArtifactCoord,
		Version:    m.Version,
		Classifier: m.Classifier,
		Extension:  m.Extension,
	}
}
