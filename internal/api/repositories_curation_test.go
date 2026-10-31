// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/database/models"
)

func TestCreateCurationMode(t *testing.T) {
	const proxy = `"repo_type":"proxy","upstream_url":"https://93.184.216.34/"`

	cases := []struct {
		name     string
		body     string
		wantCode int
		wantMode string
	}{
		{"proxy denylist", `{"name":"p","format":"npm",` + proxy + `,"curation_mode":"denylist"}`, http.StatusCreated, "denylist"},
		{"proxy allowlist", `{"name":"p","format":"npm",` + proxy + `,"curation_mode":"allowlist"}`, http.StatusCreated, "allowlist"},
		{"proxy off", `{"name":"p","format":"npm",` + proxy + `,"curation_mode":"off"}`, http.StatusCreated, "off"},
		{"proxy absent is off", `{"name":"p","format":"npm",` + proxy + `}`, http.StatusCreated, "off"},
		{"proxy unknown mode", `{"name":"p","format":"npm",` + proxy + `,"curation_mode":"greylist"}`, http.StatusBadRequest, ""},
		{"proxy empty mode", `{"name":"p","format":"npm",` + proxy + `,"curation_mode":""}`, http.StatusBadRequest, ""},
		{"hosted rejected", `{"name":"h","format":"npm","curation_mode":"denylist"}`, http.StatusBadRequest, ""},
		{"group rejected", `{"name":"g","format":"npm","repo_type":"group","curation_mode":"denylist"}`, http.StatusBadRequest, ""},
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

			if db.created.CurationMode != tc.wantMode {
				t.Errorf("CurationMode = %q, want %q", db.created.CurationMode, tc.wantMode)
			}

			if want := `"curation_mode":"` + tc.wantMode + `"`; !strings.Contains(rw.Body.String(), want) {
				t.Errorf("proxy response lacks %s: %s", want, rw.Body.String())
			}
		})
	}
}

func TestPatchCurationMode(t *testing.T) {
	proxyRepo := func(mode string) *models.Repository {
		return &models.Repository{
			ID: "p1", Name: "g", Format: "npm", RepoType: "proxy", UpstreamURL: "https://93.184.216.34/", CurationMode: mode,
		}
	}

	cases := []struct {
		name     string
		repo     *models.Repository
		body     string
		wantCode int
		wantMode string
	}{
		{"set denylist", proxyRepo(""), `{"curation_mode":"denylist"}`, http.StatusOK, "denylist"},
		{"switch to allowlist", proxyRepo("denylist"), `{"curation_mode":"allowlist"}`, http.StatusOK, "allowlist"},
		{"back to off", proxyRepo("allowlist"), `{"curation_mode":"off"}`, http.StatusOK, "off"},
		{"untouched by other fields", proxyRepo("denylist"), `{"description":"x"}`, http.StatusOK, "denylist"},
		{"unknown mode", proxyRepo("denylist"), `{"curation_mode":"greylist"}`, http.StatusBadRequest, ""},
		{"empty mode", proxyRepo("denylist"), `{"curation_mode":""}`, http.StatusBadRequest, ""},
		{
			"hosted rejected",
			&models.Repository{ID: "h1", Name: "g", Format: "npm", RepoType: "hosted"},
			`{"curation_mode":"denylist"}`, http.StatusBadRequest, "",
		},
		{
			"group rejected",
			&models.Repository{ID: "g1", Name: "g", Format: "npm", RepoType: "group"},
			`{"curation_mode":"denylist"}`, http.StatusBadRequest, "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rw, updated := patchGroupRepo(t, tc.repo, tc.body)
			if rw.Code != tc.wantCode {
				t.Fatalf("status %d, want %d: %s", rw.Code, tc.wantCode, rw.Body.String())
			}

			if tc.wantCode != http.StatusOK {
				if updated != nil {
					t.Fatalf("repository was updated despite the error: %+v", updated)
				}

				return
			}

			if updated == nil || updated.CurationMode != tc.wantMode {
				t.Fatalf("updated = %+v, want mode %q", updated, tc.wantMode)
			}

			if want := `"curation_mode":"` + tc.wantMode + `"`; !strings.Contains(rw.Body.String(), want) {
				t.Errorf("response lacks %s: %s", want, rw.Body.String())
			}
		})
	}
}

// Changing the mode is audited with the old and the new value; a PATCH that
// leaves it as it was is not an extra event.
func TestPatchCurationModeAudit(t *testing.T) {
	rows := recordAudit(t)

	repo := func(mode string) *models.Repository {
		return &models.Repository{
			ID: "p1", Name: "g", Format: "npm", RepoType: "proxy", UpstreamURL: "https://93.184.216.34/", CurationMode: mode,
		}
	}

	patchGroupRepo(t, repo(""), `{"curation_mode":"denylist"}`)
	patchGroupRepo(t, repo("denylist"), `{"curation_mode":"denylist"}`)
	patchGroupRepo(t, repo("denylist"), `{"curation_mode":"allowlist"}`)

	got := wantAudit(t, rows(), audit.ActionRepositoryUpdated, audit.ActionRepositoryUpdated)
	wantMessage(t, got[0], "from off to denylist")
	wantMessage(t, got[1], "from denylist to allowlist")
}
