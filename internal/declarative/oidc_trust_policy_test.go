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
	"strconv"
	"strings"
	"testing"
)

const (
	githubIssuer = "https://token.actions.githubusercontent.com"

	// The parts of oidcPolicyManifestSpec, the spec of the policy most tests
	// start from.
	oidcIssuerLine     = "  issuer: https://token.actions.githubusercontent.com\n"
	oidcAudienceLine   = "  audience: https://oa.example.org\n"
	oidcAccountLine    = "  service_account: ci-bot\n"
	oidcConditionLines = "  conditions:\n    - claim: repository\n      patterns: [acme/*]\n" +
		"    - claim: ref\n      patterns: [refs/heads/main, refs/tags/v*]\n"
	oidcTTLLine     = "  token_ttl_seconds: 600\n"
	oidcEnabledLine = "  enabled: true\n"

	oidcPolicyManifestSpec = oidcIssuerLine + oidcAudienceLine + oidcAccountLine + oidcConditionLines + oidcTTLLine + oidcEnabledLine
)

func oidcCondition(claim string, patterns ...string) any {
	list := make([]any, len(patterns))
	for i, p := range patterns {
		list[i] = p
	}

	return map[string]any{"claim": claim, "patterns": list}
}

// oidcPolicySpec is a valid policy spec as the loader produces it, changed by
// mutate.
func oidcPolicySpec(mutate ...func(spec map[string]any)) map[string]any {
	spec := map[string]any{
		"issuer":            githubIssuer,
		"audience":          "https://oa.example.org",
		"service_account":   "ci-bot",
		"conditions":        []any{oidcCondition("repository", "acme/*"), oidcCondition("ref", "refs/heads/main", "refs/tags/v*")},
		"token_ttl_seconds": float64(600),
		"enabled":           true,
	}

	for _, m := range mutate {
		m(spec)
	}

	return spec
}

// oidcPolicyLive is the policy "gh" of oidcPolicyManifestSpec as the server
// lists it.
func oidcPolicyLive(mutate ...func(spec map[string]any)) *Live {
	spec := oidcPolicySpec(func(spec map[string]any) {
		// the live list is canonical: conditions sorted by claim
		spec["conditions"] = []any{oidcCondition("ref", "refs/heads/main", "refs/tags/v*"), oidcCondition("repository", "acme/*")}
	})

	for _, m := range mutate {
		m(spec)
	}

	return &Live{Kind: KindOIDCTrustPolicy, Key: "gh", ID: "p-gh", Spec: spec}
}

func identPolicyRoutes(extra ...map[string]identRoute) map[string]identRoute {
	routes := identServiceAccountRoutes(
		map[string]any{"id": "sa-ci", "name": "ci-bot"},
		map[string]any{"id": "sa-deploy", "name": "deploy-bot"},
	)
	routes["GET /api/v1/users"] = identJSON(http.StatusOK, map[string]any{"data": []map[string]any{}, "total": 0})
	routes["GET /api/v1/groups"] = identJSON(http.StatusOK, map[string]any{"data": []map[string]any{}})

	for _, e := range extra {
		maps.Copy(routes, e)
	}

	return routes
}

func TestOIDCPolicyKindList(t *testing.T) {
	t.Parallel()

	env, _ := newIdentEnv(t, map[string]identRoute{
		"GET /api/v1/oidc-trust-policies": identJSON(http.StatusOK, map[string]any{"data": []map[string]any{{
			"id": "p-gh", "name": "gh", "issuer": githubIssuer, "audience": "https://oa.example.org",
			"conditions": []map[string]any{
				{"claim": "repository", "patterns": []string{"acme/*"}},
				{"claim": "ref", "patterns": []string{"refs/tags/v*", "refs/heads/main"}},
			},
			"service_account_id": "sa-ci", "service_account": "ci-bot", "token_ttl_seconds": 600, "enabled": true,
			"created_by": "admin", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
		}, {
			"id": "p-off", "name": "off", "issuer": "https://gitlab.example.org", "audience": "oa",
			"conditions":         []map[string]any{{"claim": "sub", "patterns": []string{"project_path:acme/*"}}},
			"service_account_id": "sa-deploy", "service_account": "deploy-bot", "token_ttl_seconds": 3600, "enabled": false,
		}}}),
	})

	got, err := NewOIDCTrustPolicyKind().List(context.Background(), env)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := []*Live{
		oidcPolicyLive(),
		{Kind: KindOIDCTrustPolicy, Key: "off", ID: "p-off", Spec: map[string]any{
			"issuer": "https://gitlab.example.org", "audience": "oa",
			"conditions":      []any{oidcCondition("sub", "project_path:acme/*")},
			"service_account": "deploy-bot", "token_ttl_seconds": float64(3600), "enabled": false,
		}},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %+v, want %+v", got, want)
	}
}

func TestOIDCPolicyKindListError(t *testing.T) {
	t.Parallel()

	env, _ := newIdentEnv(t, map[string]identRoute{
		"GET /api/v1/oidc-trust-policies": identJSON(http.StatusInternalServerError, map[string]any{"error": "boom"}),
	})

	_, err := NewOIDCTrustPolicyKind().List(context.Background(), env)
	if !IsStatus(err, http.StatusInternalServerError) || !strings.Contains(err.Error(), "list OIDC trust policies") {
		t.Errorf("List error = %v, want the 500 named as the policy listing", err)
	}
}

func TestOIDCPolicyKindCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    map[string]any
		want    map[string]any
		wantErr error
	}{
		{
			name: "sends the service account as its id",
			spec: oidcPolicySpec(),
			want: map[string]any{
				"name": "gh", "issuer": githubIssuer, "audience": "https://oa.example.org", "service_account_id": "sa-ci",
				"conditions":        []any{oidcCondition("repository", "acme/*"), oidcCondition("ref", "refs/heads/main", "refs/tags/v*")},
				"token_ttl_seconds": float64(600), "enabled": true,
			},
		},
		{
			name: "leaves the lifetime and the enabled flag to the server defaults",
			spec: oidcPolicySpec(func(spec map[string]any) {
				delete(spec, "token_ttl_seconds")
				delete(spec, "enabled")
			}),
			want: map[string]any{
				"name": "gh", "issuer": githubIssuer, "audience": "https://oa.example.org", "service_account_id": "sa-ci",
				"conditions": []any{oidcCondition("repository", "acme/*"), oidcCondition("ref", "refs/heads/main", "refs/tags/v*")},
			},
		},
		{
			name:    "an unknown service account sends nothing",
			spec:    oidcPolicySpec(func(spec map[string]any) { spec["service_account"] = "ghost" }),
			wantErr: ErrPrincipalNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env, fake := newIdentEnv(t, identPolicyRoutes(map[string]identRoute{
				"POST /api/v1/oidc-trust-policies": identJSON(http.StatusCreated, map[string]any{"id": "p-gh", "name": "gh"}),
			}))

			id, err := NewOIDCTrustPolicyKind().Create(context.Background(), env, &Object{Kind: KindOIDCTrustPolicy, Name: "gh", Spec: tc.spec})

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), `"ghost"`) {
					t.Errorf("Create error = %v, want %v naming ghost", err, tc.wantErr)
				}

				identAssertCalls(t, fake.writes(), nil)

				return
			}

			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			if id != "p-gh" {
				t.Errorf("id = %q, want p-gh", id)
			}

			identAssertCalls(t, fake.writes(), []identCall{{Method: http.MethodPost, URI: "/api/v1/oidc-trust-policies", Body: tc.want}})
		})
	}
}

func TestOIDCPolicyKindUpdate(t *testing.T) {
	t.Parallel()

	allConditions := []any{oidcCondition("repository", "acme/*", "acme-labs/*"), oidcCondition("ref", "refs/heads/main")}

	tests := []struct {
		name    string
		desired map[string]any
		changes []Change
		want    map[string]any
		wantErr error
	}{
		{
			name:    "scalar changes patch only the changed fields",
			desired: oidcPolicySpec(),
			changes: []Change{
				{Path: "token_ttl_seconds", Old: float64(900), New: float64(600)},
				{Path: "enabled", Old: false, New: true},
				{Path: "audience", Old: "old", New: "https://oa.example.org"},
			},
			want: map[string]any{"token_ttl_seconds": float64(600), "enabled": true, "audience": "https://oa.example.org"},
		},
		{
			name:    "a changed pattern resends every condition once",
			desired: oidcPolicySpec(func(spec map[string]any) { spec["conditions"] = allConditions }),
			changes: []Change{
				{Path: "conditions[repository].patterns", Old: []any{"acme/*"}, New: []any{"acme/*", "acme-labs/*"}},
				{Path: "conditions[ref].patterns", Old: []any{"refs/tags/v*"}, New: []any{"refs/heads/main"}},
			},
			want: map[string]any{"conditions": allConditions},
		},
		{
			name:    "an added condition resends the list",
			desired: oidcPolicySpec(func(spec map[string]any) { spec["conditions"] = allConditions }),
			changes: []Change{{Path: "conditions[ref]", New: oidcCondition("ref", "refs/heads/main")}},
			want:    map[string]any{"conditions": allConditions},
		},
		{
			name:    "a changed service account is sent as its id",
			desired: oidcPolicySpec(func(spec map[string]any) { spec["service_account"] = "deploy-bot" }),
			changes: []Change{{Path: "service_account", Old: "ci-bot", New: "deploy-bot"}},
			want:    map[string]any{"service_account_id": "sa-deploy"},
		},
		{
			name:    "an unknown service account sends nothing",
			desired: oidcPolicySpec(func(spec map[string]any) { spec["service_account"] = "ghost" }),
			changes: []Change{{Path: "service_account", Old: "ci-bot", New: "ghost"}},
			wantErr: ErrPrincipalNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env, fake := newIdentEnv(t, identPolicyRoutes(map[string]identRoute{
				"PATCH /api/v1/oidc-trust-policies/p-gh": identJSON(http.StatusOK, map[string]any{}),
			}))

			id, err := NewOIDCTrustPolicyKind().Update(
				context.Background(), env, oidcPolicyLive(), &Object{Name: "gh", Spec: tc.desired}, tc.changes)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), `"ghost"`) {
					t.Errorf("Update error = %v, want %v naming ghost", err, tc.wantErr)
				}

				identAssertCalls(t, fake.writes(), nil)

				return
			}

			if err != nil {
				t.Fatalf("Update: %v", err)
			}

			if id != "p-gh" {
				t.Errorf("id = %q, want p-gh", id)
			}

			identAssertCalls(
				t,
				fake.writes(),
				[]identCall{{Method: http.MethodPatch, URI: "/api/v1/oidc-trust-policies/p-gh", Body: tc.want}},
			)
		})
	}
}

func TestOIDCPolicyKindDelete(t *testing.T) {
	t.Parallel()

	env, fake := newIdentEnv(t, map[string]identRoute{
		"DELETE /api/v1/oidc-trust-policies/p-gh": identJSON(http.StatusNoContent, nil),
	})

	if err := NewOIDCTrustPolicyKind().Delete(context.Background(), env, &Live{Key: "gh", ID: "p-gh"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	identAssertCalls(t, fake.writes(), []identCall{{Method: http.MethodDelete, URI: "/api/v1/oidc-trust-policies/p-gh"}})
}

func TestOIDCPolicyKindErrorsKeepAPIError(t *testing.T) {
	t.Parallel()

	k := NewOIDCTrustPolicyKind()
	ctx := context.Background()

	tests := []struct {
		name   string
		status int
		call   func(env *Env) error
	}{
		{"create", http.StatusBadRequest, func(env *Env) error {
			_, err := k.Create(ctx, env, &Object{Name: "gh", Spec: oidcPolicySpec()})
			return err
		}},
		{"update", http.StatusConflict, func(env *Env) error {
			_, err := k.Update(
				ctx,
				env,
				oidcPolicyLive(),
				&Object{Name: "gh", Spec: oidcPolicySpec()},
				[]Change{{Path: "enabled", New: true}},
			)
			return err
		}},
		{"delete", http.StatusInternalServerError, func(env *Env) error {
			return k.Delete(ctx, env, &Live{Key: "gh", ID: "p-gh"})
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rejected := identJSON(tc.status, map[string]any{"error": "rejected"})
			env, _ := newIdentEnv(t, identPolicyRoutes(map[string]identRoute{
				"POST /api/v1/oidc-trust-policies":        rejected,
				"PATCH /api/v1/oidc-trust-policies/p-gh":  rejected,
				"DELETE /api/v1/oidc-trust-policies/p-gh": rejected,
			}))

			if err := tc.call(env); !IsStatus(err, tc.status) || !strings.Contains(err.Error(), "OIDC trust policy") {
				t.Errorf("%s error = %v, want an *APIError with status %d naming the policy", tc.name, err, tc.status)
			}
		})
	}
}

func TestOIDCPolicyKindMetadata(t *testing.T) {
	t.Parallel()

	k := NewOIDCTrustPolicyKind()

	if k.Name() != "OIDCTrustPolicy" || k.Rank() != 55 {
		t.Errorf("Name, Rank = %q, %d, want OIDCTrustPolicy, 55", k.Name(), k.Rank())
	}

	want := []Ref{{Kind: KindServiceAccount, Key: "ci-bot", Field: "service_account"}}
	if got := k.Refs(&Object{Name: "gh", Spec: oidcPolicySpec()}); !reflect.DeepEqual(got, want) {
		t.Errorf("Refs = %+v, want %+v", got, want)
	}

	ttl := k.Schema().Fields["token_ttl_seconds"]
	if ttl == nil || ttl.Type != TInt || !ttl.HasMin || ttl.Min != 60 {
		t.Errorf("token_ttl_seconds schema = %+v, want an integer of at least 60", ttl)
	}

	for _, required := range []string{"issuer", "audience", "conditions", "service_account"} {
		if f := k.Schema().Fields[required]; f == nil || !f.Required {
			t.Errorf("%s must be a required field", required)
		}
	}
}

func TestOIDCPolicyKindValidate(t *testing.T) {
	t.Parallel()

	setIssuer := func(issuer string) func(map[string]any) {
		return func(spec map[string]any) { spec["issuer"] = issuer }
	}
	setAudience := func(audience string) func(map[string]any) {
		return func(spec map[string]any) { spec["audience"] = audience }
	}
	setTTL := func(ttl float64) func(map[string]any) {
		return func(spec map[string]any) { spec["token_ttl_seconds"] = ttl }
	}
	setConditions := func(conditions ...any) func(map[string]any) {
		return func(spec map[string]any) { spec["conditions"] = conditions }
	}

	manyConditions := make([]any, 17)
	for i := range manyConditions {
		manyConditions[i] = oidcCondition("claim"+strconv.Itoa(i), "x")
	}

	tests := []struct {
		name    string
		policy  string
		spec    map[string]any
		wantErr string
	}{
		{name: "valid", policy: "gh-main_1.0", spec: oidcPolicySpec()},
		{name: "http issuer", spec: oidcPolicySpec(setIssuer("http://localhost:8080/realm"))},
		{name: "name starting with a dash", policy: "-gh", spec: oidcPolicySpec(), wantErr: "metadata.name"},
		{name: "name too long", policy: strings.Repeat("a", 65), spec: oidcPolicySpec(), wantErr: "metadata.name"},
		{name: "name with a space", policy: "gh main", spec: oidcPolicySpec(), wantErr: "metadata.name"},
		{name: "issuer that is no URL", spec: oidcPolicySpec(setIssuer("github")), wantErr: "issuer"},
		{name: "issuer with another scheme", spec: oidcPolicySpec(setIssuer("ftp://example.org")), wantErr: "issuer must be an https URL"},
		{name: "issuer with a query", spec: oidcPolicySpec(setIssuer("https://example.org/?a=b")), wantErr: "issuer"},
		{name: "issuer with a fragment", spec: oidcPolicySpec(setIssuer("https://example.org/#a")), wantErr: "issuer"},
		{name: "issuer with credentials", spec: oidcPolicySpec(setIssuer("https://user:pw@example.org")), wantErr: "issuer"},
		{name: "issuer too long", spec: oidcPolicySpec(setIssuer("https://example.org/" + strings.Repeat("a", 512))), wantErr: "issuer"},
		{name: "empty audience", spec: oidcPolicySpec(setAudience("")), wantErr: "audience"},
		{name: "audience with surrounding spaces", spec: oidcPolicySpec(setAudience(" oa ")), wantErr: "audience"},
		{name: "audience too long", spec: oidcPolicySpec(setAudience(strings.Repeat("a", 256))), wantErr: "audience"},
		{name: "longest lifetime", spec: oidcPolicySpec(setTTL(3600))},
		{name: "lifetime above one hour", spec: oidcPolicySpec(setTTL(3601)), wantErr: "token_ttl_seconds must be between 60 and 3600"},
		{name: "no conditions", spec: oidcPolicySpec(setConditions()), wantErr: "conditions: between 1 and 16"},
		{name: "too many conditions", spec: oidcPolicySpec(setConditions(manyConditions...)), wantErr: "conditions: between 1 and 16"},
		{name: "claim that is no name", spec: oidcPolicySpec(setConditions(oidcCondition("re po", "x"))), wantErr: "conditions[0].claim"},
		{
			name:    "claim listed twice",
			spec:    oidcPolicySpec(setConditions(oidcCondition("sub", "a"), oidcCondition("sub", "b"))),
			wantErr: `claim "sub" is listed twice`,
		},
		{
			name:    "condition without patterns",
			spec:    oidcPolicySpec(setConditions(oidcCondition("sub"))),
			wantErr: "conditions[0].patterns: between 1 and 8",
		},
		{
			name:    "too many patterns",
			spec:    oidcPolicySpec(setConditions(oidcCondition("sub", "1", "2", "3", "4", "5", "6", "7", "8", "9"))),
			wantErr: "conditions[0].patterns: between 1 and 8",
		},
		{name: "pattern of wildcards only", spec: oidcPolicySpec(setConditions(oidcCondition("sub", "*"))), wantErr: "literal character"},
		{
			name:    "pattern of mixed wildcards",
			spec:    oidcPolicySpec(setConditions(oidcCondition("sub", "?*?"))),
			wantErr: "literal character",
		},
		{name: "wildcard next to a literal", spec: oidcPolicySpec(setConditions(oidcCondition("sub", "*a")))},
		{
			name:    "pattern too long",
			spec:    oidcPolicySpec(setConditions(oidcCondition("sub", strings.Repeat("a", 257)))),
			wantErr: "a pattern is at most 256 characters",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			v, ok := NewOIDCTrustPolicyKind().(Validator)
			if !ok {
				t.Fatal("OIDCTrustPolicy must implement Validator")
			}

			name := tc.policy
			if name == "" {
				name = "gh"
			}

			errs := v.Validate(&Object{Kind: KindOIDCTrustPolicy, Name: name, Spec: tc.spec})

			switch {
			case tc.wantErr == "" && len(errs) != 0:
				t.Errorf("Validate = %v, want none", errs)
			case tc.wantErr != "" && (len(errs) != 1 || !strings.Contains(errs[0].Error(), tc.wantErr)):
				t.Errorf("Validate = %v, want one error containing %q", errs, tc.wantErr)
			}
		})
	}
}

func TestLoadOIDCPolicyRejectsInvalidManifests(t *testing.T) {
	t.Parallel()

	replace := func(old, repl string) string { return strings.Replace(oidcPolicyManifestSpec, old, repl, 1) }

	tests := []struct {
		name    string
		spec    string
		wantErr string
	}{
		{"missing issuer", replace(oidcIssuerLine, ""), "issuer"},
		{"missing audience", replace(oidcAudienceLine, ""), "audience"},
		{"missing service account", replace(oidcAccountLine, ""), "service_account"},
		{"missing conditions", replace(oidcConditionLines, ""), "conditions"},
		{"lifetime below one minute", replace(oidcTTLLine, "  token_ttl_seconds: 59\n"), "token_ttl_seconds"},
		{"lifetime above one hour", replace(oidcTTLLine, "  token_ttl_seconds: 3601\n"), "token_ttl_seconds"},
		{"unknown field", oidcPolicyManifestSpec + "  subject: x\n", "subject"},
		{"condition without patterns", replace("      patterns: [acme/*]\n", ""), "patterns"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := loadYAML(
				t,
				NewRegistry(NewServiceAccountKind(), NewOIDCTrustPolicyKind()),
				manifestOf(KindOIDCTrustPolicy, "gh", tc.spec),
			)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("load error = %v, want one mentioning %q", err, tc.wantErr)
			}
		})
	}
}

func TestPlanOIDCPolicies(t *testing.T) {
	t.Parallel()

	serviceAccounts := []*Live{
		{Kind: KindServiceAccount, Key: "ci-bot", ID: "sa-ci", Spec: map[string]any{"description": "", "disabled": false}},
		{Kind: KindServiceAccount, Key: "deploy-bot", ID: "sa-deploy", Spec: map[string]any{"description": "", "disabled": false}},
	}

	// replace edits the base manifest spec.
	replace := func(old, repl string) string { return strings.Replace(oidcPolicyManifestSpec, old, repl, 1) }

	tests := []struct {
		name     string
		manifest string
		live     []*Live
		managed  bool
		prune    bool
		want     []string
	}{
		{name: "create", manifest: oidcPolicyManifestSpec, want: []string{"create gh"}},
		{name: "unchanged", manifest: oidcPolicyManifestSpec, live: []*Live{oidcPolicyLive()}, want: []string{"unchanged gh"}},
		{
			name: "conditions and patterns are sets",
			manifest: oidcIssuerLine + oidcAudienceLine + oidcAccountLine +
				"  conditions:\n    - claim: ref\n      patterns: [refs/tags/v*, refs/heads/main]\n" +
				"    - claim: repository\n      patterns: [acme/*]\n" + oidcTTLLine + oidcEnabledLine,
			live: []*Live{oidcPolicyLive()}, want: []string{"unchanged gh"},
		},
		{
			name:     "the defaults are left to the server",
			manifest: strings.NewReplacer(oidcTTLLine, "", oidcEnabledLine, "").Replace(oidcPolicyManifestSpec),
			live:     []*Live{oidcPolicyLive(func(spec map[string]any) { spec["token_ttl_seconds"] = float64(900) })},
			want:     []string{"unchanged gh"},
		},
		{
			name: "a longer lifetime", manifest: replace("token_ttl_seconds: 600", "token_ttl_seconds: 1200"),
			live: []*Live{oidcPolicyLive()}, want: []string{"update gh [token_ttl_seconds]"},
		},
		{
			name: "disable", manifest: replace("enabled: true", "enabled: false"),
			live: []*Live{oidcPolicyLive()}, want: []string{"update gh [enabled]"},
		},
		{
			name: "another audience", manifest: replace("https://oa.example.org", "https://registry.example.org"),
			live: []*Live{oidcPolicyLive()}, want: []string{"update gh [audience]"},
		},
		{
			name: "another service account", manifest: replace("service_account: ci-bot", "service_account: deploy-bot"),
			live: []*Live{oidcPolicyLive()}, want: []string{"update gh [service_account]"},
		},
		{
			name: "a pattern added to a condition", manifest: replace("[acme/*]", "[acme/*, acme-labs/*]"),
			live: []*Live{oidcPolicyLive()}, want: []string{"update gh [conditions[repository].patterns]"},
		},
		{
			name:     "a condition added",
			manifest: replace(oidcConditionLines, oidcConditionLines+"    - claim: environment\n      patterns: [production]\n"),
			live:     []*Live{oidcPolicyLive()}, want: []string{"update gh [conditions[environment]]"},
		},
		{name: "prune deletes a managed policy", live: []*Live{oidcPolicyLive()}, managed: true, prune: true, want: []string{"delete gh"}},
		{name: "prune leaves an unmanaged policy", live: []*Live{oidcPolicyLive()}, prune: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reg := NewRegistry(NewServiceAccountKind(), NewOIDCTrustPolicyKind())
			in := PlanInput{
				Live:  map[string][]*Live{KindServiceAccount: serviceAccounts, KindOIDCTrustPolicy: tc.live},
				Prune: tc.prune,
				Kinds: map[string]bool{KindOIDCTrustPolicy: true},
			}

			if tc.managed {
				in.Markers = Markers{Ident(KindOIDCTrustPolicy, "gh"): {Kind: KindOIDCTrustPolicy, Name: "gh", ObjectID: "p-gh"}}
			}

			if tc.manifest != "" {
				objs, err := loadYAML(t, reg, manifestOf(KindOIDCTrustPolicy, "gh", tc.manifest))
				if err != nil {
					t.Fatalf("load: %v", err)
				}

				in.Desired = objs
			}

			plan := planFor(reg, in)
			if errs := plan.Errors(); len(errs) != 0 {
				t.Fatalf("plan errors: %v", errs)
			}

			if got := planSummary(plan); !reflect.DeepEqual(got, tc.want) && (len(got) != 0 || len(tc.want) != 0) {
				t.Errorf("plan = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPlanOIDCPolicyNeedsItsServiceAccount(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(NewServiceAccountKind(), NewOIDCTrustPolicyKind())
	policy := manifestOf(KindOIDCTrustPolicy, "gh", oidcPolicyManifestSpec)
	account := manifestOf(KindServiceAccount, "ci-bot", "  description: CI pipelines\n")

	tests := []struct {
		name     string
		manifest string
		live     []*Live
		want     []string
		wantErr  string
	}{
		{
			name: "created in the same manifest, before the policy", manifest: policy + "---\n" + account,
			want: []string{"create ci-bot", "create gh"},
		},
		{
			name:     "already on the server",
			manifest: policy,
			live: []*Live{
				{Kind: KindServiceAccount, Key: "ci-bot", ID: "sa-ci", Spec: map[string]any{"description": "", "disabled": false}},
			},
			want: []string{"create gh"},
		},
		{name: "nowhere", manifest: policy, wantErr: `service_account references ServiceAccount "ci-bot"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			objs, err := loadYAML(t, reg, tc.manifest)
			if err != nil {
				t.Fatalf("load: %v", err)
			}

			plan := planFor(reg, PlanInput{Desired: objs, Live: map[string][]*Live{KindServiceAccount: tc.live}})

			if tc.wantErr != "" {
				if errs := plan.Errors(); len(errs) != 1 || !strings.Contains(errs[0].Error(), tc.wantErr) {
					t.Errorf("plan errors = %v, want one containing %q", errs, tc.wantErr)
				}

				return
			}

			if errs := plan.Errors(); len(errs) != 0 {
				t.Fatalf("plan errors: %v", errs)
			}

			if got := planSummary(plan); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("plan = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPlanDeletesOIDCPolicyBeforeItsServiceAccount(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(NewServiceAccountKind(), NewOIDCTrustPolicyKind())
	markers := Markers{
		Ident(KindServiceAccount, "ci-bot"): {Kind: KindServiceAccount, Name: "ci-bot", ObjectID: "sa-ci"},
		Ident(KindOIDCTrustPolicy, "gh"):    {Kind: KindOIDCTrustPolicy, Name: "gh", ObjectID: "p-gh"},
	}

	plan := planFor(reg, PlanInput{
		Prune: true, Markers: markers,
		Live: map[string][]*Live{
			KindServiceAccount: {
				{Kind: KindServiceAccount, Key: "ci-bot", ID: "sa-ci", Spec: map[string]any{"description": "", "disabled": false}},
			},
			KindOIDCTrustPolicy: {oidcPolicyLive()},
		},
	})

	if got, want := planSummary(plan), []string{"delete gh", "delete ci-bot"}; !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %v, want %v", got, want)
	}
}

func TestOIDCPolicyExportRoundTripsToNoChanges(t *testing.T) {
	t.Parallel()

	k := NewOIDCTrustPolicyKind()
	reg := NewRegistry(NewServiceAccountKind(), k)
	live := oidcPolicyLive()

	raw, err := exportObject(k, live, false)
	if err != nil {
		t.Fatal(err)
	}

	text := string(raw)
	for _, want := range []string{"kind: OIDCTrustPolicy", "name: gh", "service_account: ci-bot", "token_ttl_seconds: 600"} {
		if !strings.Contains(text, want) {
			t.Errorf("export missing %q:\n%s", want, text)
		}
	}

	objs, err := loadYAML(t, reg, text)
	if err != nil {
		t.Fatalf("exported manifest does not load: %v\n%s", err, text)
	}

	plan := planFor(reg, PlanInput{
		Desired: objs,
		Live: map[string][]*Live{
			KindServiceAccount: {
				{Kind: KindServiceAccount, Key: "ci-bot", ID: "sa-ci", Spec: map[string]any{"description": "", "disabled": false}},
			},
			KindOIDCTrustPolicy: {live},
		},
	})

	if plan.HasChanges() || len(plan.Errors()) != 0 {
		t.Fatalf("re-applying the export is not a no-op: %v %v", planSummary(plan), plan.Errors())
	}
}
