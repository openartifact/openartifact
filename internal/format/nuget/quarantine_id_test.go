// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestDownloadQuarantinedByRowID: a scan keyed by the package's row id (the id
// the package listing shows) blocks the download of that version only.
func TestDownloadQuarantinedByRowID(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.more = []*models.NuGetPackage{
		{ID: "file-1", PackageID: "foo", Version: "1.0.0", BlobKey: "abc", PackageSize: 3},
		{ID: "file-2", PackageID: "foo", Version: "2.0.0", BlobKey: "abc", PackageSize: 3},
	}
	bs.Blobs["abc"] = []byte("xyz")

	formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-1", formattest.StatusOf(register(h)),
		"/repos/myrepo/v3/package/foo/1.0.0/foo.1.0.0.nupkg", "/repos/myrepo/v3/package/foo/2.0.0/foo.2.0.0.nupkg")
}
