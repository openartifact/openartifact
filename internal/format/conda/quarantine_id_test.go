// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestDownloadQuarantinedByRowID: a scan keyed by the package's row id (the id
// the package listing shows) blocks the download of that build only.
func TestDownloadQuarantinedByRowID(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)

	for _, v := range []struct{ id, version string }{{"file-1", "1.0"}, {"file-2", "2.0"}} {
		db.pkgs = append(db.pkgs, &models.CondaPackage{
			ID: v.id, Name: "demo", Version: v.version, BuildString: "py_0", Platform: "linux-64",
			Extension: models.CondaExtTarBz2, BlobKey: "abc", SHA256: "abc", SizeBytes: 3,
		})
	}

	bs.Blobs["abc"] = []byte("xyz")

	formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-1", formattest.StatusOf(h.RegisterRepoRoutes),
		"/repos/myrepo/linux-64/demo-1.0-py_0.tar.bz2", "/repos/myrepo/linux-64/demo-2.0-py_0.tar.bz2")
}
