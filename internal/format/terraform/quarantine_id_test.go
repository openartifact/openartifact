// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestDownloadQuarantinedByRowID: a scan keyed by the module's or provider's row
// id (the id the package listing shows) refuses the download link of that
// version only.
func TestDownloadQuarantinedByRowID(t *testing.T) {
	e := newEnv(t)
	e.h.WithSigner(signerResolver{newSigner(t)})

	for _, v := range []string{"1.0.0", "1.1.0"} {
		if w := e.putModule("vpc", "aws", v, moduleZip(t)); w.Code != http.StatusCreated {
			t.Fatalf("upload module %s: %d", v, w.Code)
		}

		if w := e.putProvider("aws", v, "linux", "amd64", providerZip(t, "aws", v), ""); w.Code != http.StatusCreated {
			t.Fatalf("upload provider %s: %d", v, w.Code)
		}
	}

	// A download link is minted with 204 (module) or described with 200 (provider).
	get := func(path string) int {
		if c := e.do(http.MethodGet, path, nil).Code; c != http.StatusNoContent {
			return c
		}

		return http.StatusOK
	}

	t.Run("module", func(t *testing.T) {
		formattest.AssertQuarantinedByID(t, &e.db.Quarantine, e.db.mods[0].ID, get,
			"/repos/myrepo/vpc/aws/1.0.0/download", "/repos/myrepo/vpc/aws/1.1.0/download")
	})

	t.Run("provider", func(t *testing.T) {
		formattest.AssertQuarantinedByID(t, &e.db.Quarantine, e.db.provs[0].ID, get,
			"/repos/myrepo/aws/1.0.0/download/linux/amd64", "/repos/myrepo/aws/1.1.0/download/linux/amd64")
	})
}
