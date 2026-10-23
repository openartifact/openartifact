// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

// ssoTierReq is one request the fake REST API received.
type ssoTierReq struct {
	Method string
	Path   string
	Body   map[string]any
}

// ssoTierFake is a recording fake REST API; answer decides every response.
type ssoTierFake struct {
	mu     sync.Mutex
	reqs   []ssoTierReq
	answer func(ssoTierReq) (int, any)
}

func newSSOTierFake(t *testing.T, answer func(ssoTierReq) (int, any)) (*Env, *ssoTierFake) {
	t.Helper()

	fake := &ssoTierFake{answer: answer}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := ssoTierReq{Method: r.Method, Path: r.URL.EscapedPath()}

		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&req.Body); err != nil {
				t.Errorf("decode request body of %s %s: %v", r.Method, r.URL.Path, err)
			}
		}

		fake.mu.Lock()
		fake.reqs = append(fake.reqs, req)
		fake.mu.Unlock()

		status, resp := answer(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)

		if resp != nil {
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	t.Cleanup(srv.Close)

	return NewEnv(NewClient(srv.URL, "tok", srv.Client())), fake
}

func (f *ssoTierFake) requests() []ssoTierReq {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]ssoTierReq(nil), f.reqs...)
}

func okAnswer(status int, resp any) func(ssoTierReq) (int, any) {
	return func(ssoTierReq) (int, any) { return status, resp }
}

func ssoOIDCAPI() map[string]any {
	return map[string]any{
		"id": "id-oidc", "slug": "corp", "kind": "oidc", "display_name": "Corp", "enabled": true,
		"oidc_config": map[string]any{
			"issuer": "https://idp.example.com", "client_id": "cid", "scopes": nil,
			"username_claim": "", "email_claim": "email", "name_claim": "", "groups_claim": "groups",
			"allowed_tenants": []any{"t1"}, "has_secret": true,
		},
		"group_mapping":        map[string]any{"eng": "developers"},
		"auto_provision_users": true,
	}
}

func TestSSOListNormalizesProviders(t *testing.T) {
	t.Parallel()

	saml := map[string]any{
		"id": "id-saml", "slug": "okta", "kind": "saml", "display_name": "Okta", "enabled": false,
		"saml_config": map[string]any{"idp_metadata_url": "https://okta.example.com/meta", "groups_attribute": "g"},
		"claim_mapping": map[string]any{
			"username_claim": "upn",
		},
	}

	env, fake := newSSOTierFake(t, okAnswer(http.StatusOK, map[string]any{"providers": []any{ssoOIDCAPI(), saml}}))

	lives, err := NewSSOProviderKind().List(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}

	if got := fake.requests(); len(got) != 1 || got[0].Method != http.MethodGet || got[0].Path != ssoPath {
		t.Fatalf("requests = %+v", got)
	}

	if len(lives) != 2 {
		t.Fatalf("got %d providers", len(lives))
	}

	oidc := lives[0]
	if oidc.Key != "corp" || oidc.ID != "id-oidc" || oidc.Seeded {
		t.Errorf("oidc identity = %+v", oidc)
	}

	if v, ok := oidc.Secrets["oidc_config.client_secret"]; !ok || !v {
		t.Errorf("secrets = %v, want client_secret present", oidc.Secrets)
	}

	cfg, _ := oidc.Spec["oidc_config"].(map[string]any)
	if _, leaked := cfg["client_secret"]; leaked {
		t.Error("client_secret must not appear in Live.Spec")
	}

	if _, leaked := cfg["has_secret"]; leaked {
		t.Error("has_secret must not appear in Live.Spec")
	}

	want := map[string]any{
		"issuer": "https://idp.example.com", "client_id": "cid", "scopes": nil, "email_claim": "email",
		"groups_claim": "groups", "allowed_tenants": []any{"t1"}, "type": "", "allowed_orgs": []any{},
		"graph_endpoint": "", "require_email_verified": false,
	}

	for k, v := range want {
		if !reflect.DeepEqual(cfg[k], v) {
			t.Errorf("oidc_config.%s = %#v, want %#v", k, cfg[k], v)
		}
	}

	if len(cfg) != len(ssoOIDCFields())-1 {
		t.Errorf("oidc_config has %d keys, want every non-secret schema field (%d)", len(cfg), len(ssoOIDCFields())-1)
	}

	if _, ok := oidc.Spec["saml_config"]; ok {
		t.Error("oidc provider must not carry saml_config")
	}

	if !reflect.DeepEqual(oidc.Spec["group_mapping"], map[string]any{"eng": "developers"}) {
		t.Errorf("group_mapping = %#v", oidc.Spec["group_mapping"])
	}

	if !reflect.DeepEqual(oidc.Spec["permission_mapping"], map[string]any{}) {
		t.Errorf("empty permission_mapping must normalize to {}, got %#v", oidc.Spec["permission_mapping"])
	}

	ssoCheckSAMLLive(t, lives[1])
}

func ssoCheckSAMLLive(t *testing.T, l *Live) {
	t.Helper()

	if l.Key != "okta" || l.Spec["enabled"] != false {
		t.Errorf("saml live = %+v", l)
	}

	if len(l.Secrets) != 0 {
		t.Errorf("saml provider has no secrets, got %v", l.Secrets)
	}

	cfg, _ := l.Spec["saml_config"].(map[string]any)
	if cfg["idp_metadata_url"] != "https://okta.example.com/meta" || cfg["groups_attribute"] != "g" ||
		cfg["idp_metadata_refresh_seconds"] != float64(0) || cfg["allow_idp_initiated"] != false {
		t.Errorf("saml_config = %#v", cfg)
	}

	if !reflect.DeepEqual(l.Spec["claim_mapping"], map[string]any{"username_claim": "upn"}) {
		t.Errorf("claim_mapping = %#v", l.Spec["claim_mapping"])
	}
}

func TestSSOCreate(t *testing.T) {
	t.Parallel()

	oidcSpec := func(enabled any) map[string]any {
		spec := map[string]any{
			"kind": "oidc", "display_name": "Corp",
			"oidc_config":   map[string]any{"issuer": "https://idp", "client_id": "cid", "client_secret": "s3cret"},
			"group_mapping": map[string]any{"eng": "dev"}, "link_by_email": true,
		}
		if enabled != nil {
			spec["enabled"] = enabled
		}

		return spec
	}

	tests := []struct {
		name     string
		spec     map[string]any
		wantReqs []ssoTierReq
	}{
		{
			name: "enabled by default needs a single POST",
			spec: oidcSpec(nil),
			wantReqs: []ssoTierReq{{Method: http.MethodPost, Path: ssoPath, Body: map[string]any{
				"slug": "corp", "kind": "oidc", "display_name": "Corp",
				"oidc_config":   map[string]any{"issuer": "https://idp", "client_id": "cid", "client_secret": "s3cret"},
				"group_mapping": map[string]any{"eng": "dev"}, "link_by_email": true,
			}}},
		},
		{
			name: "enabled true is not sent",
			spec: oidcSpec(true),
			wantReqs: []ssoTierReq{{Method: http.MethodPost, Path: ssoPath, Body: map[string]any{
				"slug": "corp", "kind": "oidc", "display_name": "Corp",
				"oidc_config":   map[string]any{"issuer": "https://idp", "client_id": "cid", "client_secret": "s3cret"},
				"group_mapping": map[string]any{"eng": "dev"}, "link_by_email": true,
			}}},
		},
		{
			name: "enabled false is patched after create",
			spec: oidcSpec(false),
			wantReqs: []ssoTierReq{
				{Method: http.MethodPost, Path: ssoPath, Body: map[string]any{
					"slug": "corp", "kind": "oidc", "display_name": "Corp",
					"oidc_config":   map[string]any{"issuer": "https://idp", "client_id": "cid", "client_secret": "s3cret"},
					"group_mapping": map[string]any{"eng": "dev"}, "link_by_email": true,
				}},
				{Method: http.MethodPatch, Path: ssoPath + "/corp", Body: map[string]any{"enabled": false}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env, fake := newSSOTierFake(t, okAnswer(http.StatusCreated, map[string]any{"id": "new-id"}))

			id, err := NewSSOProviderKind().Create(context.Background(), env, &Object{Kind: KindSSOProvider, Name: "corp", Spec: tc.spec})
			if err != nil {
				t.Fatal(err)
			}

			if id != "new-id" {
				t.Errorf("id = %q", id)
			}

			if got := fake.requests(); !reflect.DeepEqual(got, tc.wantReqs) {
				t.Errorf("requests = %+v\nwant %+v", got, tc.wantReqs)
			}
		})
	}
}

func TestSSOUpdateSendsOnlyChanges(t *testing.T) {
	t.Parallel()

	env, fake := newSSOTierFake(t, okAnswer(http.StatusOK, map[string]any{}))
	live := &Live{Kind: KindSSOProvider, Key: "corp", ID: "id-oidc"}
	changes := []Change{
		{Path: "display_name", Old: "Corp", New: "Corporate"},
		{Path: "oidc_config.issuer", Old: "https://a", New: "https://b"},
		{Path: "oidc_config.client_secret", Secret: true, New: "rotated"},
		{Path: "group_mapping", Old: map[string]any{"a": "b"}, New: map[string]any{"c": "d"}},
	}

	id, err := NewSSOProviderKind().Update(context.Background(), env, live, &Object{}, changes)
	if err != nil {
		t.Fatal(err)
	}

	if id != "id-oidc" {
		t.Errorf("id = %q", id)
	}

	want := []ssoTierReq{{Method: http.MethodPatch, Path: ssoPath + "/corp", Body: map[string]any{
		"display_name": "Corporate",
		"oidc_config":  map[string]any{"issuer": "https://b", "client_secret": "rotated"},
		"group_mapping": map[string]any{
			"c": "d",
		},
	}}}

	if got := fake.requests(); !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %+v\nwant %+v", got, want)
	}
}

func TestSSODelete(t *testing.T) {
	t.Parallel()

	env, fake := newSSOTierFake(t, okAnswer(http.StatusNoContent, nil))

	if err := NewSSOProviderKind().Delete(context.Background(), env, &Live{Key: "corp"}); err != nil {
		t.Fatal(err)
	}

	want := []ssoTierReq{{Method: http.MethodDelete, Path: ssoPath + "/corp"}}
	if got := fake.requests(); !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %+v, want %+v", got, want)
	}
}

func TestSSOUpdateErrorKeepsAPIError(t *testing.T) {
	t.Parallel()

	env, _ := newSSOTierFake(t, okAnswer(http.StatusBadRequest, map[string]any{"message": "nope"}))

	_, err := NewSSOProviderKind().Update(context.Background(), env, &Live{Key: "corp"}, &Object{},
		[]Change{{Path: "display_name", New: "x"}})
	if !IsStatus(err, http.StatusBadRequest) {
		t.Errorf("err = %v, want reachable *APIError 400", err)
	}
}

func TestSSOValidate(t *testing.T) {
	t.Parallel()

	secret := SecretRef{Env: "OIDC_SECRET"}
	oidcCfg := func(extra map[string]any) map[string]any {
		cfg := map[string]any{"issuer": "https://idp", "client_id": "cid", "client_secret": secret}
		maps.Copy(cfg, extra)

		return cfg
	}

	tests := []struct {
		name    string
		slug    string
		spec    map[string]any
		wantErr bool
	}{
		{"valid oidc", "corp", map[string]any{"kind": "oidc", "oidc_config": oidcCfg(nil)}, false},
		{"valid saml", "okta", map[string]any{"kind": "saml", "saml_config": map[string]any{"idp_metadata_url": "https://x"}}, false},
		{"valid saml xml", "okta", map[string]any{"kind": "saml", "saml_config": map[string]any{"idp_metadata_xml": "<x/>"}}, false},
		{"bad slug", "Corp!", map[string]any{"kind": "oidc", "oidc_config": oidcCfg(nil)}, true},
		{"oidc without config", "corp", map[string]any{"kind": "oidc"}, true},
		{"oidc without secret", "corp", map[string]any{"kind": "oidc", "oidc_config": map[string]any{"issuer": "https://idp", "client_id": "c"}}, true},
		{"oidc without issuer", "corp", map[string]any{"kind": "oidc", "oidc_config": map[string]any{"client_id": "c", "client_secret": secret}}, true},
		{"oidc with saml block", "corp", map[string]any{"kind": "oidc", "oidc_config": oidcCfg(nil), "saml_config": map[string]any{}}, true},
		{"github without orgs", "gh", map[string]any{"kind": "oidc", "oidc_config": map[string]any{"type": "github", "client_id": "c", "client_secret": secret}}, true},
		{"github with orgs", "gh", map[string]any{"kind": "oidc", "oidc_config": map[string]any{"type": "github", "client_id": "c", "client_secret": secret, "allowed_orgs": []any{"acme"}}}, false},
		{"oauth2 without urls", "o2", map[string]any{"kind": "oidc", "oidc_config": map[string]any{"type": "oauth2", "client_id": "c", "client_secret": secret}}, true},
		{"saml without metadata", "okta", map[string]any{"kind": "saml", "saml_config": map[string]any{}}, true},
		{"saml without config", "okta", map[string]any{"kind": "saml"}, true},
		{"saml with oidc block", "okta", map[string]any{"kind": "saml", "saml_config": map[string]any{"idp_metadata_url": "https://x"}, "oidc_config": map[string]any{}}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			v, ok := NewSSOProviderKind().(Validator)
			if !ok {
				t.Fatal("SSOProvider kind must implement Validator")
			}

			errs := v.Validate(&Object{Kind: KindSSOProvider, Name: tc.slug, Spec: tc.spec})
			if (len(errs) > 0) != tc.wantErr {
				t.Errorf("errors = %v, wantErr %v", errs, tc.wantErr)
			}
		})
	}
}

func TestSSOSchemaCoversLiveSpec(t *testing.T) {
	t.Parallel()

	schema := NewSSOProviderKind().Schema()

	m, _ := Canon(ssoOIDCAPI()).(map[string]any)

	for k := range ssoLive(m).Spec {
		if _, ok := schema.Fields[k]; !ok {
			t.Errorf("Live.Spec key %q is not declared in the schema", k)
		}
	}

	if !schema.Fields["oidc_config"].Fields["client_secret"].Secret {
		t.Error("oidc_config.client_secret must be a Secret field")
	}

	if !schema.Fields["kind"].Immutable {
		t.Error("kind must be immutable")
	}
}
