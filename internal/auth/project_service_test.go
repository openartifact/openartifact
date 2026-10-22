// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func serviceToken(project string) Principal {
	p := projUser()
	p.TokenKind = TokenKindDeploy
	p.Credential = CredentialBearerToken
	p.ServiceProjectID = project

	return p
}

// TestServiceAccountAccess: a project token's service account holds the role of
// its own project only, never anything from permission targets, never manage.
func TestServiceAccountAccess(t *testing.T) {
	admin := []*models.PermissionTarget{{ID: "t", RepositoryFilter: ".*", PathPattern: ".*", Actions: []string{"read", "write", "delete", "manage"}}}

	tests := []struct {
		name    string
		grants  []*models.ProjectGrant
		targets []*models.PermissionTarget
		repo    string
		want    map[string]bool
	}{
		{"viewer token reads its project", []*models.ProjectGrant{g(projA, models.ProjectRoleViewer)}, nil, "in-a", wantActions("read")},
		{"developer token writes its project", []*models.ProjectGrant{g(projA, models.ProjectRoleDeveloper)}, nil, "in-a", wantActions("read", "write")},
		{"developer token has nothing in another project", []*models.ProjectGrant{g(projA, models.ProjectRoleDeveloper)}, nil, "in-b", wantActions()},
		{"a stray membership elsewhere is ignored", []*models.ProjectGrant{g(projA, models.ProjectRoleDeveloper), g(projB, models.ProjectRoleOwner)}, nil, "in-b", wantActions()},
		{"admin targets assigned to the service user are ignored", []*models.ProjectGrant{g(projA, models.ProjectRoleViewer)}, admin, "in-a", wantActions("read")},
		{"admin targets give nothing outside the project", nil, admin, "in-b", wantActions()},
		{"admin targets never give manage", nil, admin, "", wantActions()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newProjDB(tc.grants...)
			db.targets = tc.targets

			got := allowedActions(t, NewEvaluator(db, nil, false), serviceToken(projA), tc.repo)
			if !equalActions(got, tc.want) {
				t.Fatalf("actions = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestServiceAccountReadFilter(t *testing.T) {
	db := newProjDB(g(projA, models.ProjectRoleViewer), g(projB, models.ProjectRoleViewer))
	db.targets = []*models.PermissionTarget{{ID: "t", RepositoryFilter: ".*", PathPattern: ".*", Actions: []string{"read"}}}

	filter, err := NewEvaluator(db, nil, false).RepoReadFilter(t.Context(), serviceToken(projA))
	if err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]bool{"in-a": true, "in-b": false, "in-sys": false, "no-project": false} {
		got, err := filter(db.repos[name])
		if err != nil || got != want {
			t.Errorf("filter(%s) = %v, %v; want %v", name, got, err, want)
		}
	}
}

// TestServiceAccountHasNoControlPlane: a deploy token never reaches the
// project control plane or the management API.
func TestServiceAccountHasNoControlPlane(t *testing.T) {
	db := newProjDB(g(projA, models.ProjectRoleDeveloper))
	e := NewEvaluator(db, nil, false)

	if ok, err := e.Evaluate(t.Context(), serviceToken(projA), "", "", "manage"); err != nil || ok {
		t.Fatalf("manage = %v, %v", ok, err)
	}

	roles, err := e.ProjectRoles(t.Context(), serviceToken(projA))
	if err != nil || len(roles) != 0 {
		t.Fatalf("control-plane roles = %v, %v; want none", roles, err)
	}
}
