// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

func identProjectDirectory() map[string]identRoute {
	routes := identUsersRoutes(
		map[string]any{"id": "u-alice", "username": "alice"},
		map[string]any{"id": "u-bob", "username": "bob"},
		map[string]any{"id": "u-carol", "username": "carol"},
		map[string]any{"id": "u-dave", "username": "dave"},
	)
	routes["GET /api/v1/groups"] = identJSON(http.StatusOK, map[string]any{"data": []map[string]any{
		{"id": "g-dev", "name": "dev"},
		{"id": "g-ops", "name": "ops"},
		{"id": "g-legacy", "name": "legacy"},
	}})

	return routes
}

func identMemberSpec(entries ...[2]string) []any {
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = map[string]any{"username": e[0], "role": e[1]}
	}

	return out
}

func TestProjectKindList(t *testing.T) {
	t.Parallel()

	env, fake := newIdentEnv(t, map[string]identRoute{
		"GET /api/v1/projects": identJSON(http.StatusOK, map[string]any{"projects": []map[string]any{
			{
				"id": "p-sys", "name": "unassigned", "display_name": nil, "description": nil,
				"allowed_formats": nil, "allowed_repo_types": []string{"hosted"}, "repo_name_prefix": "unassigned-",
				"allow_public_repos": false, "max_repositories": 50, "token_max_ttl_days": 365, "is_system": true,
			},
			{
				"id": "p-web", "name": "web", "display_name": "Web", "description": "Web team",
				"allowed_formats": []string{"npm", "docker"}, "allowed_repo_types": []string{"proxy", "hosted"},
				"repo_name_prefix": "web-", "allow_public_repos": true, "max_repositories": 10, "token_max_ttl_days": 30,
			},
		}, "can_create": true}),
		"GET /api/v1/projects/unassigned/members": identJSON(http.StatusOK, map[string]any{"members": []any{}}),
		"GET /api/v1/projects/unassigned/groups":  identJSON(http.StatusOK, map[string]any{"groups": []any{}}),
		"GET /api/v1/projects/web/members": identJSON(http.StatusOK, map[string]any{"members": []map[string]any{
			{"user_id": "u-bob", "username": "bob", "role": "viewer"},
			{"user_id": "u-alice", "username": "alice", "role": "owner"},
		}}),
		"GET /api/v1/projects/web/groups": identJSON(http.StatusOK, map[string]any{"groups": []map[string]any{
			{"group_id": "g-dev", "group_name": "dev", "role": "developer"},
		}}),
	})

	got, err := NewProjectKind().List(context.Background(), env)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := []*Live{
		{Kind: KindProject, Key: "unassigned", ID: "p-sys", Seeded: true, Spec: map[string]any{
			"display_name": "", "description": "", "allowed_formats": nil,
			"allowed_repo_types": []any{"hosted"}, "repo_name_prefix": "unassigned-", "allow_public_repos": false,
			"max_repositories": float64(50), "token_max_ttl_days": float64(365),
			"members": []any{}, "groups": []any{},
		}},
		{Kind: KindProject, Key: "web", ID: "p-web", Spec: map[string]any{
			"display_name": "Web", "description": "Web team", "allowed_formats": []any{"docker", "npm"},
			"allowed_repo_types": []any{"hosted", "proxy"}, "repo_name_prefix": "web-", "allow_public_repos": true,
			"max_repositories": float64(10), "token_max_ttl_days": float64(30),
			"members": identMemberSpec([2]string{"alice", "owner"}, [2]string{"bob", "viewer"}),
			"groups":  []any{map[string]any{"group": "dev", "role": "developer"}},
		}},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %+v, want %+v", got, want)
	}

	if first := fake.calls[0]; first.URI != "/api/v1/projects?all=true" {
		t.Errorf("project list requested %q, want /api/v1/projects?all=true", first.URI)
	}
}

func TestProjectKindCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec map[string]any
		want []identCall
	}{
		{
			name: "members: first owner becomes the owner, the others and groups are attached afterwards",
			spec: map[string]any{
				"display_name":       "Web",
				"allowed_repo_types": []any{"hosted", "proxy"},
				"max_repositories":   float64(10),
				"members":            identMemberSpec([2]string{"bob", "viewer"}, [2]string{"alice", "owner"}, [2]string{"carol", "owner"}),
				"groups":             []any{map[string]any{"group": "dev", "role": "developer"}},
			},
			want: []identCall{
				{Method: http.MethodPost, URI: "/api/v1/projects", Body: map[string]any{
					"name": "web", "display_name": "Web", "owner": "alice",
					"allowed_repo_types": []any{"hosted", "proxy"}, "max_repositories": float64(10),
				}},
				{Method: http.MethodPost, URI: "/api/v1/projects/web/members", Body: map[string]any{"username": "carol", "role": "owner"}},
				{Method: http.MethodPost, URI: "/api/v1/projects/web/members", Body: map[string]any{"username": "bob", "role": "viewer"}},
				{Method: http.MethodPut, URI: "/api/v1/projects/web/groups/g-dev", Body: map[string]any{"role": "developer"}},
			},
		},
		{
			name: "no members: no owner is named and null allowed_formats is not sent",
			spec: map[string]any{"description": "d", "allowed_formats": nil},
			want: []identCall{
				{Method: http.MethodPost, URI: "/api/v1/projects", Body: map[string]any{"name": "web", "description": "d"}},
			},
		},
		{
			name: "restricted formats are sent as given",
			spec: map[string]any{"allowed_formats": []any{"npm"}},
			want: []identCall{
				{Method: http.MethodPost, URI: "/api/v1/projects", Body: map[string]any{"name": "web", "allowed_formats": []any{"npm"}}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			routes := identProjectDirectory()
			routes["POST /api/v1/projects"] = identJSON(http.StatusCreated, map[string]any{"id": "p-web", "name": "web"})
			routes["POST /api/v1/projects/web/members"] = identJSON(http.StatusCreated, map[string]any{})
			routes["PUT /api/v1/projects/web/groups/g-dev"] = identJSON(http.StatusOK, map[string]any{})
			env, fake := newIdentEnv(t, routes)

			id, err := NewProjectKind().Create(context.Background(), env, &Object{Kind: KindProject, Name: "web", Spec: tc.spec})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			if id != "p-web" {
				t.Errorf("id = %q, want p-web", id)
			}

			identAssertCalls(t, fake.writes(), tc.want)
		})
	}
}

func TestProjectKindUpdatePolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		changes []Change
		want    map[string]any
	}{
		{
			name: "sends only the changed policy fields",
			changes: []Change{
				{Path: "max_repositories", Old: float64(50), New: float64(10)},
				{Path: "allow_public_repos", Old: false, New: true},
			},
			want: map[string]any{"max_repositories": float64(10), "allow_public_repos": true},
		},
		{
			name:    "null allowed_formats is sent as the empty list meaning every format",
			changes: []Change{{Path: "allowed_formats", Old: []any{"npm"}, New: nil}},
			want:    map[string]any{"allowed_formats": []any{}},
		},
		{
			name: "members and groups changes never reach the project PATCH",
			changes: []Change{
				{Path: "description", Old: "a", New: "b"},
				{Path: "members[bob].role", Old: "viewer", New: "developer"},
				{Path: "groups[dev]", New: map[string]any{"group": "dev", "role": "viewer"}},
			},
			want: map[string]any{"description": "b"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			routes := identProjectDirectory()
			routes["PATCH /api/v1/projects/web"] = identJSON(http.StatusOK, map[string]any{})
			routes["PATCH /api/v1/projects/web/members/u-bob"] = identJSON(http.StatusOK, map[string]any{})
			routes["PUT /api/v1/projects/web/groups/g-dev"] = identJSON(http.StatusOK, map[string]any{})
			env, fake := newIdentEnv(t, routes)

			// Members and groups are absent from the desired spec, so they are left alone.
			obj := &Object{Name: "web", Spec: map[string]any{}}
			live := &Live{ID: "p-web", Key: "web", Spec: map[string]any{}}

			if _, err := NewProjectKind().Update(context.Background(), env, live, obj, tc.changes); err != nil {
				t.Fatalf("Update: %v", err)
			}

			identAssertCalls(t, fake.writes(), []identCall{{Method: http.MethodPatch, URI: "/api/v1/projects/web", Body: tc.want}})
		})
	}
}

func TestProjectKindUpdateMembers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		live    []any
		desired []any
		want    []identCall
	}{
		{
			name:    "adds, changes roles and removes members, without touching the project",
			live:    identMemberSpec([2]string{"alice", "owner"}, [2]string{"bob", "viewer"}, [2]string{"carol", "developer"}),
			desired: identMemberSpec([2]string{"alice", "owner"}, [2]string{"bob", "maintainer"}, [2]string{"dave", "developer"}),
			want: []identCall{
				{Method: http.MethodPost, URI: "/api/v1/projects/web/members", Body: map[string]any{"username": "dave", "role": "developer"}},
				{Method: http.MethodPatch, URI: "/api/v1/projects/web/members/u-bob", Body: map[string]any{"role": "maintainer"}},
				{Method: http.MethodDelete, URI: "/api/v1/projects/web/members/u-carol"},
			},
		},
		{
			name:    "promotes the new owner before demoting the old one",
			live:    identMemberSpec([2]string{"alice", "owner"}, [2]string{"bob", "viewer"}),
			desired: identMemberSpec([2]string{"alice", "viewer"}, [2]string{"bob", "owner"}),
			want: []identCall{
				{Method: http.MethodPatch, URI: "/api/v1/projects/web/members/u-bob", Body: map[string]any{"role": "owner"}},
				{Method: http.MethodPatch, URI: "/api/v1/projects/web/members/u-alice", Body: map[string]any{"role": "viewer"}},
			},
		},
		{
			name:    "an unchanged membership sends nothing",
			live:    identMemberSpec([2]string{"alice", "owner"}),
			desired: identMemberSpec([2]string{"alice", "owner"}),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			routes := identProjectDirectory()
			routes["POST /api/v1/projects/web/members"] = identJSON(http.StatusCreated, map[string]any{})

			for _, id := range []string{"u-alice", "u-bob", "u-carol"} {
				routes["PATCH /api/v1/projects/web/members/"+id] = identJSON(http.StatusOK, map[string]any{})
				routes["DELETE /api/v1/projects/web/members/"+id] = identJSON(http.StatusNoContent, nil)
			}

			env, fake := newIdentEnv(t, routes)

			obj := &Object{Name: "web", Spec: map[string]any{"members": tc.desired}}
			live := &Live{ID: "p-web", Key: "web", Spec: map[string]any{"members": tc.live}}
			changes := []Change{{Path: "members[bob].role", Old: "viewer", New: "maintainer"}}

			if _, err := NewProjectKind().Update(context.Background(), env, live, obj, changes); err != nil {
				t.Fatalf("Update: %v", err)
			}

			identAssertCalls(t, fake.writes(), tc.want)
		})
	}
}

func TestProjectKindUpdateGroups(t *testing.T) {
	t.Parallel()

	routes := identProjectDirectory()
	routes["PUT /api/v1/projects/web/groups/g-dev"] = identJSON(http.StatusOK, map[string]any{})
	routes["PUT /api/v1/projects/web/groups/g-ops"] = identJSON(http.StatusOK, map[string]any{})
	routes["DELETE /api/v1/projects/web/groups/g-legacy"] = identJSON(http.StatusNoContent, nil)
	env, fake := newIdentEnv(t, routes)

	group := func(name, role string) any { return map[string]any{"group": name, "role": role} }
	obj := &Object{Name: "web", Spec: map[string]any{"groups": []any{group("dev", "maintainer"), group("ops", "viewer")}}}
	live := &Live{ID: "p-web", Key: "web", Spec: map[string]any{"groups": []any{group("dev", "developer"), group("legacy", "viewer")}}}
	changes := []Change{{Path: "groups[dev].role", Old: "developer", New: "maintainer"}}

	if _, err := NewProjectKind().Update(context.Background(), env, live, obj, changes); err != nil {
		t.Fatalf("Update: %v", err)
	}

	identAssertCalls(t, fake.writes(), []identCall{
		{Method: http.MethodPut, URI: "/api/v1/projects/web/groups/g-ops", Body: map[string]any{"role": "viewer"}},
		{Method: http.MethodPut, URI: "/api/v1/projects/web/groups/g-dev", Body: map[string]any{"role": "maintainer"}},
		{Method: http.MethodDelete, URI: "/api/v1/projects/web/groups/g-legacy"},
	})
}

func TestProjectKindDelete(t *testing.T) {
	t.Parallel()

	env, fake := newIdentEnv(t, map[string]identRoute{
		"DELETE /api/v1/projects/web": identJSON(http.StatusNoContent, nil),
	})

	if err := NewProjectKind().Delete(context.Background(), env, &Live{Key: "web", ID: "p-web"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	identAssertCalls(t, fake.writes(), []identCall{{Method: http.MethodDelete, URI: "/api/v1/projects/web"}})
}

func TestProjectKindValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    map[string]any
		wantErr bool
	}{
		{name: "no members is fine", spec: map[string]any{}},
		{name: "members with an owner", spec: map[string]any{"members": identMemberSpec([2]string{"a", "viewer"}, [2]string{"b", "owner"})}},
		{name: "members without an owner", spec: map[string]any{"members": identMemberSpec([2]string{"a", "viewer"})}, wantErr: true},
		{name: "empty members have no owner", spec: map[string]any{"members": []any{}}, wantErr: true},
		{name: "null allowed_formats means every format", spec: map[string]any{"allowed_formats": nil}},
		{name: "empty allowed_formats is ambiguous", spec: map[string]any{"allowed_formats": []any{}}, wantErr: true},
		{name: "empty allowed_repo_types", spec: map[string]any{"allowed_repo_types": []any{}}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			v, ok := NewProjectKind().(Validator)
			if !ok {
				t.Fatal("Project must implement Validator")
			}

			errs := v.Validate(&Object{Kind: KindProject, Name: "web", Spec: tc.spec})
			if (len(errs) > 0) != tc.wantErr {
				t.Errorf("Validate = %v, wantErr %v", errs, tc.wantErr)
			}
		})
	}
}

func TestProjectKindMetadata(t *testing.T) {
	t.Parallel()

	k := NewProjectKind()
	o := &Object{Spec: map[string]any{
		"members": identMemberSpec([2]string{"alice", "owner"}),
		"groups":  []any{map[string]any{"group": "dev", "role": "viewer"}},
	}}

	want := []Ref{
		{Kind: KindUser, Key: "alice", Field: "members[0].username"},
		{Kind: KindGroup, Key: "dev", Field: "groups[0].group"},
	}

	if got := k.Refs(o); !reflect.DeepEqual(got, want) {
		t.Errorf("Refs = %+v, want %+v", got, want)
	}

	if k.Rank() != 20 {
		t.Errorf("Rank = %d, want 20", k.Rank())
	}
}
