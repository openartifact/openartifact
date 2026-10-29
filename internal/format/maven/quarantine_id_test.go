// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"context"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/pkg/logger"
)

// mavenByPathDB looks artifacts up by repository path and answers the
// quarantined keys.
type mavenByPathDB struct {
	mavenMockDB
	formattest.Quarantine

	arts map[string]*models.MavenArtifact
}

func (d *mavenByPathDB) GetMavenArtifactByPath(_ context.Context, _, path string) (*models.MavenArtifact, error) {
	return d.arts[path], nil
}

// IsArtifactQuarantined names the quarantine the fake uses.
func (d *mavenByPathDB) IsArtifactQuarantined(ctx context.Context, repoID string, keys []string) (bool, error) {
	return d.Quarantine.IsArtifactQuarantined(ctx, repoID, keys)
}

// TestDownloadQuarantinedByRowID: a scan keyed by the artifact's row id (the id
// the package listing shows) blocks the download of that file only.
func TestDownloadQuarantinedByRowID(t *testing.T) {
	const sum = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	db := &mavenByPathDB{arts: map[string]*models.MavenArtifact{}}
	db.repo = &models.Repository{ID: "repo-1", Name: "myrepo"}

	for _, v := range []struct{ id, version string }{{"file-1", "1.0"}, {"file-2", "1.1"}} {
		p := "com/example/a/" + v.version + "/a-" + v.version + ".jar"
		db.arts[p] = &models.MavenArtifact{ID: v.id, BlobID: "uuid", SHA256: sum, RepoPath: p, Version: v.version}
	}

	log, _ := logger.New("info", "json", "stdout")
	h := NewHTTPHandler(log, db, &mavenMockBlobStore{blobs: map[string][]byte{sum: []byte("jar")}})

	formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-1", formattest.StatusOf(h.RegisterRepoRoutes),
		"/repos/myrepo/com/example/a/1.0/a-1.0.jar", "/repos/myrepo/com/example/a/1.1/a-1.1.jar")
}
