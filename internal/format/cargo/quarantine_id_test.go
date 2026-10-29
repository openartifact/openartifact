// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestDownloadQuarantinedByRowID: a scan keyed by the crate's row id (the id the
// package listing shows) blocks the download of that version only.
func TestDownloadQuarantinedByRowID(t *testing.T) {
	h, db, bs, _ := newCargoHandler(t)
	db.crates = []*models.CargoCrate{
		{ID: "file-1", Name: "demo", Version: "0.1.0", BlobKey: "abc", Checksum: "abc"},
		{ID: "file-2", Name: "demo", Version: "0.2.0", BlobKey: "abc", Checksum: "abc"},
	}
	bs.Blobs["abc"] = []byte("xyz")

	formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-1", formattest.StatusOf(h.RegisterRepoRoutes),
		"/repos/myrepo/api/v1/crates/demo/0.1.0/download", "/repos/myrepo/api/v1/crates/demo/0.2.0/download")
}
