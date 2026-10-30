// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package federation

import (
	"context"
	"net/http"
	"testing"
)

// TestEngineUnitQuarantinedByRowID: a scan recorded under the unit's row id (as
// the UI, the license policies and the OSV worker do) refuses the unit to a
// peer, and a sibling unit is still served.
func TestEngineUnitQuarantinedByRowID(t *testing.T) {
	a := newEngSite(t, "site-a", "raw")

	a.put(t, "ok", "fine")
	a.put(t, "secret.bin", "payload")

	var id string
	if err := a.db.QueryRowContext(context.Background(),
		`SELECT id::text FROM package_files WHERE repository_id = $1::uuid AND unit_key = $2 AND deleted_at IS NULL`,
		a.repoID, "secret.bin").Scan(&id); err != nil {
		t.Fatalf("row id of the unit: %v", err)
	}

	for _, key := range []string{"ok", "secret.bin"} {
		if status, _, body := engGet(t, a.srv.URL, engUnitPath+"?key="+key, engBearer(a.token)); status != http.StatusOK {
			t.Fatalf("%s before the quarantine: %d %s, want 200", key, status, body)
		}
	}

	a.setQuarantine(t, id, true) // the scan path column holds the row id, not a path

	status, _, body := engGet(t, a.srv.URL, engUnitPath+"?key=secret.bin", engBearer(a.token))
	if status != http.StatusLocked || engErrCode(t, body) != codeQuarantined {
		t.Errorf("quarantined by row id: %d %s, want 423 %s", status, body, codeQuarantined)
	}

	if status, _, body := engGet(t, a.srv.URL, engUnitPath+"?key=ok", engBearer(a.token)); status != http.StatusOK {
		t.Errorf("sibling unit: %d %s, want 200", status, body)
	}
}
