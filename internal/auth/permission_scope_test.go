// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// targetsWithIDs returns the targets whose ID is in ids.
func targetsWithIDs(targets []*models.PermissionTarget, ids []string) []*models.PermissionTarget {
	var out []*models.PermissionTarget

	for _, t := range targets {
		if slices.Contains(ids, t.ID) {
			out = append(out, t)
		}
	}

	return out
}

// scopeMockDB holds a catalogue of permission targets (all of them, assigned
// or not) and the subset assigned to the owner.
type scopeMockDB struct {
	EvaluatorStore

	all      []*models.PermissionTarget
	assigned []*models.PermissionTarget
}

func (m *scopeMockDB) GetPermissionTargetsForPrincipal(context.Context, string, []string) ([]*models.PermissionTarget, error) {
	return m.assigned, nil
}

func (m *scopeMockDB) GetPermissionTargetsByIDs(_ context.Context, ids []string) ([]*models.PermissionTarget, error) {
	return targetsWithIDs(m.all, ids), nil
}

func (m *scopeMockDB) GetRepositoryByName(context.Context, string) (*models.Repository, error) {
	return nil, nil
}

func (m *scopeMockDB) ListProjectGrantsByUser(context.Context, string) ([]*models.ProjectGrant, error) {
	return nil, nil
}

func scopeTarget(id, repoFilter, pathPattern string, actions ...string) *models.PermissionTarget {
	return &models.PermissionTarget{ID: id, Name: id, RepositoryFilter: repoFilter, PathPattern: pathPattern, Actions: actions}
}

func scopedPrincipal(scopes ...string) Principal {
	uid := "owner"

	return Principal{UserID: &uid, Username: "owner", Credential: CredentialBearerToken, TokenKind: TokenKindAccess, TokenScopes: scopes}
}

func TestEvaluateScopedTokenIsIntersection(t *testing.T) {
	adminAll := scopeTarget("admin-all", ".*", ".*", "read", "write", "delete", "manage")
	ciDocker := scopeTarget("ci-docker", "docker-ci", ".*", "read", "write")
	readOnlyDocker := scopeTarget("ro-docker", "docker-ci", ".*", "read")
	ciMaven := scopeTarget("ci-maven", "maven-.*", ".*", "read", "write")

	type check struct {
		repo, path, action string
		want               bool
	}

	tests := []struct {
		name      string
		all       []*models.PermissionTarget
		assigned  []*models.PermissionTarget
		principal Principal
		checks    []check
	}{
		{
			name:      "unassigned scope target, owner holds admin-all: allowed inside the scope only",
			all:       []*models.PermissionTarget{adminAll, ciDocker},
			assigned:  []*models.PermissionTarget{adminAll},
			principal: scopedPrincipal("ci-docker"),
			checks: []check{
				{"docker-ci", "img/tags", "read", true},
				{"docker-ci", "img/tags", "write", true},
				{"docker-ci", "img/tags", "delete", false},
				{"docker-ci", "img/tags", "manage", false},
				{"maven-central", "a/b", "read", false},
			},
		},
		{
			name:      "owner lacks the right: denied although the scope covers it",
			all:       []*models.PermissionTarget{ciDocker, readOnlyDocker},
			assigned:  []*models.PermissionTarget{readOnlyDocker},
			principal: scopedPrincipal("ci-docker"),
			checks: []check{
				{"docker-ci", "img/tags", "read", true},
				{"docker-ci", "img/tags", "write", false},
			},
		},
		{
			name:      "owner holds nothing: a scope grants nothing by itself",
			all:       []*models.PermissionTarget{ciDocker},
			principal: scopedPrincipal("ci-docker"),
			checks:    []check{{"docker-ci", "img/tags", "read", false}},
		},
		{
			name:      "assigned scope behaves as before",
			all:       []*models.PermissionTarget{ciDocker, ciMaven},
			assigned:  []*models.PermissionTarget{ciDocker, ciMaven},
			principal: scopedPrincipal("ci-docker"),
			checks: []check{
				{"docker-ci", "img/tags", "write", true},
				{"maven-central", "a/b", "read", false},
			},
		},
		{
			name:      "several scopes: any of them may cover the request",
			all:       []*models.PermissionTarget{adminAll, ciDocker, ciMaven},
			assigned:  []*models.PermissionTarget{adminAll},
			principal: scopedPrincipal("ci-docker", "ci-maven"),
			checks: []check{
				{"docker-ci", "x", "write", true},
				{"maven-snapshots", "x", "write", true},
				{"npm-registry", "x", "read", false},
			},
		},
		{
			name:      "deleted scope target grants nothing",
			all:       []*models.PermissionTarget{adminAll},
			assigned:  []*models.PermissionTarget{adminAll},
			principal: scopedPrincipal("ci-docker"),
			checks:    []check{{"docker-ci", "x", "read", false}},
		},
		{
			name:      "deleted scope target next to a live one: only the live one counts",
			all:       []*models.PermissionTarget{adminAll, ciMaven},
			assigned:  []*models.PermissionTarget{adminAll},
			principal: scopedPrincipal("ci-docker", "ci-maven"),
			checks: []check{
				{"docker-ci", "x", "read", false},
				{"maven-central", "x", "read", true},
			},
		},
		{
			name:      "non-scoped principal is unchanged",
			all:       []*models.PermissionTarget{adminAll, ciDocker},
			assigned:  []*models.PermissionTarget{ciDocker},
			principal: scopedPrincipal(),
			checks: []check{
				{"docker-ci", "x", "write", true},
				{"maven-central", "x", "read", false},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewEvaluator(&scopeMockDB{all: tt.all, assigned: tt.assigned}, nil, false)

			for _, c := range tt.checks {
				got, err := e.Evaluate(context.Background(), tt.principal, c.repo, c.path, c.action)
				if err != nil {
					t.Fatal(err)
				}

				if got != c.want {
					t.Errorf("Evaluate(%s, %s, %s) = %v, want %v", c.repo, c.path, c.action, got, c.want)
				}
			}
		})
	}
}

// TestPathReadFilterScopedTokenIsIntersection: the batch read filter applies
// the same rule as Evaluate.
func TestPathReadFilterScopedTokenIsIntersection(t *testing.T) {
	adminAll := scopeTarget("admin-all", ".*", ".*", "read", "write", "delete", "manage")
	ciDocker := scopeTarget("ci-docker", "docker-ci", ".*", "read", "write")
	db := &scopeMockDB{all: []*models.PermissionTarget{adminAll, ciDocker}, assigned: []*models.PermissionTarget{adminAll}}
	e := NewEvaluator(db, nil, false)

	f, err := e.RepoReadFilter(context.Background(), scopedPrincipal("ci-docker"))
	if err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]bool{"docker-ci": true, "maven-central": false} {
		got, err := f(&models.Repository{Name: name})
		if err != nil {
			t.Fatal(err)
		}

		if got != want {
			t.Errorf("read %s = %v, want %v", name, got, want)
		}
	}
}
