// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// mvnRow describes one maven file: artifact, version, file extension, age in
// days and, for a snapshot build, its timestamp and build number.
type mvnRow struct {
	artifact, version, ext string
	ageDays                int
	snapTS                 string
	snapBuild              int
}

// mavenFile stores one maven file through the maven store (so every table the
// store writes, maven_files included, is as production leaves it) pointing at
// blobID, and returns the file id.
func (s *purgeSeed) mavenFile(repoID, blobID string, r mvnRow) string {
	s.t.Helper()

	path := fmt.Sprintf("g/%s/%s/%s-%s.%s", r.artifact, r.version, r.artifact, r.version, r.ext)
	if r.snapTS != "" {
		path = fmt.Sprintf("g/%s/%s/%s-%s-%d.%s", r.artifact, r.version, r.artifact, r.snapTS, r.snapBuild, r.ext)
	}

	at := time.Now().Add(-time.Duration(r.ageDays) * 24 * time.Hour)
	a := &models.MavenArtifact{
		RepositoryID: repoID, BlobID: blobID, GroupID: "g", ArtifactCoord: r.artifact, Version: r.version,
		Extension: r.ext, RepoPath: path, IsSnapshot: r.snapTS != "", SnapshotTimestamp: r.snapTS,
		SnapshotBuildNumber: r.snapBuild, CreatedAt: at, UpdatedAt: at,
	}

	if err := s.pg.CreateMavenArtifact(context.Background(), a); err != nil {
		s.t.Fatalf("CreateMavenArtifact %+v: %v", r, err)
	}

	return a.ID
}
