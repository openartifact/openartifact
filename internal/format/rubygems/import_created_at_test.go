// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubygems

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestPushHonorsImportCreatedAt: a push carrying an origin creation time (as
// importmeta resolves it for a manage principal) stores it as created_at; an
// ordinary push leaves it unset so the database stamps now.
func TestPushHonorsImportCreatedAt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		at, want time.Time
	}{
		{"imported", formattest.Origin(), formattest.Origin()},
		{"normal upload", time.Time{}, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, db, _, _ := newTestHandler(t)
			gem := createGemFile(t, map[string]any{"name": "demo", "version": "1.0.0"})

			w := formattest.Do(formattest.AtOrigin(h.RegisterRepoRoutes, tc.at),
				formattest.Request(http.MethodPost, "/repos/myrepo/api/v1/gems", bytes.NewReader(gem)))
			if w.Code != http.StatusCreated {
				t.Fatalf("push status = %d: %s", w.Code, w.Body.String())
			}

			if len(db.gems) != 1 || !db.gems[0].CreatedAt.Equal(tc.want) {
				t.Fatalf("gems = %+v, want created_at %v", db.gems, tc.want)
			}
		})
	}
}
