// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestManifestQuarantinedByTagRowID: a scan keyed by a tag's row id (the id the
// image listing shows) blocks the manifest the tag points at, pulled by tag, by
// digest or probed with HEAD, and leaves the manifest of another tag served.
func TestManifestQuarantinedByTagRowID(t *testing.T) {
	db := newEventsDB()
	h, _ := newTestHandler(t, db)

	digests := map[string]string{}

	for tag, body := range map[string]string{
		"latest": `{"schemaVersion":2,"annotations":{"n":"a"}}`,
		"other":  `{"schemaVersion":2,"annotations":{"n":"b"}}`,
	} {
		rec := putManifest(t, h, tag, []byte(body))
		if rec.Code != http.StatusCreated {
			t.Fatalf("push %s = %d: %s", tag, rec.Code, rec.Body.String())
		}

		digests[tag] = rec.Header().Get("Docker-Content-Digest")
		db.tags[scoped("", tag)].ID = "tag-" + tag
	}

	// "METHOD reference" names the request: the helper only knows paths.
	serve := func(call string) int {
		method, reference, _ := strings.Cut(call, " ")
		req := withChiVars(
			httptest.NewRequestWithContext(context.Background(), method, "/v2/myrepo/manifests/"+reference, nil),
			map[string]string{"name": "myrepo", "reference": reference},
		)
		w := httptest.NewRecorder()

		if method == http.MethodHead {
			h.handleHeadManifest(w, req)
		} else {
			h.handleGetManifest(w, req)
		}

		return w.Code
	}

	for _, blocked := range []string{"GET latest", "HEAD latest", "GET " + digests["latest"], "HEAD " + digests["latest"]} {
		t.Run(blocked, func(t *testing.T) {
			formattest.AssertQuarantinedByID(t, &db.Quarantine, "tag-latest", serve, blocked, "GET other")
		})
	}
}
