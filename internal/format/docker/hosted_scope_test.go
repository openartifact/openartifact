// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"net/http"
	"strings"
	"testing"
)

// The registry handler serves hosted docker repositories only: a repository of
// another format, or a proxy/group one, must not take blobs or manifests.
func TestRegistry_WritesAreRefusedOutsideHostedDocker(t *testing.T) {
	db := newRegFakeDB("npmrepo", "proxyrepo", "okrepo")
	db.repos["npmrepo"].Format = "npm"
	db.repos["proxyrepo"].RepoType = "proxy"

	g := newRegistry(t, db)

	for _, name := range []string{"npmrepo", "proxyrepo"} {
		rec := g.do(http.MethodPost, "/v2/"+name+"/app/blobs/uploads/", "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: upload start = %d, want 404", name, rec.Code)
		}

		if len(db.uploads) != 0 {
			t.Errorf("%s: an upload session was created", name)
		}
	}

	if rec := g.do(http.MethodPost, "/v2/okrepo/app/blobs/uploads/", ""); rec.Code != http.StatusAccepted {
		t.Errorf("hosted docker: upload start = %d, want 202", rec.Code)
	}
}

// A writer on a full repository cannot stage layer bytes the quota refuses:
// the PATCH is cut off with 507 and the session is discarded.
func TestRegistry_PatchBeyondQuotaIsRefusedAndDiscarded(t *testing.T) {
	g, db := quotaRegistry(t, 16, "reg")

	start := g.do(http.MethodPost, "/v2/reg/app/blobs/uploads/", "")
	if start.Code != http.StatusAccepted {
		t.Fatalf("upload start = %d", start.Code)
	}

	loc := start.Header().Get("Location")

	rec := g.do(http.MethodPatch, loc, strings.Repeat("x", 64))
	assertDenied(t, rec)

	if len(db.uploads) != 0 {
		t.Errorf("upload sessions left = %d, want 0", len(db.uploads))
	}
}
