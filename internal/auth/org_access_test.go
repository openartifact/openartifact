// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	orgA = "11111111-1111-1111-1111-111111111111"
	orgB = "22222222-2222-2222-2222-222222222222"
)

// orgMockDB serves one user's memberships, targets and a fixed repository set.
type orgMockDB struct {
	database.BaseMockDB

	grants  []*models.OrgGrant
	targets []*models.PermissionTarget
	repos   map[string]*models.Repository
}

func (m *orgMockDB) ListOrgGrantsByUser(_ context.Context, _ string) ([]*models.OrgGrant, error) {
	return m.grants, nil
}

func (m *orgMockDB) GetPermissionTargetsForPrincipal(_ context.Context, _ string, _ []string) ([]*models.PermissionTarget, error) {
	return m.targets, nil
}

func (m *orgMockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return m.repos[name], nil
}

func newOrgDB(grants ...*models.OrgGrant) *orgMockDB {
	a, b, sys := orgA, orgB, models.SystemOrgID

	return &orgMockDB{
		grants: grants,
		repos: map[string]*models.Repository{
			"in-a":   {Name: "in-a", OrgID: &a},
			"in-b":   {Name: "in-b", OrgID: &b},
			"in-sys": {Name: "in-sys", OrgID: &sys},
			"no-org": {Name: "no-org"},
		},
	}
}

func orgUser() Principal {
	id := "user-1"

	return Principal{UserID: &id, Username: "u", Credential: CredentialSession}
}

func allowedActions(t *testing.T, e *Evaluator, p Principal, repo string) map[string]bool {
	t.Helper()

	got := map[string]bool{}

	for _, action := range []string{"read", "write", "delete", "manage", "annotate"} {
		ok, err := e.Evaluate(context.Background(), p, repo, "", action)
		if err != nil {
			t.Fatalf("Evaluate(%s, %s): %v", repo, action, err)
		}

		got[action] = ok
	}

	return got
}

func wantActions(actions ...string) map[string]bool {
	m := map[string]bool{"read": false, "write": false, "delete": false, "manage": false, "annotate": false}
	for _, a := range actions {
		m[a] = true
	}

	return m
}

func equalActions(a, b map[string]bool) bool {
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}

	for k, v := range b {
		if a[k] != v {
			return false
		}
	}

	return true
}

func TestOrgAccessMemberRoleActions(t *testing.T) {
	tests := []struct {
		name       string
		role       string
		memberRole string
		want       map[string]bool
	}{
		{"member read", "member", "read", wantActions("read")},
		{"member write", "member", "write", wantActions("read", "write")},
		{"member admin", "member", "admin", wantActions("read", "write", "delete")},
		{"org admin role with read member_role", "admin", "read", wantActions("read", "write", "delete")},
		{"org owner role with read member_role", "owner", "read", wantActions("read", "write", "delete")},
		{"unknown member_role grants nothing", "member", "bogus", wantActions()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newOrgDB(&models.OrgGrant{OrgID: orgA, Role: tt.role, MemberRole: tt.memberRole})
			e := NewEvaluator(db, nil, false)

			if got := allowedActions(t, e, orgUser(), "in-a"); !equalActions(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOrgAccessDeniedCases(t *testing.T) {
	scoped := orgUser()
	scoped.TokenKind, scoped.TokenScopes = "access", []string{"some-target"}

	unscoped := orgUser()
	unscoped.TokenKind = "access"

	deploy := orgUser()
	deploy.TokenKind = TokenKindDeploy

	tests := []struct {
		name      string
		grants    []*models.OrgGrant
		principal Principal
		repo      string
		want      map[string]bool
	}{
		{"non-member", nil, orgUser(), "in-a", wantActions()},
		{"other org does not leak", []*models.OrgGrant{{OrgID: orgA, Role: "owner", MemberRole: "admin"}}, orgUser(), "in-b", wantActions()},
		{"system org grants nothing", []*models.OrgGrant{{OrgID: models.SystemOrgID, Role: "owner", MemberRole: "admin"}}, orgUser(), "in-sys", wantActions()},
		{"repo without org", []*models.OrgGrant{{OrgID: orgA, Role: "owner", MemberRole: "admin"}}, orgUser(), "no-org", wantActions()},
		{"unknown repo", []*models.OrgGrant{{OrgID: orgA, Role: "owner", MemberRole: "admin"}}, orgUser(), "ghost", wantActions()},
		{"scoped token gets no org grant", []*models.OrgGrant{{OrgID: orgA, Role: "owner", MemberRole: "admin"}}, scoped, "in-a", wantActions()},
		{"anonymous gets no org grant", []*models.OrgGrant{{OrgID: orgA, Role: "owner", MemberRole: "admin"}}, Principal{IsAnonymous: true}, "in-a", wantActions()},
		{"unscoped token inherits", []*models.OrgGrant{{OrgID: orgA, Role: "member", MemberRole: "write"}}, unscoped, "in-a", wantActions("read", "write")},
		{"deploy token inherits repo actions, never manage", []*models.OrgGrant{{OrgID: orgA, Role: "owner", MemberRole: "admin"}}, deploy, "in-a", wantActions("read", "write", "delete")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewEvaluator(newOrgDB(tt.grants...), nil, false)

			if got := allowedActions(t, e, tt.principal, tt.repo); !equalActions(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOrgAccessUnionWithPermissionTargets(t *testing.T) {
	db := newOrgDB(&models.OrgGrant{OrgID: orgA, Role: "member", MemberRole: "read"})
	db.targets = []*models.PermissionTarget{{
		ID: "t1", Name: "deployers", RepositoryFilter: "in-.*", PathPattern: ".*", Actions: []string{"write", "annotate"},
	}}
	e := NewEvaluator(db, nil, false)

	// in-a: org read + target write/annotate; in-b: target only; in-sys: target only.
	tests := []struct {
		repo string
		want map[string]bool
	}{
		{"in-a", wantActions("read", "write", "annotate")},
		{"in-b", wantActions("write", "annotate")},
		{"in-sys", wantActions("write", "annotate")},
	}

	for _, tt := range tests {
		t.Run(tt.repo, func(t *testing.T) {
			if got := allowedActions(t, e, orgUser(), tt.repo); !equalActions(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRepoReadFilterAppliesOrgGrants(t *testing.T) {
	scoped := orgUser()
	scoped.TokenScopes = []string{"x"}

	tests := []struct {
		name      string
		grants    []*models.OrgGrant
		principal Principal
		want      map[string]bool
	}{
		{"member sees own org only", []*models.OrgGrant{{OrgID: orgA, Role: "member", MemberRole: "read"}}, orgUser(),
			map[string]bool{"in-a": true, "in-b": false, "in-sys": false, "no-org": false}},
		{"system org membership shows nothing", []*models.OrgGrant{{OrgID: models.SystemOrgID, Role: "owner", MemberRole: "admin"}}, orgUser(),
			map[string]bool{"in-a": false, "in-sys": false}},
		{"scoped token sees nothing from orgs", []*models.OrgGrant{{OrgID: orgA, Role: "owner", MemberRole: "admin"}}, scoped,
			map[string]bool{"in-a": false}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newOrgDB(tt.grants...)
			e := NewEvaluator(db, nil, false)

			filter, err := e.RepoReadFilter(context.Background(), tt.principal)
			if err != nil {
				t.Fatal(err)
			}

			for name, want := range tt.want {
				got, err := filter(db.repos[name])
				if err != nil {
					t.Fatal(err)
				}

				if got != want {
					t.Errorf("%s: got %v, want %v", name, got, want)
				}
			}
		})
	}
}
