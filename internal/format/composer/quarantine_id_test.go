// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer

import (
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/pkg/composer/composertest"
)

// TestDistQuarantinedByRowID: a scan keyed by the package's row id (the id the
// package listing shows) blocks the dist of that version only.
func TestDistQuarantinedByRowID(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	next := composertest.Spec{Name: "acme/tools", Description: "Next"}

	for _, up := range []struct {
		version string
		spec    composertest.Spec
	}{{"v1.2.0", acme}, {"v1.3.0", next}} {
		if w := upload(h, "acme/tools", up.version, composertest.Build(t, up.spec)); w.Code != http.StatusCreated {
			t.Fatalf("upload %s = %d: %s", up.version, w.Code, w.Body.String())
		}
	}

	var dist [2]string

	for i, r := range db.rows {
		r.ID = "file-" + r.Version
		dist[i] = root + "dists/acme/tools/" + r.Version + "/" + r.SHA256 + ".zip"
	}

	formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-v1.2.0", formattest.StatusOf(h.RegisterRepoRoutes),
		dist[0], dist[1])
}
