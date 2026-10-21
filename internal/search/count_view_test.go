// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"strings"
	"testing"
)

// The count view must drop the blobs join from every branch while keeping the
// set of branches (and therefore the row set) identical to the full view.
func TestCountSearchView_DropsBlobJoinKeepsBranches(t *testing.T) {
	full, lean := unifiedSearchView(), countSearchView()

	if !strings.Contains(full, "JOIN blobs b") {
		t.Fatal("full view no longer joins blobs; the count optimisation is stale")
	}

	if strings.Contains(lean, "JOIN blobs b") || strings.Contains(lean, "b.sha256") || strings.Contains(lean, "b.size_bytes") {
		t.Fatalf("count view still references the blobs join:\n%s", lean)
	}

	if got, want := strings.Count(lean, "UNION ALL"), strings.Count(full, "UNION ALL"); got != want {
		t.Fatalf("count view has %d branches, full view has %d", got+1, want+1)
	}

	for _, format := range []string{"'maven'", "'npm'", "'deb'", "'rpm'", "'docker'", "'pypi'"} {
		if !strings.Contains(lean, format) {
			t.Errorf("count view lost the %s branch", format)
		}
	}
}
