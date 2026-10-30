// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestDownloadQuarantinedByRowID: a scan keyed by the file's row id (the id the
// package listing shows) blocks the download of that file only.
func TestDownloadQuarantinedByRowID(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.pkgs = []*models.PyPIPackage{
		{ID: "file-1", Name: "demo", Version: "1.0", Filename: "demo-1.0.whl", SHA256Digest: "abc", SizeBytes: 3},
		{ID: "file-2", Name: "demo", Version: "1.1", Filename: "demo-1.1.whl", SHA256Digest: "abc", SizeBytes: 3},
	}
	bs.Blobs["abc"] = []byte("xyz")

	formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-1", formattest.StatusOf(register(h)),
		"/repos/myrepo/files/demo-1.0.whl", "/repos/myrepo/files/demo-1.1.whl")
}
