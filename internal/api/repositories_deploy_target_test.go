// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// deployFixture is the maven group "grp" holding the hosted and proxy members
// of the repoOps fixture, plus a deb group whose format has no deploy target.
func deployFixture() *repoOpsDB {
	db := newRepoOpsDB()
	acme := testAcmeID

	db.members["id-grp"] = []string{"id-hosted", "id-proxy"}
	db.repos["deb-hosted"] = &models.Repository{ID: "id-deb", Name: "deb-hosted", Format: "deb", RepoType: "hosted", ProjectID: &acme}
	db.repos["deb-grp"] = &models.Repository{ID: "id-debgrp", Name: "deb-grp", Format: "deb", RepoType: "group", ProjectID: &acme}
	db.members["id-debgrp"] = []string{"id-deb"}

	return db
}

func TestRepositoryUpdateDeployTarget(t *testing.T) {
	tests := []struct {
		name      string
		repo      string
		body      string
		setup     func(*repoOpsDB)
		want      int
		wantSet   bool    // the update writes the target
		wantID    *string // ... to this repository id (nil = cleared)
		wantReply string  // deploy_target in the answer
	}{
		{"a hosted member", "grp", `{"deploy_target":"hosted"}`, nil, http.StatusOK, true, new("id-hosted"), "hosted"},
		{
			"cleared with an empty name", "grp", `{"deploy_target":""}`,
			func(d *repoOpsDB) {
				d.repos["grp"].DeployTargetID, d.repos["grp"].DeployTarget = new("id-hosted"), "hosted"
			},
			http.StatusOK, true, nil, "",
		},
		{
			"absent leaves it", "grp", `{"description":"x"}`,
			func(d *repoOpsDB) {
				d.repos["grp"].DeployTargetID, d.repos["grp"].DeployTarget = new("id-hosted"), "hosted"
			},
			http.StatusOK, false, nil, "hosted",
		},
		{
			"member and target in one update", "grp", `{"member_repos":["sys-maven","proxy"],"deploy_target":"sys-maven"}`, nil,
			http.StatusOK, true, new("id-sys"), "sys-maven",
		},
		{"a proxy member is no target", "grp", `{"deploy_target":"proxy"}`, nil, http.StatusBadRequest, false, nil, ""},
		{"a repository outside the group", "grp", `{"deploy_target":"sys-maven"}`, nil, http.StatusBadRequest, false, nil, ""},
		{
			"a target the new member list drops",
			"grp",
			`{"member_repos":["proxy"],"deploy_target":"hosted"}`,
			nil,
			http.StatusBadRequest,
			false,
			nil,
			"",
		},
		{"an unknown repository", "grp", `{"deploy_target":"ghost"}`, nil, http.StatusBadRequest, false, nil, ""},
		{"a format without deploy support", "deb-grp", `{"deploy_target":"deb-hosted"}`, nil, http.StatusBadRequest, false, nil, ""},
		{"a hosted repository has none", "hosted", `{"deploy_target":"hosted"}`, nil, http.StatusBadRequest, false, nil, ""},
		{
			"dropping the target from the members clears it", "grp", `{"member_repos":["proxy"]}`,
			func(d *repoOpsDB) {
				d.repos["grp"].DeployTargetID, d.repos["grp"].DeployTarget = new("id-hosted"), "hosted"
			},
			http.StatusOK, false, nil, "",
		},
		{
			"keeping the target among the members keeps it", "grp", `{"member_repos":["proxy","hosted"]}`,
			func(d *repoOpsDB) {
				d.repos["grp"].DeployTargetID, d.repos["grp"].DeployTarget = new("id-hosted"), "hosted"
			},
			http.StatusOK, false, nil, "hosted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := deployFixture()
			if tt.setup != nil {
				tt.setup(db)
			}

			code, body := projectJSON(t, db.router(t), uAdmin1, http.MethodPatch, "/api/v1/repository/"+tt.repo, tt.body)
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if tt.want != http.StatusOK {
				if db.updated != nil {
					t.Error("a refused update wrote the repository")
				}

				return
			}

			if db.change.SetDeployTarget != tt.wantSet {
				t.Errorf("SetDeployTarget = %v, want %v", db.change.SetDeployTarget, tt.wantSet)
			}

			if tt.wantSet && !equalStrPtr(db.change.DeployTargetID, tt.wantID) {
				t.Errorf("DeployTargetID = %v, want %v", deref(db.change.DeployTargetID), deref(tt.wantID))
			}

			if got, _ := body["deploy_target"].(string); got != tt.wantReply {
				t.Errorf("answer deploy_target = %q, want %q", got, tt.wantReply)
			}
		})
	}
}

func TestRepositoryCreateDeployTarget(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{
			"hosted member",
			`{"name":"g2","format":"maven","repo_type":"group","member_repos":["hosted"],"deploy_target":"hosted"}`,
			http.StatusCreated,
		},
		{
			"not a member",
			`{"name":"g2","format":"maven","repo_type":"group","member_repos":["proxy"],"deploy_target":"hosted"}`,
			http.StatusBadRequest,
		},
		{
			"proxy member",
			`{"name":"g2","format":"maven","repo_type":"group","member_repos":["proxy"],"deploy_target":"proxy"}`,
			http.StatusBadRequest,
		},
		{
			"unsupported format",
			`{"name":"g2","format":"deb","repo_type":"group","member_repos":["deb-hosted"],"deploy_target":"deb-hosted"}`,
			http.StatusBadRequest,
		},
		{"hosted repository", `{"name":"h2","format":"maven","repo_type":"hosted","deploy_target":"hosted"}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := deployFixture()

			code, body := projectJSON(t, db.router(t), uAdmin1, http.MethodPost, "/api/v1/repositories", tt.body)
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}
		})
	}
}

func equalStrPtr(a, b *string) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}
