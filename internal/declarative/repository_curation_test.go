// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const curationBase = "/api/v1/repositories/px/curation-rules"

func curationRulesFixture() map[string]any {
	return map[string]any{"rules": []any{
		map[string]any{
			"id": "r1", "action": "block", "package_pattern": "left-pad", "version_pattern": "1.*",
			"reason": "malware", "created_by": "admin", "created_at": "2026-01-01T00:00:00Z",
		},
		map[string]any{
			"id": "r2", "action": "allow", "package_pattern": "@acme/*", "version_pattern": "",
			"reason": "", "created_by": "admin", "created_at": "2026-01-02T00:00:00Z",
		},
	}}
}

// A proxy lists its mode and rules in manifest shape; the rules of other
// repository types are not part of their spec.
func TestRepositoryListCuration(t *testing.T) {
	t.Parallel()

	env, _ := newRepoFake(t, func(req repoFakeReq) (int, any) {
		switch {
		case req.Method == http.MethodGet && req.Path == "/api/v1/repositories":
			return http.StatusOK, map[string]any{"total": 2, "repositories": []any{
				map[string]any{
					"id": "id-px", "name": "px", "format": "npm", "repo_type": "proxy", "upstream_url": "https://registry.npmjs.org",
					"curation_mode": "denylist", "cache_ttl_seconds": 3600, "negative_cache_ttl_seconds": 60, "max_stale_seconds": 0,
					"min_age_hours": 0,
				},
				map[string]any{"id": "id-h", "name": "h", "format": "npm", "repo_type": "hosted"},
			}}
		case req.Method == http.MethodGet && req.Path == curationBase:
			return http.StatusOK, curationRulesFixture()
		case req.Method == http.MethodGet && strings.HasSuffix(req.Path, "/curation-rules"):
			t.Errorf("curation rules of a non-proxy were fetched: %s", req.Path)
		}

		if status, resp, ok := repoSubAnswer(req); ok {
			return status, resp
		}

		return repoNotFound(req)
	})

	lives, err := NewRepositoryKind().List(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}

	proxy, hosted := lives[0].Spec, lives[1].Spec

	if proxy["curation_mode"] != "denylist" {
		t.Errorf("curation_mode = %v, want denylist", proxy["curation_mode"])
	}

	wantRules := []any{
		map[string]any{"action": "block", "package_pattern": "left-pad", "version_pattern": "1.*", "reason": "malware"},
		map[string]any{"action": "allow", "package_pattern": "@acme/*", "version_pattern": "", "reason": ""},
	}
	if !reflect.DeepEqual(proxy["curation_rules"], wantRules) {
		t.Errorf("curation_rules = %#v, want %#v", proxy["curation_rules"], wantRules)
	}

	for _, k := range []string{"curation_mode", "curation_rules"} {
		if _, ok := hosted[k]; ok {
			t.Errorf("hosted spec has %s", k)
		}
	}
}

func TestRepositoryCreateCuration(t *testing.T) {
	t.Parallel()

	env, fake := newRepoFake(t, func(req repoFakeReq) (int, any) {
		switch {
		case req.Method == http.MethodPost && req.Path == "/api/v1/repositories":
			return http.StatusCreated, map[string]any{"id": "new-id"}
		case req.Method == http.MethodGet && strings.HasSuffix(req.Path, "/curation-rules"):
			return http.StatusOK, map[string]any{"rules": []any{}}
		case req.Method != http.MethodGet:
			return http.StatusCreated, map[string]any{}
		}

		return repoNotFound(req)
	})

	spec := map[string]any{
		"format": "npm", "repo_type": "proxy", "curation_mode": "allowlist",
		"curation_rules": []any{
			map[string]any{"action": "allow", "package_pattern": "react", "version_pattern": "18.*", "reason": "approved"},
		},
	}

	if _, err := NewRepositoryKind().Create(context.Background(), env, &Object{Kind: KindRepository, Name: "px", Spec: spec}); err != nil {
		t.Fatal(err)
	}

	assertRepoMutations(t, fake, []repoFakeReq{
		{Method: http.MethodPost, Path: "/api/v1/repositories", Body: map[string]any{
			"name": "px", "format": "npm", "repo_type": "proxy", "curation_mode": "allowlist",
		}},
		{Method: http.MethodPost, Path: curationBase, Body: map[string]any{
			"action": "allow", "package_pattern": "react", "version_pattern": "18.*", "reason": "approved",
		}},
	})
}

// The rules are reconciled by (action, package pattern, version pattern): a new
// one is created, a changed reason is patched, an unlisted one is deleted after
// the creates, an unchanged one is left alone, and the mode goes to the PATCH
// of the repository.
func TestRepositoryUpdateCuration(t *testing.T) {
	t.Parallel()

	env, fake := newRepoFake(t, func(req repoFakeReq) (int, any) {
		if req.Method == http.MethodGet && req.Path == curationBase {
			return http.StatusOK, curationRulesFixture()
		}

		if req.Method != http.MethodGet {
			return http.StatusOK, map[string]any{}
		}

		return repoNotFound(req)
	})

	o := &Object{Kind: KindRepository, Name: "px", Spec: map[string]any{
		"curation_mode": "allowlist",
		"curation_rules": []any{
			map[string]any{"action": "block", "package_pattern": "left-pad", "version_pattern": "1.*", "reason": "confirmed malware"},
			map[string]any{"action": "allow", "package_pattern": "react"},
		},
	}}
	live := &Live{Kind: KindRepository, Key: "px", ID: "id-px", Spec: map[string]any{}}
	changes := []Change{
		{Path: "curation_mode", Old: "denylist", New: "allowlist"},
		{Path: "curation_rules[block|left-pad|1.*].reason", Old: "malware", New: "confirmed malware"},
		{Path: "curation_rules[allow|react|]", New: map[string]any{}},
		{Path: "curation_rules[allow|@acme/*|]", Old: map[string]any{}},
	}

	if _, err := NewRepositoryKind().Update(context.Background(), env, live, o, changes); err != nil {
		t.Fatal(err)
	}

	assertRepoMutations(t, fake, []repoFakeReq{
		{Method: http.MethodPatch, Path: "/api/v1/repository/px", Body: map[string]any{"curation_mode": "allowlist"}},
		{Method: http.MethodPatch, Path: curationBase + "/r1", Body: map[string]any{"reason": "confirmed malware"}},
		{Method: http.MethodPost, Path: curationBase, Body: map[string]any{"action": "allow", "package_pattern": "react"}},
		{Method: http.MethodDelete, Path: curationBase + "/r2"},
	})
}

func TestRepositoryValidateCuration(t *testing.T) {
	t.Parallel()

	rule := func(action, pkg string) map[string]any {
		return map[string]any{"action": action, "package_pattern": pkg}
	}
	proxy := func(extra map[string]any) *Object {
		spec := map[string]any{"format": "npm", "repo_type": "proxy"}
		maps.Copy(spec, extra)

		return &Object{Name: "r", Spec: spec}
	}

	tests := []struct {
		name    string
		obj     *Object
		wantErr string
	}{
		{name: "valid", obj: proxy(map[string]any{"curation_mode": "denylist", "curation_rules": []any{rule("block", "evil-*")}})},
		{
			name:    "mode on hosted",
			obj:     &Object{Name: "r", Spec: map[string]any{"format": "npm", "curation_mode": "denylist"}},
			wantErr: "curation_mode is only valid for proxy",
		},
		{
			name: "rules on group",
			obj: &Object{
				Name: "r",
				Spec: map[string]any{"format": "npm", "repo_type": "group", "curation_rules": []any{rule("block", "x")}},
			},
			wantErr: "curation_rules is only valid for proxy",
		},
		{
			name:    "rule with a blank package pattern",
			obj:     proxy(map[string]any{"curation_rules": []any{rule("block", " ")}}),
			wantErr: "curation_rules[0]",
		},
		{
			name:    "rule with a pattern the server refuses",
			obj:     proxy(map[string]any{"curation_rules": []any{rule("block", "a b")}}),
			wantErr: "printable text without spaces",
		},
	}

	kind, ok := NewRepositoryKind().(Validator)
	if !ok {
		t.Fatal("Repository kind does not implement Validator")
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			errs := kind.Validate(tc.obj)
			if tc.wantErr == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected errors: %v", errs)
				}

				return
			}

			if joined := errors.Join(errs...); joined == nil || !strings.Contains(joined.Error(), tc.wantErr) {
				t.Fatalf("errors = %v, want one containing %q", joined, tc.wantErr)
			}
		})
	}
}

// Rules are identified by action and both patterns, so two rules that differ
// only in the version pattern are two set elements, and order does not matter.
func TestRepositoryCurationRulesDiff(t *testing.T) {
	t.Parallel()

	schema := NewRepositoryKind().Schema()
	a := map[string]any{"action": "block", "package_pattern": "x", "version_pattern": "1.*", "reason": "r"}
	b := map[string]any{"action": "block", "package_pattern": "x", "version_pattern": "2.*", "reason": "r"}

	same := diffSpec(schema, map[string]any{"curation_rules": []any{a, b}}, map[string]any{"curation_rules": []any{b, a}}, nil, false)
	if len(same.Changes) != 0 {
		t.Errorf("reordered rules differ: %v", same.Changes)
	}

	added := diffSpec(schema, map[string]any{"curation_rules": []any{a, b}}, map[string]any{"curation_rules": []any{a}}, nil, false)
	if len(added.Changes) != 1 || added.Changes[0].Path != "curation_rules[block|x|2.*]" {
		t.Errorf("changes = %+v, want one add of curation_rules[block|x|2.*]", added.Changes)
	}
}
