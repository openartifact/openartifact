// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ansible

import (
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestDownloadQuarantinedByRowID: a scan keyed by the collection's row id (the
// id the package listing shows) blocks the download of that version only.
func TestDownloadQuarantinedByRowID(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	for _, v := range []string{"1.0.0", "1.1.0"} {
		spec := acme
		spec.Version = v

		if w, _ := publish(t, h, spec); w.Code != http.StatusAccepted {
			t.Fatalf("publish %s = %d: %s", v, w.Code, w.Body.String())
		}
	}

	formattest.AssertQuarantinedByID(t, &db.Quarantine, db.rows[0].ID, formattest.StatusOf(h.RegisterRepoRoutes),
		publishPath+"acme-tools-1.0.0.tar.gz", publishPath+"acme-tools-1.1.0.tar.gz")
}
