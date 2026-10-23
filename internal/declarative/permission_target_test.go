// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"maps"
	"net/http"
	"reflect"
	"testing"
)

func identPermDirectory() map[string]identRoute {
	routes := identUsersRoutes(
		map[string]any{"id": "u-alice", "username": "alice"},
		map[string]any{"id": "u-bob", "username": "bob"},
	)
	routes["GET /api/v1/groups"] = identJSON(http.StatusOK, map[string]any{"data": []map[string]any{
		{"id": "g-dev", "name": "dev"},
		{"id": "g-ops", "name": "ops"},
	}})

	return routes
}

func identAssignments(entries ...[3]string) map[string]any {
	data := make([]map[string]any, len(entries))
	for i, e := range entries {
		data[i] = map[string]any{"principal_type": e[0], "principal_id": e[1], "principal_name": e[2]}
	}

	return map[string]any{"data": data}
}

func TestPermissionTargetKindList(t *testing.T) {
	t.Parallel()

	env, _ := newIdentEnv(t, map[string]identRoute{
		"GET /api/v1/permission-targets": identJSON(http.StatusOK, map[string]any{"data": []map[string]any{
			{"id": "t-admin", "name": "admin-all", "repository_filter": ".*", "path_pattern": ".*", "actions": []string{"manage", "read"}},
			{"id": "t-anon", "name": "anonymous-read", "repository_filter": ".*", "path_pattern": ".*", "actions": []string{"read"}},
			{"id": "t-ci", "name": "ci", "repository_filter": "ci-.*", "path_pattern": "*", "actions": []string{"write", "read"}},
		}}),
		"GET /api/v1/permission-targets/t-admin/assignments": identJSON(http.StatusOK, identAssignments([3]string{"user", "u-alice", "alice"})),
		"GET /api/v1/permission-targets/t-anon/assignments":  identJSON(http.StatusOK, identAssignments()),
		"GET /api/v1/permission-targets/t-ci/assignments": identJSON(http.StatusOK, identAssignments(
			[3]string{"group", "g-ops", "ops"}, [3]string{"user", "u-bob", "bob"}, [3]string{"group", "g-dev", "dev"},
		)),
	})

	got, err := NewPermissionTargetKind().List(context.Background(), env)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	spec := func(filter, pattern string, actions, users, groups []any) map[string]any {
		return map[string]any{
			"repository_filter": filter, "path_pattern": pattern, "actions": actions,
			"assignments": map[string]any{"users": users, "groups": groups},
		}
	}

	want := []*Live{
		{Kind: KindPermissionTarget, Key: "admin-all", ID: "t-admin", Seeded: true,
			Spec: spec(".*", ".*", []any{"manage", "read"}, []any{"alice"}, []any{})},
		{Kind: KindPermissionTarget, Key: "anonymous-read", ID: "t-anon", Seeded: true,
			Spec: spec(".*", ".*", []any{"read"}, []any{}, []any{})},
		{Kind: KindPermissionTarget, Key: "ci", ID: "t-ci",
			Spec: spec("ci-.*", "*", []any{"read", "write"}, []any{"bob"}, []any{"dev", "ops"})},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %+v, want %+v", got, want)
	}
}

func TestPermissionTargetKindCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec map[string]any
		want []identCall
	}{
		{
			name: "creates the target and assigns users and groups by id",
			spec: map[string]any{
				"repository_filter": "ci-.*", "path_pattern": "*", "actions": []any{"read", "write"},
				"assignments": map[string]any{"users": []any{"bob", "alice"}, "groups": []any{"dev"}},
			},
			want: []identCall{
				{Method: http.MethodPost, URI: "/api/v1/permission-targets", Body: map[string]any{
					"name": "ci", "repository_filter": "ci-.*", "path_pattern": "*", "actions": []any{"read", "write"},
				}},
				{Method: http.MethodPost, URI: "/api/v1/permission-targets/t-new/assignments", Body: map[string]any{"principal_type": "user", "principal_id": "u-alice"}},
				{Method: http.MethodPost, URI: "/api/v1/permission-targets/t-new/assignments", Body: map[string]any{"principal_type": "user", "principal_id": "u-bob"}},
				{Method: http.MethodPost, URI: "/api/v1/permission-targets/t-new/assignments", Body: map[string]any{"principal_type": "group", "principal_id": "g-dev"}},
			},
		},
		{
			name: "without assignments only the target is created",
			spec: map[string]any{"repository_filter": ".*", "path_pattern": ".*", "actions": []any{"read"}},
			want: []identCall{
				{Method: http.MethodPost, URI: "/api/v1/permission-targets", Body: map[string]any{
					"name": "ci", "repository_filter": ".*", "path_pattern": ".*", "actions": []any{"read"},
				}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			routes := identPermDirectory()
			routes["POST /api/v1/permission-targets"] = identJSON(http.StatusCreated, map[string]any{"id": "t-new", "name": "ci"})
			routes["POST /api/v1/permission-targets/t-new/assignments"] = identJSON(http.StatusNoContent, nil)
			env, fake := newIdentEnv(t, routes)

			id, err := NewPermissionTargetKind().Create(context.Background(), env, &Object{Name: "ci", Spec: tc.spec})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			if id != "t-new" {
				t.Errorf("id = %q, want t-new", id)
			}

			identAssertCalls(t, fake.writes(), tc.want)
		})
	}
}

func TestPermissionTargetKindUpdate(t *testing.T) {
	t.Parallel()

	live := &Live{ID: "t-ci", Key: "ci", Spec: map[string]any{
		"assignments": map[string]any{"users": []any{"alice", "bob"}, "groups": []any{"ops"}},
	}}

	tests := []struct {
		name    string
		desired map[string]any
		changes []Change
		want    []identCall
	}{
		{
			name:    "field changes patch only the changed fields",
			desired: map[string]any{},
			changes: []Change{{Path: "path_pattern", Old: ".*", New: "dist/.*"}, {Path: "actions", Old: []any{"read"}, New: []any{"read", "write"}}},
			want: []identCall{{Method: http.MethodPatch, URI: "/api/v1/permission-targets/t-ci", Body: map[string]any{
				"path_pattern": "dist/.*", "actions": []any{"read", "write"},
			}}},
		},
		{
			name: "assignment changes assign the missing and unassign the extra principals",
			desired: map[string]any{"assignments": map[string]any{
				"users": []any{"alice"}, "groups": []any{"dev", "ops"},
			}},
			changes: []Change{
				{Path: "assignments.users", Old: []any{"alice", "bob"}, New: []any{"alice"}},
				{Path: "assignments.groups", Old: []any{"ops"}, New: []any{"dev", "ops"}},
			},
			want: []identCall{
				{Method: http.MethodDelete, URI: "/api/v1/permission-targets/t-ci/assignments?principal_id=u-bob&principal_type=user"},
				{Method: http.MethodPost, URI: "/api/v1/permission-targets/t-ci/assignments", Body: map[string]any{"principal_type": "group", "principal_id": "g-dev"}},
			},
		},
		{
			name:    "only the users list present: groups stay as they are",
			desired: map[string]any{"assignments": map[string]any{"users": []any{}}},
			changes: []Change{{Path: "assignments.users", Old: []any{"alice", "bob"}, New: []any{}}},
			want: []identCall{
				{Method: http.MethodDelete, URI: "/api/v1/permission-targets/t-ci/assignments?principal_id=u-alice&principal_type=user"},
				{Method: http.MethodDelete, URI: "/api/v1/permission-targets/t-ci/assignments?principal_id=u-bob&principal_type=user"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			routes := identPermDirectory()
			routes["PATCH /api/v1/permission-targets/t-ci"] = identJSON(http.StatusOK, map[string]any{})
			routes["POST /api/v1/permission-targets/t-ci/assignments"] = identJSON(http.StatusNoContent, nil)
			routes["DELETE /api/v1/permission-targets/t-ci/assignments"] = identJSON(http.StatusNoContent, nil)
			env, fake := newIdentEnv(t, routes)

			id, err := NewPermissionTargetKind().Update(context.Background(), env, live, &Object{Name: "ci", Spec: tc.desired}, tc.changes)
			if err != nil {
				t.Fatalf("Update: %v", err)
			}

			if id != "t-ci" {
				t.Errorf("id = %q, want t-ci", id)
			}

			identAssertCalls(t, fake.writes(), tc.want)
		})
	}
}

func TestPermissionTargetKindDelete(t *testing.T) {
	t.Parallel()

	env, fake := newIdentEnv(t, map[string]identRoute{
		"DELETE /api/v1/permission-targets/t-ci": identJSON(http.StatusNoContent, nil),
	})

	if err := NewPermissionTargetKind().Delete(context.Background(), env, &Live{Key: "ci", ID: "t-ci"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	identAssertCalls(t, fake.writes(), []identCall{{Method: http.MethodDelete, URI: "/api/v1/permission-targets/t-ci"}})
}

func TestPermissionTargetKindValidate(t *testing.T) {
	t.Parallel()

	valid := func(over map[string]any) map[string]any {
		spec := map[string]any{"repository_filter": "ci-.*", "path_pattern": "*", "actions": []any{"read"}}
		maps.Copy(spec, over)

		return spec
	}

	tests := []struct {
		name    string
		target  string
		spec    map[string]any
		wantErr bool
	}{
		{name: "valid", target: "ci", spec: valid(nil)},
		{name: "star shorthand", target: "ci", spec: valid(map[string]any{"repository_filter": "*"})},
		{name: "empty filter", target: "ci", spec: valid(map[string]any{"repository_filter": ""}), wantErr: true},
		{name: "blank pattern", target: "ci", spec: valid(map[string]any{"path_pattern": "  "}), wantErr: true},
		{name: "invalid regex", target: "ci", spec: valid(map[string]any{"path_pattern": "([a-z"}), wantErr: true},
		{name: "no actions", target: "ci", spec: valid(map[string]any{"actions": []any{}}), wantErr: true},
		{name: "padded name", target: " ci", spec: valid(nil), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			v, ok := NewPermissionTargetKind().(Validator)
			if !ok {
				t.Fatal("PermissionTarget must implement Validator")
			}

			errs := v.Validate(&Object{Kind: KindPermissionTarget, Name: tc.target, Spec: tc.spec})
			if (len(errs) > 0) != tc.wantErr {
				t.Errorf("Validate = %v, wantErr %v", errs, tc.wantErr)
			}
		})
	}
}

func TestPermissionTargetKindMetadata(t *testing.T) {
	t.Parallel()

	k := NewPermissionTargetKind()
	o := &Object{Spec: map[string]any{"assignments": map[string]any{
		"users": []any{"alice"}, "groups": []any{"dev"},
	}}}

	want := []Ref{
		{Kind: KindUser, Key: "alice", Field: "assignments.users[0]"},
		{Kind: KindGroup, Key: "dev", Field: "assignments.groups[0]"},
	}

	if got := k.Refs(o); !reflect.DeepEqual(got, want) {
		t.Errorf("Refs = %+v, want %+v", got, want)
	}

	if got := k.Refs(&Object{Spec: map[string]any{}}); len(got) != 0 {
		t.Errorf("Refs without assignments = %+v, want none", got)
	}

	if k.Rank() != 50 {
		t.Errorf("Rank = %d, want 50", k.Rank())
	}
}
