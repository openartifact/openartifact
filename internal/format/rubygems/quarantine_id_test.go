// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubygems

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestDownloadQuarantinedByRowID: a scan keyed by the gem's row id (the id the
// package listing shows) blocks the download of that gem only.
func TestDownloadQuarantinedByRowID(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.gems = []*models.RubyGem{
		{ID: "file-1", Name: "demo", Version: "1.0.0", Platform: "ruby", BlobKey: "abc"},
		{ID: "file-2", Name: "demo", Version: "1.1.0", Platform: "ruby", BlobKey: "abc"},
	}
	bs.Blobs["abc"] = []byte("xyz")

	formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-1", formattest.StatusOf(h.RegisterRepoRoutes),
		"/repos/myrepo/gems/demo-1.0.0.gem", "/repos/myrepo/gems/demo-1.1.0.gem")
}
