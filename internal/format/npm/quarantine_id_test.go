// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"context"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// quarantinedByKeysDB answers the quarantined keys of a formattest.Quarantine.
type quarantinedByKeysDB struct {
	*tarballMockDB
	formattest.Quarantine
}

// IsArtifactQuarantined names the quarantine the fake uses.
func (d *quarantinedByKeysDB) IsArtifactQuarantined(ctx context.Context, repoID string, keys []string) (bool, error) {
	return d.Quarantine.IsArtifactQuarantined(ctx, repoID, keys)
}

// TestTarballQuarantinedByRowID: a scan keyed by the version's row id (the id the
// package listing shows) blocks the tarball of that version only.
func TestTarballQuarantinedByRowID(t *testing.T) {
	db := &quarantinedByKeysDB{tarballMockDB: &tarballMockDB{
		repo: &models.Repository{ID: "repo-1", Name: "myrepo"},
		pkg:  &models.NPMPackage{ID: "pkg-1", FullName: "lodash"},
		versions: []*models.NPMVersion{
			{ID: "file-1", PackageID: "pkg-1", Version: "1.0.0", BlobSHA256: "sha-abc", TarballFilename: "lodash-1.0.0.tgz"},
			{ID: "file-2", PackageID: "pkg-1", Version: "1.1.0", BlobSHA256: "sha-abc", TarballFilename: "lodash-1.1.0.tgz"},
		},
	}}
	bs := &tarballMockBlobStore{blobs: map[string][]byte{"sha-abc": []byte("payload")}}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost:8080")

	formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-1",
		formattest.StatusOf(func(r chi.Router) { h.RegisterRepoRoutes(r) }),
		"/repos/myrepo/lodash/-/lodash-1.0.0.tgz", "/repos/myrepo/lodash/-/lodash-1.1.0.tgz")
}
