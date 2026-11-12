// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !nomcp

package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// depsFixtures are the group members and the stored hosted versions of the
// check_dependencies tests.
type depsFixtures struct {
	members map[string][]string                    // member repository ids by group id, in order
	stored  map[string]map[models.NameVersion]bool // by hosted repository id
}

func (m *mockDB) ListGroupMemberRepositories(_ context.Context, groupID string) ([]*models.Repository, error) {
	var out []*models.Repository

	for _, id := range m.deps.members[groupID] {
		for _, r := range m.repos {
			if r.ID == id {
				out = append(out, r)
			}
		}
	}

	return out, m.err
}

func (m *mockDB) HostedNamesHeld(_ context.Context, repoIDs, names []string) ([]models.HeldName, error) {
	var out []models.HeldName

	for _, id := range repoIDs {
		for i, n := range names {
			for nv := range m.deps.stored[id] {
				if nv.Name == n {
					out = append(out, models.HeldName{Index: i, RepoID: id})
					break
				}
			}
		}
	}

	return out, m.err
}

func (m *mockDB) HostedVersionsStored(_ context.Context, repoIDs []string, versions []models.NameVersion) ([]models.StoredVersion, error) {
	var out []models.StoredVersion

	for _, id := range repoIDs {
		for i, v := range versions {
			if m.deps.stored[id][v] {
				out = append(out, models.StoredVersion{Index: i, RepoID: id})
			}
		}
	}

	return out, m.err
}

// depsStack has the npm group npm-all of hidden (hosted, storing left-pad 9.0.0)
// and web-npm (proxy, whose cached metadata lists left-pad 1.0.0). The read-only
// user may read the group and web-npm, not hidden.
func depsStack(t *testing.T) *stack {
	t.Helper()

	st := newStack(t, nil)
	st.db.repos = append(st.db.repos, &models.Repository{ID: "r-group", Name: "npm-all", Format: "npm", RepoType: "group"})
	st.db.deps = depsFixtures{
		members: map[string][]string{"r-group": {"r-secret", "r-npm"}},
		stored:  map[string]map[models.NameVersion]bool{"r-secret": {{Name: "left-pad", Version: "9.0.0"}: true}},
	}
	st.db.gate.upstream.published = map[string]time.Time{"left-pad@1.0.0": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	st.db.targets["reader"][0].RepositoryFilter = "^(web-npm|images|npm-all)$"

	return st
}

func depResults(t *testing.T, resp toolResponse) []string {
	t.Helper()

	if resp.Result.IsError {
		t.Fatalf("tool error: %s", toolText(t, resp))
	}

	results, _ := resp.Result.StructuredContent["results"].([]any)

	out := make([]string, len(results))
	for i, r := range results {
		m := r.(map[string]any)
		out[i] = m["package"].(string) + "@" + m["version"].(string) + " " + m["status"].(string) + " " + m["repository"].(string)
	}

	return out
}

// TestCheckDependenciesFollowsTheGroup: check_dependencies decides through a group
// the way serving does, and a member the caller cannot read is absent: for the
// admin the hosted member owns left-pad (hosted wins), for the read-only user it
// does not exist and the proxy decides.
func TestCheckDependenciesFollowsTheGroup(t *testing.T) {
	st := depsStack(t)
	args := map[string]any{"repository": "npm-all", "packages": []map[string]any{
		{"package": "left-pad", "version": "1.0.0"},
		{"package": "left-pad", "version": "9.0.0"},
		{"package": "right-pad", "version": "1.0.0"},
	}}

	for _, tc := range []struct {
		user string
		want []string
	}{
		{"admin", []string{"left-pad@1.0.0 not_found npm-all", "left-pad@9.0.0 available hidden", "right-pad@1.0.0 unknown web-npm"}},
		{"reader", []string{"left-pad@1.0.0 available web-npm", "left-pad@9.0.0 not_found npm-all", "right-pad@1.0.0 unknown web-npm"}},
	} {
		got := depResults(t, st.call(tc.user, "check_dependencies", args))
		if strings.Join(got, "; ") != strings.Join(tc.want, "; ") {
			t.Errorf("%s: %v, want %v", tc.user, got, tc.want)
		}
	}
}

// TestCheckDependenciesRefuses: inputs out of bounds and a docker repository are
// tool errors the model can read, and an unreadable repository looks absent.
func TestCheckDependenciesRefuses(t *testing.T) {
	st := depsStack(t)
	one := []map[string]any{{"package": "left-pad", "version": "1.0.0"}}

	for _, tc := range []struct {
		name, user string
		args       map[string]any
		want       string
	}{
		{"no packages", "reader", map[string]any{"repository": "web-npm", "packages": []map[string]any{}}, "1 to 1000"},
		{"no version", "reader", map[string]any{"repository": "web-npm", "packages": []map[string]any{{"package": "x", "version": ""}}}, "1 to 1000"},
		{"docker", "reader", map[string]any{"repository": "images", "packages": one}, "not supported for registry (docker)"},
		{"unreadable", "reader", map[string]any{"repository": "hidden", "packages": one}, msgNotFound},
	} {
		resp := st.call(tc.user, "check_dependencies", tc.args)
		if !resp.Result.IsError || !strings.Contains(toolText(t, resp), tc.want) {
			t.Errorf("%s: got (%v) %q, want an error containing %q", tc.name, resp.Result.IsError, toolText(t, resp), tc.want)
		}
	}
}
