// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nix

import (
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestQuarantinedByRowID: a scan keyed by the store path's row id (the id the
// package listing shows) blocks its narinfo and its NAR, and no other path.
func TestQuarantinedByRowID(t *testing.T) {
	f := newFixture(t)
	a := newStorePath(hashA, "hello-2.12", "hello")
	b := newStorePath(hashB, "world-1.0", "world")
	f.upload(t, a)
	f.upload(t, b)

	get := func(path string) int { return f.do(http.MethodGet, path, nil).Code }
	id := f.db.paths[hashA].ID

	for name, blocked := range map[string]string{
		"narinfo": "/repos/myrepo/" + hashA + ".narinfo",
		"nar":     "/repos/myrepo/nar/" + a.file,
	} {
		t.Run(name, func(t *testing.T) {
			formattest.AssertQuarantinedByID(t, &f.db.Quarantine, id, get, blocked, "/repos/myrepo/"+hashB+".narinfo")
		})
	}
}
