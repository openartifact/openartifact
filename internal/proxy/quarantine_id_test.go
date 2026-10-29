// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"net/http"
	"testing"
)

// TestAnsibleProxy_QuarantinedByEntryRowID: a scan keyed by the cache entry's row
// id (the id the package listing shows for a proxy repository) refuses the
// cached download of that file only.
func TestAnsibleProxy_QuarantinedByEntryRowID(t *testing.T) {
	f := ansibleFixture(t, newFakeGalaxy())
	blocked := "api/v3/artifacts/collections/ns-demo-1.0.0.tar.gz"
	sibling := "api/v3/artifacts/collections/ns-demo-1.1.0.tar.gz"

	for _, p := range []string{blocked, sibling} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", p, w.Code)
		}
	}

	f.svc.db = &quarantineDB{memDB: f.db, blocked: testEntryID(blocked)}

	if w := f.do(http.MethodGet, blocked); w.Code != http.StatusForbidden {
		t.Errorf("quarantined by entry id = %d, want 403", w.Code)
	}

	if w := f.do(http.MethodGet, sibling); w.Code != http.StatusOK {
		t.Errorf("sibling = %d, want 200", w.Code)
	}
}
