// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestCreateMinAgeHours(t *testing.T) {
	const proxy = `"repo_type":"proxy","upstream_url":"https://93.184.216.34/"`

	cases := []struct {
		name     string
		body     string
		wantCode int
		wantAge  int
	}{
		{"proxy set", `{"name":"p","format":"npm",` + proxy + `,"min_age_hours":72}`, http.StatusCreated, 72},
		{"proxy absent is off", `{"name":"p","format":"npm",` + proxy + `}`, http.StatusCreated, 0},
		{"proxy zero", `{"name":"p","format":"npm",` + proxy + `,"min_age_hours":0}`, http.StatusCreated, 0},
		{"proxy max", `{"name":"p","format":"npm",` + proxy + `,"min_age_hours":87600}`, http.StatusCreated, 87600},
		{"proxy above max", `{"name":"p","format":"npm",` + proxy + `,"min_age_hours":87601}`, http.StatusBadRequest, 0},
		{"proxy negative", `{"name":"p","format":"npm",` + proxy + `,"min_age_hours":-1}`, http.StatusBadRequest, 0},
		{"hosted rejected", `{"name":"h","format":"npm","min_age_hours":24}`, http.StatusBadRequest, 0},
		{"group rejected", `{"name":"g","format":"npm","repo_type":"group","min_age_hours":24}`, http.StatusBadRequest, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newProxyGroupDB()
			rw := createRepo(t, db, tc.body)

			if rw.Code != tc.wantCode {
				t.Fatalf("status %d, want %d: %s", rw.Code, tc.wantCode, rw.Body.String())
			}

			if tc.wantCode != http.StatusCreated {
				if db.created != nil {
					t.Fatal("repository was created despite the error")
				}

				return
			}

			if db.created.MinAgeHours != tc.wantAge {
				t.Errorf("MinAgeHours = %d, want %d", db.created.MinAgeHours, tc.wantAge)
			}

			if !strings.Contains(rw.Body.String(), `"min_age_hours":`) {
				t.Errorf("proxy response lacks min_age_hours: %s", rw.Body.String())
			}
		})
	}
}

func TestPatchMinAgeHours(t *testing.T) {
	got := patchProxy(t, `{"min_age_hours":48}`)
	if got.MinAgeHours != 48 {
		t.Errorf("MinAgeHours = %d, want 48", got.MinAgeHours)
	}

	got = patchProxy(t, `{"description":"x"}`)
	if got.MinAgeHours != 0 {
		t.Errorf("untouched MinAgeHours = %d, want 0", got.MinAgeHours)
	}

	for _, body := range []string{`{"min_age_hours":-1}`, `{"min_age_hours":87601}`} {
		rw, updated := patchGroupRepo(t, &models.Repository{
			ID: "p1", Name: "g", Format: "npm", RepoType: "proxy", UpstreamURL: "https://93.184.216.34/",
		}, body)
		if rw.Code != http.StatusBadRequest || updated != nil {
			t.Errorf("%s: status %d updated %+v, want 400", body, rw.Code, updated)
		}
	}

	rw, updated := patchGroupRepo(t, &models.Repository{ID: "g1", Name: "g", Format: "npm", RepoType: "group"}, `{"min_age_hours":5}`)
	if rw.Code != http.StatusBadRequest || updated != nil {
		t.Errorf("group: status %d updated %+v, want 400", rw.Code, updated)
	}

	rw, updated = patchGroupRepo(t, &models.Repository{
		ID: "p2", Name: "g", Format: "npm", RepoType: "proxy", UpstreamURL: "https://93.184.216.34/", MinAgeHours: 6,
	}, `{"min_age_hours":0}`)
	if rw.Code != http.StatusOK || updated == nil || updated.MinAgeHours != 0 ||
		!strings.Contains(rw.Body.String(), `"min_age_hours":0`) {
		t.Errorf("clearing: status %d updated %+v body %s", rw.Code, updated, rw.Body.String())
	}
}
