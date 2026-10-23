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

func tierAPIItems() map[string]any {
	return map[string]any{"total": 2, "items": []any{
		map[string]any{
			"id": "t-hot", "name": "hot", "primary": true, "backend": nil, "rank": 10, "promote_on_read": false,
			"blob_count": 7, "total_bytes": 700, "available": true,
		},
		map[string]any{
			"id": "t-cold", "name": "cold", "primary": false, "backend": "hdd", "backend_kind": "s3", "rank": 20,
			"promote_on_read": true, "blob_count": 3,
		},
	}}
}

func TestTierList(t *testing.T) {
	t.Parallel()

	env, fake := newSSOTierFake(t, okAnswer(http.StatusOK, tierAPIItems()))

	lives, err := NewStorageTierKind().List(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}

	if got := fake.requests(); len(got) != 1 || got[0].Path != tierPath {
		t.Fatalf("requests = %+v", got)
	}

	want := []*Live{
		{Kind: KindStorageTier, Key: "hot", ID: "t-hot", Seeded: true,
			Spec: map[string]any{"backend": nil, "rank": float64(10), "promote_on_read": false}},
		{Kind: KindStorageTier, Key: "cold", ID: "t-cold",
			Spec: map[string]any{"backend": "hdd", "rank": float64(20), "promote_on_read": true}},
	}

	if !reflect.DeepEqual(lives, want) {
		t.Errorf("lives = %+v\nwant %+v", lives, want)
	}
}

func TestTierCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		spec     map[string]any
		wantBody map[string]any
		wantErr  bool
	}{
		{
			name:     "all fields",
			spec:     map[string]any{"backend": "hdd", "rank": float64(30), "promote_on_read": true},
			wantBody: map[string]any{"name": "cold", "backend": "hdd", "rank": float64(30), "promote_on_read": true},
		},
		{
			name:     "server picks the rank",
			spec:     map[string]any{"backend": "hdd"},
			wantBody: map[string]any{"name": "cold", "backend": "hdd"},
		},
		{name: "primary tier cannot be created (null backend)", spec: map[string]any{"backend": nil}, wantErr: true},
		{name: "primary tier cannot be created (no backend)", spec: map[string]any{"rank": float64(1)}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env, fake := newSSOTierFake(t, okAnswer(http.StatusCreated, map[string]any{"id": "t-new"}))

			id, err := NewStorageTierKind().Create(context.Background(), env, &Object{Kind: KindStorageTier, Name: "cold", Spec: tc.spec})
			if tc.wantErr {
				if err == nil {
					t.Fatal("want an error")
				}

				if got := fake.requests(); len(got) != 0 {
					t.Errorf("no request expected, got %+v", got)
				}

				return
			}

			if err != nil || id != "t-new" {
				t.Fatalf("id=%q err=%v", id, err)
			}

			want := []ssoTierReq{{Method: http.MethodPost, Path: tierPath, Body: tc.wantBody}}
			if got := fake.requests(); !reflect.DeepEqual(got, want) {
				t.Errorf("requests = %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestTierUpdateAndDelete(t *testing.T) {
	t.Parallel()

	env, fake := newSSOTierFake(t, okAnswer(http.StatusOK, map[string]any{}))
	k := NewStorageTierKind()
	live := &Live{Key: "cold", ID: "t-cold"}

	if _, err := k.Update(context.Background(), env, live, &Object{}, []Change{{Path: "rank", Old: float64(20), New: float64(40)}}); err != nil {
		t.Fatal(err)
	}

	if err := k.Delete(context.Background(), env, live); err != nil {
		t.Fatal(err)
	}

	want := []ssoTierReq{
		{Method: http.MethodPatch, Path: tierPath + "/cold", Body: map[string]any{"rank": float64(40)}},
		{Method: http.MethodDelete, Path: tierPath + "/cold"},
	}

	if got := fake.requests(); !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %+v\nwant %+v", got, want)
	}
}

func TestTierSchemaBackendImmutable(t *testing.T) {
	t.Parallel()

	f := NewStorageTierKind().Schema().Fields["backend"]
	if !f.Immutable || !f.Nullable {
		t.Errorf("backend must be immutable and nullable: %+v", f)
	}
}

func policyAPIItems() map[string]any {
	return map[string]any{"total": 2, "items": []any{
		map[string]any{
			"id": "p1", "name": "archive-old", "enabled": true, "position": 10, "target_tier": "cold", "from_tier": "hot",
			"min_age_seconds": 86400, "min_idle_seconds": nil, "min_size_bytes": nil, "max_size_bytes": 1000,
			"repo_glob": "docker-*", "formats": []any{"docker", "maven"}, "path_glob": nil, "created_at": "2026-01-01T00:00:00Z",
		},
		map[string]any{
			"id": "p2", "name": "minimal", "enabled": false, "position": 20, "target_tier": "cold", "from_tier": nil,
			"min_age_seconds": nil, "min_idle_seconds": nil, "min_size_bytes": nil, "max_size_bytes": nil,
			"repo_glob": nil, "formats": nil, "path_glob": "*.iso",
		},
	}}
}

func TestPolicyList(t *testing.T) {
	t.Parallel()

	env, fake := newSSOTierFake(t, okAnswer(http.StatusOK, policyAPIItems()))

	lives, err := NewTierPolicyKind().List(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}

	if got := fake.requests(); len(got) != 1 || got[0].Path != policyPath {
		t.Fatalf("requests = %+v", got)
	}

	want := []*Live{
		{Kind: KindTierPolicy, Key: "archive-old", ID: "p1", Spec: map[string]any{
			"enabled": true, "position": float64(10), "target_tier": "cold", "from_tier": "hot",
			"min_age_seconds": float64(86400), "min_idle_seconds": nil, "min_size_bytes": nil, "max_size_bytes": float64(1000),
			"repo_glob": "docker-*", "formats": []any{"docker", "maven"}, "path_glob": nil,
		}},
		{Kind: KindTierPolicy, Key: "minimal", ID: "p2", Spec: map[string]any{
			"enabled": false, "position": float64(20), "target_tier": "cold", "from_tier": nil,
			"min_age_seconds": nil, "min_idle_seconds": nil, "min_size_bytes": nil, "max_size_bytes": nil,
			"repo_glob": nil, "formats": []any{}, "path_glob": "*.iso",
		}},
	}

	if !reflect.DeepEqual(lives, want) {
		t.Errorf("lives = %+v\nwant %+v", lives, want)
	}

	schema := NewTierPolicyKind().Schema()
	for k := range lives[0].Spec {
		if _, ok := schema.Fields[k]; !ok {
			t.Errorf("Live.Spec key %q is not declared in the schema", k)
		}
	}
}

func TestPolicyCreate(t *testing.T) {
	t.Parallel()

	env, fake := newSSOTierFake(t, okAnswer(http.StatusCreated, map[string]any{"id": "p-new"}))

	spec := map[string]any{
		"target_tier": "cold", "from_tier": nil, "min_age_seconds": float64(3600), "formats": []any{"npm"},
	}

	id, err := NewTierPolicyKind().Create(context.Background(), env, &Object{Kind: KindTierPolicy, Name: "p", Spec: spec})
	if err != nil || id != "p-new" {
		t.Fatalf("id=%q err=%v", id, err)
	}

	want := []ssoTierReq{{Method: http.MethodPost, Path: policyPath, Body: map[string]any{
		"name": "p", "target_tier": "cold", "from_tier": nil, "min_age_seconds": float64(3600), "formats": []any{"npm"},
	}}}

	if got := fake.requests(); !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %+v\nwant %+v", got, want)
	}
}

func TestPolicyUpdateMergesIntoFullPut(t *testing.T) {
	t.Parallel()

	env, fake := newSSOTierFake(t, okAnswer(http.StatusOK, map[string]any{}))
	items, _ := Canon(policyAPIItems()["items"]).([]any)
	first, _ := items[0].(map[string]any)
	live := policyLive(first)
	changes := []Change{
		{Path: "min_age_seconds", Old: float64(86400), New: nil},
		{Path: "path_glob", Old: nil, New: "*.bin"},
	}

	if _, err := NewTierPolicyKind().Update(context.Background(), env, live, &Object{}, changes); err != nil {
		t.Fatal(err)
	}

	want := []ssoTierReq{{Method: http.MethodPut, Path: policyPath + "/p1", Body: map[string]any{
		"name": "archive-old", "enabled": true, "position": float64(10), "target_tier": "cold", "from_tier": "hot",
		"min_age_seconds": nil, "min_idle_seconds": nil, "min_size_bytes": nil, "max_size_bytes": float64(1000),
		"repo_glob": "docker-*", "formats": []any{"docker", "maven"}, "path_glob": "*.bin",
	}}}

	if got := fake.requests(); !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %+v\nwant %+v", got, want)
	}
}

func TestPolicyDelete(t *testing.T) {
	t.Parallel()

	env, fake := newSSOTierFake(t, okAnswer(http.StatusNoContent, nil))

	if err := NewTierPolicyKind().Delete(context.Background(), env, &Live{Key: "p", ID: "p1"}); err != nil {
		t.Fatal(err)
	}

	want := []ssoTierReq{{Method: http.MethodDelete, Path: policyPath + "/p1"}}
	if got := fake.requests(); !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %+v\nwant %+v", got, want)
	}
}

func TestPolicyRefsAndValidate(t *testing.T) {
	t.Parallel()

	k := NewTierPolicyKind()

	tests := []struct {
		name     string
		spec     map[string]any
		wantRefs []Ref
		wantErr  bool
	}{
		{
			name:     "target only",
			spec:     map[string]any{"target_tier": "cold", "from_tier": nil},
			wantRefs: []Ref{{Kind: KindStorageTier, Key: "cold", Field: "target_tier"}},
		},
		{
			name: "target and source",
			spec: map[string]any{"target_tier": "cold", "from_tier": "hot"},
			wantRefs: []Ref{
				{Kind: KindStorageTier, Key: "cold", Field: "target_tier"},
				{Kind: KindStorageTier, Key: "hot", Field: "from_tier"},
			},
		},
		{
			name:     "same tier twice",
			spec:     map[string]any{"target_tier": "cold", "from_tier": "cold"},
			wantRefs: []Ref{{Kind: KindStorageTier, Key: "cold", Field: "target_tier"}, {Kind: KindStorageTier, Key: "cold", Field: "from_tier"}},
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			o := &Object{Kind: KindTierPolicy, Name: "p", Spec: tc.spec}

			if got := k.Refs(o); !reflect.DeepEqual(got, tc.wantRefs) {
				t.Errorf("refs = %+v, want %+v", got, tc.wantRefs)
			}

			v, ok := k.(Validator)
			if !ok {
				t.Fatal("TierPolicy kind must implement Validator")
			}

			if errs := v.Validate(o); (len(errs) > 0) != tc.wantErr {
				t.Errorf("errors = %v, wantErr %v", errs, tc.wantErr)
			}
		})
	}
}
