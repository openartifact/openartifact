// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"net/http"
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

			w := formattest.Do(formattest.AtOrigin(register(h), tc.at), uploadRequest(t, "demo_pkg-1.0.tar.gz", []byte("payload")))
			if w.Code != http.StatusOK {
				t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
			}

			if len(db.pkgs) != 1 || !db.pkgs[0].CreatedAt.Equal(tc.want) {
				t.Fatalf("pkgs = %+v, want created_at %v", db.pkgs, tc.want)
			}
		})
	}
}
