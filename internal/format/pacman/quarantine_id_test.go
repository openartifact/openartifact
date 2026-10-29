// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pacman

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestDownloadQuarantinedByRowID: a scan keyed by the package's row id (the id
// the package listing shows) blocks the download of that package only.
func TestDownloadQuarantinedByRowID(t *testing.T) {
	h, db, bs, _, _ := newTestHandler(t)

	for _, v := range []struct{ id, file string }{
		{"file-1", "hello-1.0-1-x86_64.pkg.tar.gz"},
		{"file-2", "hello-1.1-1-x86_64.pkg.tar.gz"},
	} {
		db.pkgs[v.file] = &models.PacmanPackage{
			ID: v.id, Filename: v.file, Arch: "x86_64", BlobKey: "abc", SHA256: "abc", SizeBytes: 3,
		}
	}

	bs.Blobs["abc"] = []byte("xyz")

	formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-1", formattest.StatusOf(h.RegisterRepoRoutes),
		"/repos/myrepo/os/x86_64/hello-1.0-1-x86_64.pkg.tar.gz", "/repos/myrepo/os/x86_64/hello-1.1-1-x86_64.pkg.tar.gz")
}
