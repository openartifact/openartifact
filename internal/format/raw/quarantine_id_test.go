// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package raw

import (
	"context"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// rawByPathDB looks stored artifacts up by path.
type rawByPathDB struct {
	*rawDB

	arts map[string]*models.RawArtifact
}

func (d *rawByPathDB) GetRawArtifact(_ context.Context, _, path string) (*models.RawArtifact, error) {
	return d.arts[path], nil
}

// TestDownloadQuarantinedByRowID: a scan keyed by the artifact's row id (the id
// the package listing shows) blocks the GET and HEAD of that artifact only.
func TestDownloadQuarantinedByRowID(t *testing.T) {
	db := &rawByPathDB{rawDB: &rawDB{DB: formattest.NewDB()}, arts: map[string]*models.RawArtifact{
		"a/one.txt": {ID: "file-1", Path: "a/one.txt", BlobKey: "abc", SHA256: "abc", SizeBytes: 3, ContentType: "text/plain"},
		"a/two.txt": {ID: "file-2", Path: "a/two.txt", BlobKey: "abc", SHA256: "abc", SizeBytes: 3, ContentType: "text/plain"},
	}}
	bs := formattest.NewBlobStore()
	bs.Blobs["abc"] = []byte("xyz")
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")

	formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-1", formattest.StatusOf(h.RegisterRepoRoutes),
		"/repos/myrepo/a/one.txt", "/repos/myrepo/a/two.txt")
}
