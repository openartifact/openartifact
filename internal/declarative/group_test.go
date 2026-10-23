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

// identDirectoryRoutes serves the user and group lookups of Env.UserID/GroupID.
func identDirectoryRoutes() map[string]identRoute {
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

func TestGroupKindList(t *testing.T) {
	t.Parallel()

	routes := map[string]identRoute{
		"GET /api/v1/groups": identJSON(http.StatusOK, map[string]any{"data": []map[string]any{
			{"id": "g-dev", "name": "dev", "description": "Developers"},
			{"id": "g-ops", "name": "ops", "description": nil},
		}}),
		"GET /api/v1/groups/g-dev/members": identJSON(http.StatusOK, map[string]any{"data": []map[string]any{
			{"id": "u-bob", "username": "bob"}, {"id": "u-alice", "username": "alice"},
		}}),
		"GET /api/v1/groups/g-ops/members": identJSON(http.StatusOK, map[string]any{"data": []map[string]any{}}),
	}
	env, _ := newIdentEnv(t, routes)

	got, err := NewGroupKind().List(context.Background(), env)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := []*Live{
		{Kind: KindGroup, Key: "dev", ID: "g-dev", Spec: map[string]any{
			"description": "Developers", "members": []any{"alice", "bob"},
		}},
		{Kind: KindGroup, Key: "ops", ID: "g-ops", Spec: map[string]any{
			"description": "", "members": []any{},
		}},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %+v, want %+v", got, want)
	}
}

func TestGroupKindCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec map[string]any
		want []identCall
	}{
		{
			name: "creates the group then replaces its membership with user ids",
			spec: map[string]any{"description": "Developers", "members": []any{"alice", "bob"}},
			want: []identCall{
				{Method: http.MethodPost, URI: "/api/v1/groups", Body: map[string]any{"name": "dev", "description": "Developers"}},
				{Method: http.MethodPut, URI: "/api/v1/groups/g-new/members", Body: map[string]any{"members": []any{"u-alice", "u-bob"}}},
			},
		},
		{
			name: "skips the membership call for an empty or absent member list",
			spec: map[string]any{"members": []any{}},
			want: []identCall{
				{Method: http.MethodPost, URI: "/api/v1/groups", Body: map[string]any{"name": "dev"}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			routes := identDirectoryRoutes()
			routes["POST /api/v1/groups"] = identJSON(http.StatusCreated, map[string]any{"id": "g-new", "name": "dev"})
			routes["PUT /api/v1/groups/g-new/members"] = identJSON(http.StatusNoContent, nil)
			env, fake := newIdentEnv(t, routes)

			id, err := NewGroupKind().Create(context.Background(), env, &Object{Kind: KindGroup, Name: "dev", Spec: tc.spec})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			if id != "g-new" {
				t.Errorf("id = %q, want g-new", id)
			}

			identAssertCalls(t, fake.writes(), tc.want)
		})
	}
}

func TestGroupKindCreateForgetsCachedIdents(t *testing.T) {
	t.Parallel()

	env, _ := newIdentEnv(t, map[string]identRoute{
		"POST /api/v1/groups": identJSON(http.StatusCreated, map[string]any{"id": "g-new", "name": "dev"}),
	})
	env.Cache[identsCacheKey] = &Idents{}

	if _, err := NewGroupKind().Create(context.Background(), env, &Object{Name: "dev", Spec: map[string]any{}}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, cached := env.Cache[identsCacheKey]; cached {
		t.Error("Create did not drop the cached group lookup")
	}
}

func TestGroupKindUpdate(t *testing.T) {
	t.Parallel()

	spec := map[string]any{"description": "New", "members": []any{"bob"}}

	tests := []struct {
		name    string
		changes []Change
		want    []identCall
	}{
		{
			name:    "description only patches the group and leaves members alone",
			changes: []Change{{Path: "description", Old: "Old", New: "New"}},
			want: []identCall{
				{Method: http.MethodPatch, URI: "/api/v1/groups/g-dev", Body: map[string]any{"description": "New"}},
			},
		},
		{
			name:    "members only replaces the membership with the desired set",
			changes: []Change{{Path: "members", Old: []any{"alice"}, New: []any{"bob"}}},
			want: []identCall{
				{Method: http.MethodPut, URI: "/api/v1/groups/g-dev/members", Body: map[string]any{"members": []any{"u-bob"}}},
			},
		},
		{
			name: "both changes use both endpoints",
			changes: []Change{
				{Path: "description", Old: "Old", New: "New"},
				{Path: "members", Old: []any{"alice"}, New: []any{"bob"}},
			},
			want: []identCall{
				{Method: http.MethodPatch, URI: "/api/v1/groups/g-dev", Body: map[string]any{"description": "New"}},
				{Method: http.MethodPut, URI: "/api/v1/groups/g-dev/members", Body: map[string]any{"members": []any{"u-bob"}}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			routes := identDirectoryRoutes()
			routes["PATCH /api/v1/groups/g-dev"] = identJSON(http.StatusOK, map[string]any{})
			routes["PUT /api/v1/groups/g-dev/members"] = identJSON(http.StatusNoContent, nil)
			env, fake := newIdentEnv(t, routes)

			live := &Live{Kind: KindGroup, Key: "dev", ID: "g-dev"}

			id, err := NewGroupKind().Update(context.Background(), env, live, &Object{Name: "dev", Spec: spec}, tc.changes)
			if err != nil {
				t.Fatalf("Update: %v", err)
			}

			if id != "g-dev" {
				t.Errorf("id = %q, want g-dev", id)
			}

			identAssertCalls(t, fake.writes(), tc.want)
		})
	}
}

func TestGroupKindUpdateClearsMembers(t *testing.T) {
	t.Parallel()

	env, fake := newIdentEnv(t, map[string]identRoute{
		"PUT /api/v1/groups/g-dev/members": identJSON(http.StatusNoContent, nil),
	})

	changes := []Change{{Path: "members", Old: []any{"alice"}, New: []any{}}}
	obj := &Object{Name: "dev", Spec: map[string]any{"members": []any{}}}

	if _, err := NewGroupKind().Update(context.Background(), env, &Live{ID: "g-dev"}, obj, changes); err != nil {
		t.Fatalf("Update: %v", err)
	}

	identAssertCalls(t, fake.writes(), []identCall{
		{Method: http.MethodPut, URI: "/api/v1/groups/g-dev/members", Body: map[string]any{"members": []any{}}},
	})
}

func TestGroupKindDelete(t *testing.T) {
	t.Parallel()

	env, fake := newIdentEnv(t, map[string]identRoute{
		"DELETE /api/v1/groups/g-dev": identJSON(http.StatusNoContent, nil),
	})

	if err := NewGroupKind().Delete(context.Background(), env, &Live{Key: "dev", ID: "g-dev"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	identAssertCalls(t, fake.writes(), []identCall{{Method: http.MethodDelete, URI: "/api/v1/groups/g-dev"}})
}

func TestGroupKindMetadata(t *testing.T) {
	t.Parallel()

	k := NewGroupKind()

	refs := k.Refs(&Object{Spec: map[string]any{"members": []any{"alice", "bob"}}})
	wantRefs := []Ref{
		{Kind: KindUser, Key: "alice", Field: "members[0]"},
		{Kind: KindUser, Key: "bob", Field: "members[1]"},
	}

	if !reflect.DeepEqual(refs, wantRefs) {
		t.Errorf("Refs = %+v, want %+v", refs, wantRefs)
	}

	omitter, ok := k.(ExportOmitter)
	if !ok || !reflect.DeepEqual(omitter.ExportOmit(), []string{"members"}) {
		t.Error("Group must implement ExportOmitter omitting members")
	}

	if k.Rank() != 15 {
		t.Errorf("Rank = %d, want 15", k.Rank())
	}
}
