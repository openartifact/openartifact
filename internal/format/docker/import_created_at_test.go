// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/importmeta"
)

// TestPutManifestHonorsImportCreatedAt: pushing by tag stamps both the manifest
// row and the tag row with the origin creation time; pushing by digest stamps
// the manifest row only (there is no tag). An ordinary push leaves created_at
// unset so the database stamps now.
func TestPutManifestHonorsImportCreatedAt(t *testing.T) {
	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	for _, tc := range []struct {
		name      string
		reference string
		at, want  time.Time
		wantTag   bool
	}{
		{"imported by tag", "latest", formattest.Origin(), formattest.Origin(), true},
		{"imported by digest", digest, formattest.Origin(), formattest.Origin(), false},
		{"normal push by tag", "latest", time.Time{}, time.Time{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newManifestFakeDB("repo-1")
			h, _ := newTestHandler(t, db)

			ctx := context.Background()
			if !tc.at.IsZero() {
				ctx = importmeta.WithCreatedAt(ctx, tc.at)
			}

			req := withChiVars(
				httptest.NewRequestWithContext(ctx, http.MethodPut, "/v2/myrepo/manifests/"+tc.reference, bytes.NewReader(body)),
				map[string]string{"name": "myrepo", "reference": tc.reference},
			)
			req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")

			rec := httptest.NewRecorder()
			h.handlePutManifest(rec, req)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}

			m := db.manifest(digest)
			if m == nil || !m.CreatedAt.Equal(tc.want) {
				t.Fatalf("manifest = %+v, want created_at %v", m, tc.want)
			}

			tag := db.tags[scoped("", "latest")]
			if !tc.wantTag {
				if len(db.tags) != 0 {
					t.Fatalf("tags = %v, want none for a push by digest", db.tags)
				}

				return
			}

			if tag == nil || !tag.CreatedAt.Equal(tc.want) {
				t.Fatalf("tag = %+v, want created_at %v", tag, tc.want)
			}
		})
	}
}
