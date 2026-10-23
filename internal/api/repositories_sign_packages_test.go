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

func TestCreateSignPackages(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"rpm hosted", `{"name":"r","format":"rpm","sign_packages":true}`, http.StatusCreated},
		{"rpm hosted false", `{"name":"r","format":"rpm","sign_packages":false}`, http.StatusCreated},
		{"deb hosted", `{"name":"d","format":"deb","sign_packages":true}`, http.StatusBadRequest},
		{"deb hosted false", `{"name":"d","format":"deb","sign_packages":false}`, http.StatusCreated},
		{"rpm group", `{"name":"g","format":"rpm","repo_type":"group","sign_packages":true}`, http.StatusBadRequest},
		{"rpm proxy", `{"name":"p","format":"rpm","repo_type":"proxy","upstream_url":"https://93.184.216.34/","sign_packages":true}`, http.StatusBadRequest},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := newProxyGroupDB()

			rw := createRepo(t, db, c.body)
			if rw.Code != c.want {
				t.Fatalf("status %d, want %d: %s", rw.Code, c.want, rw.Body.String())
			}

			if c.want == http.StatusCreated {
				if want := strings.Contains(c.body, `"sign_packages":true`); db.created.SignPackages != want {
					t.Errorf("SignPackages = %v, want %v", db.created.SignPackages, want)
				}
			} else if db.created != nil {
				t.Error("repository was created despite the 400")
			}
		})
	}
}

func TestPatchSignPackages(t *testing.T) {
	cases := []struct {
		name string
		repo *models.Repository
		body string
		want int
	}{
		{"rpm hosted on", &models.Repository{ID: "1", Name: "g", Format: "rpm", RepoType: "hosted"}, `{"sign_packages":true}`, http.StatusOK},
		{"rpm hosted off", &models.Repository{ID: "2", Name: "g", Format: "rpm", RepoType: "hosted", SignPackages: true}, `{"sign_packages":false}`, http.StatusOK},
		{"deb hosted on", &models.Repository{ID: "3", Name: "g", Format: "deb", RepoType: "hosted"}, `{"sign_packages":true}`, http.StatusBadRequest},
		{"deb hosted off", &models.Repository{ID: "4", Name: "g", Format: "deb", RepoType: "hosted"}, `{"sign_packages":false}`, http.StatusOK},
		{"rpm group on", &models.Repository{ID: "5", Name: "g", Format: "rpm", RepoType: "group"}, `{"sign_packages":true}`, http.StatusBadRequest},
		{"rpm proxy on", &models.Repository{ID: "6", Name: "g", Format: "rpm", RepoType: "proxy", UpstreamURL: "https://93.184.216.34/"}, `{"sign_packages":true}`, http.StatusBadRequest},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rw, updated := patchGroupRepo(t, c.repo, c.body)
			if rw.Code != c.want {
				t.Fatalf("status %d, want %d: %s", rw.Code, c.want, rw.Body.String())
			}

			if c.want == http.StatusOK {
				if want := strings.Contains(c.body, "true"); updated == nil || updated.SignPackages != want {
					t.Errorf("updated %+v, want SignPackages=%v", updated, want)
				}
			} else if updated != nil {
				t.Error("repository was updated despite the 400")
			}
		})
	}
}
