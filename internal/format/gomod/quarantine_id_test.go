// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gomod

import (
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestVersionQuarantinedByRowID: a scan keyed by the zip's row id (the id the
// package listing shows) blocks every file of that version (.zip, .mod, .info),
// hides it from the list, and leaves the other version served.
func TestVersionQuarantinedByRowID(t *testing.T) {
	router, db, bs, _ := newTestRouter(t)
	zipKey := strings.Repeat("ab", 32)

	for _, v := range []struct{ id, version string }{{"file-1", "v1.0.0"}, {"file-2", "v1.1.0"}} {
		db.mods = append(db.mods, &models.GoModule{
			ID: v.id, ModulePath: testModule, Version: v.version, ZipBlobKey: zipKey, ZipHash: zipKey,
			ZipSizeBytes: 3, GoModContent: "module x",
		})
	}

	bs.Blobs[zipKey] = []byte("xyz")

	base := "/repos/myrepo/" + testModule + "/@v/"
	get := func(path string) int { return serve(router, http.MethodGet, path, nil).Code }

	for _, ext := range []string{".zip", ".mod", ".info"} {
		t.Run(ext, func(t *testing.T) {
			formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-1", get, base+"v1.0.0"+ext, base+"v1.1.0"+ext)
		})
	}

	db.Keys = map[string]bool{"file-1": true}

	if w := serve(router, http.MethodGet, base+"list", nil); strings.TrimSpace(w.Body.String()) != "v1.1.0" {
		t.Errorf("list = %q, want only v1.1.0", w.Body.String())
	}
}
