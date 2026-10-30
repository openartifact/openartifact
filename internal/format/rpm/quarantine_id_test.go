// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"context"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// packagesDB serves two packages, each with its own row id, and the quarantined keys.
type packagesDB struct {
	HTTPStore
	formattest.Quarantine

	rows map[string]*models.Component
}

func (*packagesDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return &models.Repository{ID: "repo-1", Name: name, Format: "rpm"}, nil
}

func (d *packagesDB) GetComponentByFilename(_ context.Context, _, filename string) (*models.Component, error) {
	return d.rows[filename], nil
}

// IsArtifactQuarantined names the quarantine the fake uses.
func (d *packagesDB) IsArtifactQuarantined(ctx context.Context, repoID string, keys []string) (bool, error) {
	return d.Quarantine.IsArtifactQuarantined(ctx, repoID, keys)
}

// TestPackageQuarantinedByRowID: a scan keyed by the package's row id (the id
// the package listing shows) blocks the download of that package only.
func TestPackageQuarantinedByRowID(t *testing.T) {
	db := &packagesDB{rows: map[string]*models.Component{
		"hello-1.0-1.x86_64.rpm": {ID: "file-1", SHA256: strings.Repeat("a", 64), Filename: "hello-1.0-1.x86_64.rpm"},
		"hello-1.1-1.x86_64.rpm": {ID: "file-2", SHA256: strings.Repeat("a", 64), Filename: "hello-1.1-1.x86_64.rpm"},
	}}
	h := NewHTTPHandler(formattest.NewLogger(t), db, fakeBlobs{})

	formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-1", formattest.StatusOf(h.RegisterRepoRoutes),
		"/repos/myrepo/Packages/hello-1.0-1.x86_64.rpm", "/repos/myrepo/Packages/hello-1.1-1.x86_64.rpm")
}
