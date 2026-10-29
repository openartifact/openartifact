// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/base64"
	"net/http"
	"slices"
	"testing"
	"time"
)

// TestNPMHandlersRejectOtherFormats: the shared PUT and DELETE routes reach
// every hosted repository, so a writer on a pypi repository must not be able
// to publish, unpublish or retag npm packages in it. The same requests are
// served on an npm repository.
func TestNPMHandlersRejectOtherFormats(t *testing.T) {
	payload := []byte("tarball-bytes")
	publish := npmBody("fresh", "1.0.0", base64.StdEncoding.EncodeToString(payload), len(payload), "data-middle")

	tests := []struct {
		name   string
		method string
		target string
		body   string
	}{
		{"publish", http.MethodPut, "/repos/myrepo/fresh", publish},
		{"rev put", http.MethodPut, "/repos/myrepo/demo/-rev/1-x", npmRevBody("demo", nil, "1.0.0")},
		{"unpublish version", http.MethodDelete, "/repos/myrepo/demo/-/demo-1.0.0.tgz/-rev/1-x", ""},
		{"unpublish package", http.MethodDelete, "/repos/myrepo/demo/-rev/1-x", ""},
		{"put dist-tag", http.MethodPut, "/repos/myrepo/-/package/demo/dist-tags/beta", `"2.0.0"`},
		{"delete dist-tag", http.MethodDelete, "/repos/myrepo/-/package/demo/dist-tags/stable", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNPMEnv(t)
			demo := e.db.addPackage(npmTestRepoID, "demo", time.Hour, map[string]string{"latest": "1.0.0", "stable": "1.0.0"}, "1.0.0", "2.0.0")

			e.db.Repo.Format = "pypi"

			rw := e.do(npmUser(), tt.method, tt.target, tt.body)
			if rw.Code != http.StatusNotFound {
				t.Fatalf("status on a pypi repository = %d, want 404; body = %s", rw.Code, rw.Body.String())
			}

			if got := e.db.liveVersions(demo.ID); !slices.Equal(got, []string{"1.0.0", "2.0.0"}) {
				t.Errorf("versions = %v, want them untouched", got)
			}

			if got := e.db.tagsOf(demo.ID); !mapsEqual(got, map[string]string{"latest": "1.0.0", "stable": "1.0.0"}) {
				t.Errorf("tags = %v, want them untouched", got)
			}

			if len(e.db.pkgs) != 1 || len(e.blobs.Blobs) != 0 || len(e.db.deletedPkgs) != 0 || len(e.events.Events()) != 0 {
				t.Errorf("a rejected request left pkgs=%d blobs=%d deleted=%d events=%d",
					len(e.db.pkgs), len(e.blobs.Blobs), len(e.db.deletedPkgs), len(e.events.Events()))
			}

			e.db.Repo.Format = "npm"

			if rw := e.do(npmUser(), tt.method, tt.target, tt.body); rw.Code == http.StatusNotFound {
				t.Fatalf("status on an npm repository = 404, the guard must only reject other formats; body = %s", rw.Body.String())
			}
		})
	}
}
