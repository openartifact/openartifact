// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"net/http"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestPublishHonorsImportCreatedAt: a publish carrying an origin creation time
// (as importmeta resolves it for a manage principal) stores it as created_at;
// an ordinary publish leaves it unset so the database stamps now.
func TestPublishHonorsImportCreatedAt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		at, want time.Time
	}{
		{"imported", formattest.Origin(), formattest.Origin()},
		{"normal upload", time.Time{}, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, db, _, _ := newCargoHandler(t)

			body := publishBody(`{"name":"demo","vers":"0.1.0"}`, []byte("crate-bytes"))

			w := formattest.Do(formattest.AtOrigin(h.RegisterRepoRoutes, tc.at),
				formattest.Request(http.MethodPut, "/repos/myrepo/api/v1/crates/new", body))
			if w.Code != http.StatusOK {
				t.Fatalf("publish status = %d: %s", w.Code, w.Body.String())
			}

			if len(db.crates) != 1 || !db.crates[0].CreatedAt.Equal(tc.want) {
				t.Fatalf("crates = %+v, want created_at %v", db.crates, tc.want)
			}
		})
	}
}
