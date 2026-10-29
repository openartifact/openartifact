// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbt

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestDownloadQuarantinedByRowID: a scan keyed by the artifact's row id (the id
// the package listing shows) blocks the download of that artifact only.
func TestDownloadQuarantinedByRowID(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)

	for _, v := range []struct{ id, version string }{{"file-1", "1.0"}, {"file-2", "1.1"}} {
		db.seed(&models.SBTArtifact{
			ID: v.id, GroupID: "org.example", ArtifactID: "mod", Version: v.version,
			Filename: "mod-" + v.version + ".jar", BlobKey: "abc", SHA256: "abc", SizeBytes: 3,
		})
	}

	bs.Blobs["abc"] = []byte("xyz")

	formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-1", formattest.StatusOf(h.RegisterRepoRoutes),
		"/repos/myrepo/org.example/mod/1.0/mod-1.0.jar", "/repos/myrepo/org.example/mod/1.1/mod-1.1.jar")
}
