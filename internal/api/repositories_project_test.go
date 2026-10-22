// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestCheckProjectRepoPolicy(t *testing.T) {
	project := &models.Project{
		AllowedFormats:   []string{"npm", "maven"},
		AllowedRepoTypes: []string{"hosted", "group"},
		RepoNamePrefix:   "acme-",
	}
	open := &models.Project{AllowedRepoTypes: []string{"hosted"}, AllowPublicRepos: true}

	tests := []struct {
		name     string
		project  *models.Project
		repoName string
		format   string
		repoType string
		public   bool
		want     int
	}{
		{"compliant", project, "acme-web", "npm", "hosted", false, 0},
		{"compliant group", project, "acme-g", "maven", "group", false, 0},
		{"format not allowed", project, "acme-web", "docker", "hosted", false, http.StatusBadRequest},
		{"type not allowed", project, "acme-web", "npm", "proxy", false, http.StatusBadRequest},
		{"prefix missing", project, "web", "npm", "hosted", false, http.StatusBadRequest},
		{"prefix is case sensitive", project, "ACME-web", "npm", "hosted", false, http.StatusBadRequest},
		{"public not allowed", project, "acme-web", "npm", "hosted", true, http.StatusForbidden},
		{"nil allowed_formats allows all", open, "x", "cargo", "hosted", false, 0},
		{"public allowed", open, "x", "cargo", "hosted", true, 0},
		{"empty prefix allows any name", open, "", "cargo", "hosted", false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, code, msg := checkProjectRepoPolicy(tt.project, tt.repoName, tt.format, tt.repoType, tt.public)
			if status != tt.want {
				t.Fatalf("status = %d (%s: %s), want %d", status, code, msg, tt.want)
			}

			if (status == 0) != (code == "" && msg == "") {
				t.Fatalf("code/message must be set exactly on a violation: %q %q", code, msg)
			}
		})
	}
}
