// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"bytes"
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
			chart := createHelmChart(t, sampleChartYAML)

			w := formattest.Do(formattest.AtOrigin(h.RegisterRepoRoutes, tc.at),
				formattest.Request(http.MethodPost, "/repos/myrepo/charts", bytes.NewReader(chart)))
			if w.Code != http.StatusCreated {
				t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
			}

			stored := db.charts[chartKey("mychart", "1.0.0")]
			if stored == nil || !stored.CreatedAt.Equal(tc.want) {
				t.Fatalf("chart = %+v, want created_at %v", stored, tc.want)
			}
		})
	}
}
