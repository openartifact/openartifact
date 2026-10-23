// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ivy

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestUploadHonorsImportCreatedAt: an upload carrying an origin creation time
// (as importmeta resolves it for a manage principal) stores it as created_at;
// an ordinary upload leaves it unset so the database stamps now.
func TestUploadHonorsImportCreatedAt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		at, want time.Time
	}{
		{"imported", formattest.Origin(), formattest.Origin()},
		{"normal upload", time.Time{}, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, db, _, _ := newTestHandler(t)

			w := formattest.Do(formattest.AtOrigin(h.RegisterRepoRoutes, tc.at),
				formattest.Request(http.MethodPut, artifactURL, strings.NewReader("data")))
			if w.Code != http.StatusCreated {
				t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
			}

			if db.art == nil || !db.art.CreatedAt.Equal(tc.want) {
				t.Fatalf("art = %+v, want created_at %v", db.art, tc.want)
			}
		})
	}
}
