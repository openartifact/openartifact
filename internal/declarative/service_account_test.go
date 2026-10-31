// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func identServiceAccountRoutes(accounts ...map[string]any) map[string]identRoute {
	return map[string]identRoute{
		"GET /api/v1/service-accounts": identJSON(http.StatusOK, map[string]any{"data": accounts, "total": len(accounts)}),
	}
}

// manifestOf renders one manifest document; spec is the already indented body.
func manifestOf(kind, name, spec string) string {
	return "apiVersion: openartifact.io/v1\nkind: " + kind + "\nmetadata:\n  name: " + name + "\nspec:\n" + spec
}

// planSummary renders every plan item as "action key"; an update adds the
// changed paths.
func planSummary(plan *Plan) []string {
	out := make([]string, len(plan.Items))

	for i, it := range plan.Items {
		out[i] = string(it.Action) + " " + it.Key

		if it.Action != ActionUpdate {
			continue
		}

		paths := make([]string, len(it.Changes))
		for j, c := range it.Changes {
			paths[j] = c.Path
		}

		out[i] += " [" + strings.Join(paths, ",") + "]"
	}

	return out
}

func TestServiceAccountKindList(t *testing.T) {
	t.Parallel()

	env, _ := newIdentEnv(t, identServiceAccountRoutes(
		map[string]any{
			"id": "sa-ci", "name": "ci-bot", "description": "CI pipelines", "disabled": false,
			"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
		},
		map[string]any{"id": "sa-old", "name": "old-bot", "disabled": true},
	))

	got, err := NewServiceAccountKind().List(context.Background(), env)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := []*Live{
		{Kind: KindServiceAccount, Key: "ci-bot", ID: "sa-ci", Spec: map[string]any{"description": "CI pipelines", "disabled": false}},
		{Kind: KindServiceAccount, Key: "old-bot", ID: "sa-old", Spec: map[string]any{"description": "", "disabled": true}},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %+v, want %+v", got, want)
	}
}

func TestServiceAccountKindListPagesThroughEveryAccount(t *testing.T) {
	t.Parallel()

	const total = listPageSize + 1

	env, _ := newIdentEnv(t, map[string]identRoute{
		"GET /api/v1/service-accounts": func(r *http.Request) (int, any) {
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))

			var data []map[string]any

			for i := (page - 1) * listPageSize; i < min(page*listPageSize, total); i++ {
				data = append(data, map[string]any{"id": "id-" + strconv.Itoa(i), "name": "bot" + strconv.Itoa(i)})
			}

			return http.StatusOK, map[string]any{"data": data, "total": total}
		},
	})

	got, err := NewServiceAccountKind().List(context.Background(), env)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(got) != total {
		t.Errorf("List returned %d service accounts, want %d", len(got), total)
	}
}

func TestServiceAccountKindListErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		routes    map[string]identRoute
		wantCount int
		wantErr   bool
	}{
		{
			name:      "a server without service accounts has none",
			routes:    map[string]identRoute{},
			wantCount: 0,
		},
		{
			name: "other failures are reported",
			routes: map[string]identRoute{
				"GET /api/v1/service-accounts": identJSON(http.StatusInternalServerError, map[string]any{"error": "boom"}),
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env, _ := newIdentEnv(t, tc.routes)

			got, err := NewServiceAccountKind().List(context.Background(), env)
			if (err != nil) != tc.wantErr {
				t.Fatalf("List error = %v, wantErr %v", err, tc.wantErr)
			}

			if tc.wantErr && (!IsStatus(err, http.StatusInternalServerError) || !strings.Contains(err.Error(), "list service accounts")) {
				t.Errorf("List error = %v, want the 500 named as the service account listing", err)
			}

			if len(got) != tc.wantCount {
				t.Errorf("List returned %d accounts, want %d", len(got), tc.wantCount)
			}
		})
	}
}

func TestServiceAccountKindCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec map[string]any
		want map[string]any
	}{
		{
			name: "sends the name, description and disabled flag",
			spec: map[string]any{"description": "CI pipelines", "disabled": true},
			want: map[string]any{"name": "ci-bot", "description": "CI pipelines", "disabled": true},
		},
		{
			name: "creates from the name alone",
			spec: map[string]any{},
			want: map[string]any{"name": "ci-bot"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env, fake := newIdentEnv(t, map[string]identRoute{
				"POST /api/v1/service-accounts": identJSON(http.StatusCreated, map[string]any{"id": "sa-ci", "name": "ci-bot"}),
			})
			env.Cache[identsCacheKey] = &Idents{}

			id, err := NewServiceAccountKind().Create(context.Background(), env, &Object{Kind: KindServiceAccount, Name: "ci-bot", Spec: tc.spec})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			if id != "sa-ci" {
				t.Errorf("id = %q, want sa-ci", id)
			}

			identAssertCalls(t, fake.writes(), []identCall{{Method: http.MethodPost, URI: "/api/v1/service-accounts", Body: tc.want}})

			if _, cached := env.Cache[identsCacheKey]; cached {
				t.Error("Create did not drop the cached principal lookup")
			}
		})
	}
}

func TestServiceAccountKindUpdateSendsOnlyChangedFields(t *testing.T) {
	t.Parallel()

	env, fake := newIdentEnv(t, map[string]identRoute{
		"PATCH /api/v1/service-accounts/sa-ci": identJSON(http.StatusOK, map[string]any{}),
	})

	live := &Live{Kind: KindServiceAccount, Key: "ci-bot", ID: "sa-ci"}
	changes := []Change{
		{Path: "description", Old: "CI", New: "CI pipelines"},
		{Path: "disabled", Old: false, New: true},
	}

	id, err := NewServiceAccountKind().Update(context.Background(), env, live, &Object{Name: "ci-bot"}, changes)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if id != "sa-ci" {
		t.Errorf("id = %q, want sa-ci", id)
	}

	identAssertCalls(t, fake.writes(), []identCall{{
		Method: http.MethodPatch, URI: "/api/v1/service-accounts/sa-ci",
		Body: map[string]any{"description": "CI pipelines", "disabled": true},
	}})
}

func TestServiceAccountKindDelete(t *testing.T) {
	t.Parallel()

	env, fake := newIdentEnv(t, map[string]identRoute{
		"DELETE /api/v1/service-accounts/sa-ci": identJSON(http.StatusNoContent, nil),
	})
	env.Cache[identsCacheKey] = &Idents{}

	if err := NewServiceAccountKind().Delete(context.Background(), env, &Live{Key: "ci-bot", ID: "sa-ci"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	identAssertCalls(t, fake.writes(), []identCall{{Method: http.MethodDelete, URI: "/api/v1/service-accounts/sa-ci"}})

	if _, cached := env.Cache[identsCacheKey]; cached {
		t.Error("Delete did not drop the cached principal lookup")
	}
}

func TestServiceAccountKindErrorsKeepAPIError(t *testing.T) {
	t.Parallel()

	k := NewServiceAccountKind()
	ctx := context.Background()

	tests := []struct {
		name   string
		status int
		call   func(env *Env) error
	}{
		{"create", http.StatusConflict, func(env *Env) error {
			_, err := k.Create(ctx, env, &Object{Name: "ci-bot", Spec: map[string]any{}})
			return err
		}},
		{"update", http.StatusBadRequest, func(env *Env) error {
			_, err := k.Update(
				ctx,
				env,
				&Live{Key: "ci-bot", ID: "sa-ci"},
				&Object{Name: "ci-bot"},
				[]Change{{Path: "disabled", New: true}},
			)
			return err
		}},
		{"delete", http.StatusInternalServerError, func(env *Env) error {
			return k.Delete(ctx, env, &Live{Key: "ci-bot", ID: "sa-ci"})
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env, _ := newIdentEnv(t, map[string]identRoute{
				"POST /api/v1/service-accounts":         identJSON(tc.status, map[string]any{"error": "rejected"}),
				"PATCH /api/v1/service-accounts/sa-ci":  identJSON(tc.status, map[string]any{"error": "rejected"}),
				"DELETE /api/v1/service-accounts/sa-ci": identJSON(tc.status, map[string]any{"error": "rejected"}),
			})

			if err := tc.call(env); !IsStatus(err, tc.status) || !strings.Contains(err.Error(), "service account") {
				t.Errorf("%s error = %v, want an *APIError with status %d naming the service account", tc.name, err, tc.status)
			}
		})
	}
}

func TestServiceAccountKindValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		account     string
		description string
		wantErr     string
	}{
		{name: "letters, digits, dash and underscore", account: "ci-bot_01"},
		{name: "shortest name", account: "abc"},
		{name: "longest name", account: strings.Repeat("a", 64)},
		{name: "name too short", account: "ab", wantErr: "metadata.name"},
		{name: "name too long", account: strings.Repeat("a", 65), wantErr: "metadata.name"},
		{name: "name with a dot", account: "ci.bot", wantErr: "metadata.name"},
		{name: "name with a space", account: "ci bot", wantErr: "metadata.name"},
		{name: "longest description counts characters", account: "ci-bot", description: strings.Repeat("é", 255)},
		{name: "description too long", account: "ci-bot", description: strings.Repeat("a", 256), wantErr: "description"},
		{name: "description with a newline", account: "ci-bot", description: "a\nb", wantErr: "description"},
		{name: "description with a delete character", account: "ci-bot", description: "a\x7fb", wantErr: "description"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			v, ok := NewServiceAccountKind().(Validator)
			if !ok {
				t.Fatal("ServiceAccount must implement Validator")
			}

			spec := map[string]any{}
			if tc.description != "" {
				spec["description"] = tc.description
			}

			errs := v.Validate(&Object{Kind: KindServiceAccount, Name: tc.account, Spec: spec})

			switch {
			case tc.wantErr == "" && len(errs) != 0:
				t.Errorf("Validate = %v, want none", errs)
			case tc.wantErr != "" && (len(errs) != 1 || !strings.Contains(errs[0].Error(), tc.wantErr)):
				t.Errorf("Validate = %v, want one error about %q", errs, tc.wantErr)
			}
		})
	}
}

func TestServiceAccountKindMetadata(t *testing.T) {
	t.Parallel()

	k := NewServiceAccountKind()

	if k.Name() != "ServiceAccount" {
		t.Errorf("Name = %q, want ServiceAccount", k.Name())
	}

	fields := k.Schema().Fields
	if len(fields) != 2 || fields["description"] == nil || fields["disabled"] == nil {
		t.Errorf("schema fields = %v, want description and disabled only", fields)
	}

	if refs := k.Refs(&Object{Name: "ci-bot", Spec: map[string]any{}}); len(refs) != 0 {
		t.Errorf("Refs = %+v, want none", refs)
	}

	reg := DefaultRegistry()
	rank := func(name string) int {
		kind, ok := reg.Get(name)
		if !ok {
			t.Fatalf("DefaultRegistry has no %s kind", name)
		}

		return kind.Rank()
	}

	// Creates run in rank order and deletes in reverse: an account exists before
	// anything that names it and goes after it.
	for _, referrer := range []string{KindGroup, KindProject, KindPermissionTarget, KindOIDCTrustPolicy} {
		if rank(KindUser) >= rank(KindServiceAccount) || rank(KindServiceAccount) >= rank(referrer) {
			t.Errorf("rank of ServiceAccount (%d) must sit between User (%d) and %s (%d)",
				rank(KindServiceAccount), rank(KindUser), referrer, rank(referrer))
		}
	}
}

func TestPlanServiceAccounts(t *testing.T) {
	t.Parallel()

	const spec = "  description: CI pipelines\n"

	live := func(description string, disabled bool) []*Live {
		return []*Live{{
			Kind: KindServiceAccount, Key: "ci-bot", ID: "sa-ci",
			Spec: map[string]any{"description": description, "disabled": disabled},
		}}
	}

	tests := []struct {
		name     string
		manifest string
		live     []*Live
		managed  bool
		prune    bool
		want     []string
	}{
		{name: "create", manifest: manifestOf(KindServiceAccount, "ci-bot", spec), want: []string{"create ci-bot"}},
		{
			name: "unchanged", manifest: manifestOf(KindServiceAccount, "ci-bot", spec),
			live: live("CI pipelines", false), want: []string{"unchanged ci-bot"},
		},
		{
			name: "omitted fields are left alone", manifest: manifestOf(KindServiceAccount, "ci-bot", spec),
			live: live("CI pipelines", true), want: []string{"unchanged ci-bot"},
		},
		{
			name: "update the description", manifest: manifestOf(KindServiceAccount, "ci-bot", spec),
			live: live("old", false), want: []string{"update ci-bot [description]"},
		},
		{
			name: "disable", manifest: manifestOf(KindServiceAccount, "ci-bot", spec+"  disabled: true\n"),
			live: live("CI pipelines", false), want: []string{"update ci-bot [disabled]"},
		},
		{
			name:    "prune deletes a managed account",
			live:    live("CI pipelines", false),
			managed: true,
			prune:   true,
			want:    []string{"delete ci-bot"},
		},
		{name: "prune deletes a disabled account too", live: live("", true), managed: true, prune: true, want: []string{"delete ci-bot"}},
		{name: "prune leaves an unmanaged account", live: live("CI pipelines", false), prune: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reg := NewRegistry(NewServiceAccountKind())
			in := PlanInput{Live: map[string][]*Live{KindServiceAccount: tc.live}, Prune: tc.prune}

			if tc.managed {
				in.Markers = Markers{Ident(KindServiceAccount, "ci-bot"): {Kind: KindServiceAccount, Name: "ci-bot", ObjectID: "sa-ci"}}
			}

			if tc.manifest != "" {
				objs, err := loadYAML(t, reg, tc.manifest)
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

func TestLoadServiceAccountRejectsInvalidManifests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		manifest string
		wantErr  string
	}{
		{"short name", manifestOf(KindServiceAccount, "ab", "  description: x\n"), "metadata.name"},
		{"unknown field", manifestOf(KindServiceAccount, "ci-bot", "  email: ci@example.org\n"), "email"},
		{"disabled must be a boolean", manifestOf(KindServiceAccount, "ci-bot", "  disabled: sure\n"), "disabled"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := loadYAML(t, NewRegistry(NewServiceAccountKind()), tc.manifest)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("load error = %v, want one mentioning %q", err, tc.wantErr)
			}
		})
	}
}
