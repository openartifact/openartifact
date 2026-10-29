// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestDownloadQuarantinedByRowID: a scan keyed by the chart's row id (the id the
// package listing shows) blocks the download of that chart only.
func TestDownloadQuarantinedByRowID(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)

	for _, v := range []struct{ id, version string }{{"file-1", "1.0.0"}, {"file-2", "1.1.0"}} {
		db.charts[chartKey("mychart", v.version)] = &models.HelmChart{
			ID: v.id, Name: "mychart", Version: v.version, BlobKey: "abc", Digest: "abc", SizeBytes: 3,
		}
	}

	bs.Blobs["abc"] = []byte("xyz")

	formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-1", formattest.StatusOf(h.RegisterRepoRoutes),
		"/repos/myrepo/charts/mychart-1.0.0.tgz", "/repos/myrepo/charts/mychart-1.1.0.tgz")
}
