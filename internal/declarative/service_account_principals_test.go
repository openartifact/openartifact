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
	"sync/atomic"
	"testing"
)

// withServiceAccounts adds the service account list to routes.
func withServiceAccounts(routes map[string]identRoute, accounts ...map[string]any) map[string]identRoute {
	maps.Copy(routes, identServiceAccountRoutes(accounts...))

	return routes
}

func TestUserRefAcceptsServiceAccounts(t *testing.T) {
	t.Parallel()

	ref := userRef("ci-bot", "members[0]")

	want := Ref{Kind: KindUser, Also: []string{KindServiceAccount}, Key: "ci-bot", Field: "members[0]"}
	if !reflect.DeepEqual(ref, want) {
		t.Errorf("userRef = %+v, want %+v", ref, want)
	}

	if got := ref.kinds(); !reflect.DeepEqual(got, []string{KindUser, KindServiceAccount}) {
		t.Errorf("kinds = %v, want User then ServiceAccount", got)
	}

	if got := (Ref{Kind: KindGroup}).kinds(); !reflect.DeepEqual(got, []string{KindGroup}) {
		t.Errorf("kinds of a plain reference = %v, want just its own", got)
	}
}

func TestIdentsResolveServiceAccounts(t *testing.T) {
	t.Parallel()

	env, _ := newIdentEnv(t, withServiceAccounts(identDirectoryRoutes(), map[string]any{"id": "sa-ci", "name": "ci-bot"}))
	ctx := context.Background()

	for name, want := range map[string]string{"alice": "u-alice", "ci-bot": "sa-ci"} {
		if got, err := env.UserID(ctx, name); err != nil || got != want {
			t.Errorf("UserID(%q) = %q, %v, want %q", name, got, err, want)
		}
	}

	if got, err := env.ServiceAccountID(ctx, "ci-bot"); err != nil || got != "sa-ci" {
		t.Errorf("ServiceAccountID(ci-bot) = %q, %v, want sa-ci", got, err)
	}

	// A human user is no service account: a trust policy cannot target one.
	if _, err := env.ServiceAccountID(ctx, "alice"); !errors.Is(err, ErrPrincipalNotFound) {
		t.Errorf("ServiceAccountID(alice) error = %v, want ErrPrincipalNotFound", err)
	}

	names, err := env.UserNames(ctx)
	if err != nil {
		t.Fatalf("UserNames: %v", err)
	}

	if want := map[string]string{"u-alice": "alice", "u-bob": "bob", "sa-ci": "ci-bot"}; !reflect.DeepEqual(names, want) {
		t.Errorf("UserNames = %v, want %v", names, want)
	}
}

func TestIdentsServiceAccountEndpointFailures(t *testing.T) {
	t.Parallel()

	t.Run("a server without service accounts still resolves users", func(t *testing.T) {
		t.Parallel()

		env, _ := newIdentEnv(t, identDirectoryRoutes())

		if got, err := env.UserID(context.Background(), "alice"); err != nil || got != "u-alice" {
			t.Errorf("UserID(alice) = %q, %v, want u-alice", got, err)
		}

		if _, err := env.ServiceAccountID(context.Background(), "ci-bot"); !errors.Is(err, ErrPrincipalNotFound) {
			t.Errorf("ServiceAccountID(ci-bot) error = %v, want ErrPrincipalNotFound", err)
		}
	})

	t.Run("any other failure is reported", func(t *testing.T) {
		t.Parallel()

		routes := identDirectoryRoutes()
		routes["GET /api/v1/service-accounts"] = identJSON(http.StatusInternalServerError, map[string]any{"error": "boom"})
		env, _ := newIdentEnv(t, routes)

		_, err := env.UserID(context.Background(), "alice")
		if !IsStatus(err, http.StatusInternalServerError) || !strings.Contains(err.Error(), "list service accounts") {
			t.Errorf("UserID error = %v, want the 500 named as the service account listing", err)
		}
	})
}

func TestPermissionTargetKindAssignsServiceAccounts(t *testing.T) {
	t.Parallel()

	spec := map[string]any{
		"repository_filter": "ci-.*", "path_pattern": "*", "actions": []any{"write"},
		"assignments": map[string]any{"users": []any{"ci-bot"}},
	}

	routes := withServiceAccounts(identPermDirectory(), map[string]any{"id": "sa-ci", "name": "ci-bot"})
	routes["POST /api/v1/permission-targets"] = identJSON(http.StatusCreated, map[string]any{"id": "t-new", "name": "ci"})
	routes["POST /api/v1/permission-targets/t-new/assignments"] = identJSON(http.StatusNoContent, nil)
	env, fake := newIdentEnv(t, routes)

	if _, err := NewPermissionTargetKind().Create(context.Background(), env, &Object{Name: "ci", Spec: spec}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	identAssertCalls(t, fake.writes(), []identCall{
		{Method: http.MethodPost, URI: "/api/v1/permission-targets", Body: map[string]any{
			"name": "ci", "repository_filter": "ci-.*", "path_pattern": "*", "actions": []any{"write"},
		}},
		{
			Method: http.MethodPost,
			URI:    "/api/v1/permission-targets/t-new/assignments",
			Body:   map[string]any{"principal_type": "user", "principal_id": "sa-ci"},
		},
	})
}

func TestPermissionTargetKindUnassignsServiceAccounts(t *testing.T) {
	t.Parallel()

	routes := withServiceAccounts(identPermDirectory(), map[string]any{"id": "sa-ci", "name": "ci-bot"})
	routes["DELETE /api/v1/permission-targets/t-ci/assignments"] = identJSON(http.StatusNoContent, nil)
	env, fake := newIdentEnv(t, routes)

	live := &Live{ID: "t-ci", Key: "ci", Spec: map[string]any{
		"assignments": map[string]any{"users": []any{"alice", "ci-bot"}, "groups": []any{}},
	}}
	desired := &Object{Name: "ci", Spec: map[string]any{"assignments": map[string]any{"users": []any{"alice"}}}}
	changes := []Change{{Path: "assignments.users", Old: []any{"alice", "ci-bot"}, New: []any{"alice"}}}

	if _, err := NewPermissionTargetKind().Update(context.Background(), env, live, desired, changes); err != nil {
		t.Fatalf("Update: %v", err)
	}

	identAssertCalls(t, fake.writes(), []identCall{
		{Method: http.MethodDelete, URI: "/api/v1/permission-targets/t-ci/assignments?principal_id=sa-ci&principal_type=user"},
	})
}

func TestPermissionTargetKindListsServiceAccountAssignments(t *testing.T) {
	t.Parallel()

	env, _ := newIdentEnv(t, map[string]identRoute{
		"GET /api/v1/permission-targets": identJSON(http.StatusOK, map[string]any{"data": []map[string]any{
			{"id": "t-ci", "name": "ci", "repository_filter": "ci-.*", "path_pattern": "*", "actions": []string{"write"}},
		}}),
		"GET /api/v1/permission-targets/t-ci/assignments": identJSON(http.StatusOK, identAssignments(
			[3]string{"user", "u-alice", "alice"}, [3]string{"user", "sa-ci", "ci-bot"},
		)),
	})

	got, err := NewPermissionTargetKind().List(context.Background(), env)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("List returned %d targets, want 1", len(got))
	}

	users := got[0].Spec["assignments"].(map[string]any)["users"]
	if want := []any{"alice", "ci-bot"}; !reflect.DeepEqual(users, want) {
		t.Errorf("assignments.users = %v, want %v", users, want)
	}
}

func TestProjectKindMembersAcceptServiceAccounts(t *testing.T) {
	t.Parallel()

	routes := withServiceAccounts(identProjectDirectory(),
		map[string]any{"id": "sa-ci", "name": "ci-bot"},
		map[string]any{"id": "sa-deploy", "name": "deploy-bot"},
		map[string]any{"id": "sa-old", "name": "old-bot"},
	)
	routes["POST /api/v1/projects/web/members"] = identJSON(http.StatusCreated, map[string]any{})
	routes["PATCH /api/v1/projects/web/members/sa-ci"] = identJSON(http.StatusOK, map[string]any{})
	routes["DELETE /api/v1/projects/web/members/sa-old"] = identJSON(http.StatusNoContent, nil)
	env, fake := newIdentEnv(t, routes)

	obj := &Object{Name: "web", Spec: map[string]any{"members": identMemberSpec(
		[2]string{"alice", "owner"}, [2]string{"ci-bot", "developer"}, [2]string{"deploy-bot", "developer"},
	)}}
	live := &Live{ID: "p-web", Key: "web", Spec: map[string]any{"members": identMemberSpec(
		[2]string{"alice", "owner"}, [2]string{"ci-bot", "viewer"}, [2]string{"old-bot", "viewer"},
	)}}
	changes := []Change{{Path: "members[ci-bot].role", Old: "viewer", New: "developer"}}

	if _, err := NewProjectKind().Update(context.Background(), env, live, obj, changes); err != nil {
		t.Fatalf("Update: %v", err)
	}

	identAssertCalls(t, fake.writes(), []identCall{
		{Method: http.MethodPost, URI: "/api/v1/projects/web/members", Body: map[string]any{"username": "deploy-bot", "role": "developer"}},
		{Method: http.MethodPatch, URI: "/api/v1/projects/web/members/sa-ci", Body: map[string]any{"role": "developer"}},
		{Method: http.MethodDelete, URI: "/api/v1/projects/web/members/sa-old"},
	})
}

func TestGroupKindMembersAcceptServiceAccounts(t *testing.T) {
	t.Parallel()

	routes := withServiceAccounts(identDirectoryRoutes(), map[string]any{"id": "sa-ci", "name": "ci-bot"})
	routes["POST /api/v1/groups"] = identJSON(http.StatusCreated, map[string]any{"id": "g-new", "name": "pipelines"})
	routes["PUT /api/v1/groups/g-new/members"] = identJSON(http.StatusNoContent, nil)
	env, fake := newIdentEnv(t, routes)

	spec := map[string]any{"members": []any{"alice", "ci-bot"}}
	if _, err := NewGroupKind().Create(context.Background(), env, &Object{Name: "pipelines", Spec: spec}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	identAssertCalls(t, fake.writes(), []identCall{
		{Method: http.MethodPost, URI: "/api/v1/groups", Body: map[string]any{"name": "pipelines"}},
		{Method: http.MethodPut, URI: "/api/v1/groups/g-new/members", Body: map[string]any{"members": []any{"u-alice", "sa-ci"}}},
	})
}

// ciPermissionSpec assigns the service account ci-bot.
const ciPermissionSpec = `  repository_filter: "ci-.*"
  path_pattern: "*"
  actions: [write]
  assignments:
    users: [ci-bot]
`

// accountReferrers names the service account ci-bot from a group, a project
// and a permission target; a trust policy targets it.
const accountReferrers = "---\n" +
	"apiVersion: openartifact.io/v1\nkind: Group\nmetadata:\n  name: pipelines\nspec:\n  members: [ci-bot]\n" +
	"---\n" +
	"apiVersion: openartifact.io/v1\nkind: Project\nmetadata:\n  name: web\nspec:\n  members:\n" +
	"    - {username: alice, role: owner}\n    - {username: ci-bot, role: developer}\n" +
	"---\n" + "apiVersion: openartifact.io/v1\nkind: PermissionTarget\nmetadata:\n  name: ci\nspec:\n" + ciPermissionSpec + "---\n"

func TestPlanAccountReferencesAcceptServiceAccounts(t *testing.T) {
	t.Parallel()

	account := manifestOf(KindServiceAccount, "ci-bot", "  description: CI pipelines\n")
	policy := manifestOf(KindOIDCTrustPolicy, "gh", oidcPolicyManifestSpec)
	referrers := accountReferrers + policy

	liveUsers := []*Live{{Kind: KindUser, Key: "alice", ID: "u-alice", Spec: map[string]any{}}}
	liveAccount := []*Live{
		{Kind: KindServiceAccount, Key: "ci-bot", ID: "sa-ci", Spec: map[string]any{"description": "", "disabled": false}},
	}

	tests := []struct {
		name     string
		manifest string
		accounts []*Live
		want     []string
		wantErrs int
	}{
		{
			name: "created in the same manifest, ahead of everything naming it", manifest: referrers + "---\n" + account,
			want: []string{"create ci-bot", "create pipelines", "create web", "create ci", "create gh"},
		},
		{
			name: "already on the server", manifest: referrers, accounts: liveAccount,
			want: []string{"create pipelines", "create web", "create ci", "create gh"},
		},
		{name: "nowhere", manifest: referrers, wantErrs: 4},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reg := DefaultRegistry()

			objs, err := loadYAML(t, reg, tc.manifest)
			if err != nil {
				t.Fatalf("load: %v", err)
			}

			plan := planFor(reg, PlanInput{Desired: objs, Live: map[string][]*Live{
				KindUser: liveUsers, KindServiceAccount: tc.accounts,
			}})

			errs := plan.Errors()
			if len(errs) != tc.wantErrs {
				t.Fatalf("plan errors = %v, want %d", errs, tc.wantErrs)
			}

			for _, e := range errs {
				if !strings.Contains(e.Error(), `ServiceAccount "ci-bot"`) {
					t.Errorf("error %q does not name the missing account", e)
				}
			}

			if tc.wantErrs == 0 {
				if got := planSummary(plan); !reflect.DeepEqual(got, tc.want) {
					t.Errorf("plan = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// markerWrites splits the requests of an apply run into the API writes and
// the ownership markers they recorded ("kind/name=id").
func markerWrites(calls []identCall) ([]identCall, []string) {
	var (
		api     []identCall
		markers []string
	)

	for _, c := range calls {
		if c.URI != markersPath {
			api = append(api, c)
			continue
		}

		for _, item := range c.Body["items"].([]any) {
			m := item.(map[string]any)
			markers = append(markers, m["kind"].(string)+"/"+m["name"].(string)+"="+m["object_id"].(string))
		}
	}

	return api, markers
}

func TestApplyCreatesServiceAccountBeforeWhatNamesIt(t *testing.T) {
	t.Parallel()

	var created atomic.Bool

	empty := identJSON(http.StatusOK, map[string]any{"data": []map[string]any{}})
	routes := identUsersRoutes(map[string]any{"id": "u-alice", "username": "alice"})
	routes["GET "+markersPath] = identJSON(http.StatusOK, map[string]any{"items": []any{}})
	routes["PUT "+markersPath] = identJSON(http.StatusOK, map[string]any{})
	routes["GET /api/v1/groups"] = empty
	routes["GET /api/v1/permission-targets"] = empty
	routes["GET /api/v1/oidc-trust-policies"] = empty
	routes["GET /api/v1/service-accounts"] = func(*http.Request) (int, any) {
		accounts := []map[string]any{}
		if created.Load() {
			accounts = append(accounts, map[string]any{"id": "sa-ci", "name": "ci-bot"})
		}

		return http.StatusOK, map[string]any{"data": accounts, "total": len(accounts)}
	}
	routes["POST /api/v1/service-accounts"] = func(*http.Request) (int, any) {
		created.Store(true)

		return http.StatusCreated, map[string]any{"id": "sa-ci", "name": "ci-bot"}
	}
	routes["POST /api/v1/permission-targets"] = identJSON(http.StatusCreated, map[string]any{"id": "t-ci", "name": "ci"})
	routes["POST /api/v1/permission-targets/t-ci/assignments"] = identJSON(http.StatusNoContent, nil)
	routes["POST /api/v1/oidc-trust-policies"] = identJSON(http.StatusCreated, map[string]any{"id": "p-gh", "name": "gh"})

	env, fake := newIdentEnv(t, routes)

	manifests := manifestOf(KindServiceAccount, "ci-bot", "  description: CI pipelines\n") +
		"---\n" + manifestOf(KindPermissionTarget, "ci", ciPermissionSpec) +
		"---\n" + manifestOf(KindOIDCTrustPolicy, "gh", oidcPolicyManifestSpec)

	opts := Options{
		Registry: DefaultRegistry(), Client: env.Client,
		Load: LoadOptions{Paths: []string{writeFile(t, t.TempDir(), "ci.yaml", manifests)}},
	}
	ctx := context.Background()

	prepared, err := Prepare(ctx, opts)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if errs := prepared.Plan.Errors(); len(errs) != 0 {
		t.Fatalf("plan errors: %v", errs)
	}

	// The principals were looked up before the account existed: Execute must not
	// resolve the new account from that stale lookup.
	if _, err := prepared.Env.UserID(ctx, "alice"); err != nil {
		t.Fatalf("UserID: %v", err)
	}

	if failed := Failed(Execute(ctx, opts, prepared)); len(failed) != 0 {
		t.Fatalf("Execute failed: %+v", failed[0].Err)
	}

	api, markers := markerWrites(fake.writes())

	wantAPI := []identCall{
		{Method: http.MethodPost, URI: "/api/v1/service-accounts", Body: map[string]any{"name": "ci-bot", "description": "CI pipelines"}},
		{Method: http.MethodPost, URI: "/api/v1/permission-targets", Body: map[string]any{
			"name": "ci", "repository_filter": "ci-.*", "path_pattern": "*", "actions": []any{"write"},
		}},
		{
			Method: http.MethodPost,
			URI:    "/api/v1/permission-targets/t-ci/assignments",
			Body:   map[string]any{"principal_type": "user", "principal_id": "sa-ci"},
		},
		{Method: http.MethodPost, URI: "/api/v1/oidc-trust-policies", Body: map[string]any{
			"name": "gh", "issuer": githubIssuer, "audience": "https://oa.example.org", "service_account_id": "sa-ci",
			"conditions":        []any{oidcCondition("repository", "acme/*"), oidcCondition("ref", "refs/heads/main", "refs/tags/v*")},
			"token_ttl_seconds": float64(600), "enabled": true,
		}},
	}

	identAssertCalls(t, api, wantAPI)

	wantMarkers := []string{"ServiceAccount/ci-bot=sa-ci", "PermissionTarget/ci=t-ci", "OIDCTrustPolicy/gh=p-gh"}
	if !reflect.DeepEqual(markers, wantMarkers) {
		t.Errorf("ownership markers = %v, want %v", markers, wantMarkers)
	}
}

func TestExportKeepsServiceAccountsAndTheirReferences(t *testing.T) {
	t.Parallel()

	empty := identJSON(http.StatusOK, map[string]any{"data": []map[string]any{}})
	routes := withServiceAccounts(identUsersRoutes(map[string]any{"id": "u-alice", "username": "alice"}),
		map[string]any{"id": "sa-ci", "name": "ci-bot", "description": "CI pipelines"},
	)
	routes["GET /api/v1/groups"] = empty
	routes["GET /api/v1/projects"] = identJSON(http.StatusOK, map[string]any{"projects": []map[string]any{}})
	routes["GET /api/v1/permission-targets"] = identJSON(http.StatusOK, map[string]any{"data": []map[string]any{
		{"id": "t-ci", "name": "ci", "repository_filter": "ci-.*", "path_pattern": "*", "actions": []string{"write"}},
	}})
	routes["GET /api/v1/permission-targets/t-ci/assignments"] = identJSON(
		http.StatusOK,
		identAssignments([3]string{"user", "sa-ci", "ci-bot"}),
	)
	routes["GET /api/v1/oidc-trust-policies"] = identJSON(http.StatusOK, map[string]any{"data": []map[string]any{{
		"id": "p-gh", "name": "gh", "issuer": githubIssuer, "audience": "https://oa.example.org",
		"conditions":         []map[string]any{{"claim": "repository", "patterns": []string{"acme/*"}}},
		"service_account_id": "sa-ci", "service_account": "ci-bot", "token_ttl_seconds": 900, "enabled": true,
	}}})

	env, _ := newIdentEnv(t, routes)

	docs, err := Export(context.Background(), ExportOptions{
		Registry: NewRegistry(NewServiceAccountKind(), NewPermissionTargetKind(), NewOIDCTrustPolicyKind()), Client: env.Client,
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	var got []string
	for _, d := range docs {
		got = append(got, d.Kind+"/"+d.Key)
	}

	if want := []string{"ServiceAccount/ci-bot", "PermissionTarget/ci", "OIDCTrustPolicy/gh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("exported = %v, want %v", got, want)
	}

	// The exported manifests are self-contained: applying them to the same
	// server changes nothing.
	var all strings.Builder
	for _, d := range docs {
		all.WriteString("---\n" + string(d.YAML))
	}

	reg := NewRegistry(NewServiceAccountKind(), NewPermissionTargetKind(), NewOIDCTrustPolicyKind())

	objs, err := loadYAML(t, reg, all.String())
	if err != nil {
		t.Fatalf("exported manifests do not load: %v\n%s", err, all.String())
	}

	live := map[string][]*Live{}

	for _, k := range reg.Ordered() {
		list, err := k.List(context.Background(), env)
		if err != nil {
			t.Fatalf("List %s: %v", k.Name(), err)
		}

		live[k.Name()] = list
	}

	live[KindUser] = []*Live{{Kind: KindUser, Key: "alice", ID: "u-alice"}}

	if plan := planFor(reg, PlanInput{Desired: objs, Live: live}); plan.HasChanges() || len(plan.Errors()) != 0 {
		t.Errorf("re-applying the export is not a no-op: %v %v", planSummary(plan), plan.Errors())
	}
}
