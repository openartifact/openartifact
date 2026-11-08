// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

const dexTestIssuer = "https://dex.example.com/dex"

func dexRow(t *testing.T, slug string, cfg models.OIDCConfig) *models.SSOProvider {
	t.Helper()

	cfg.Issuer, cfg.ClientID, cfg.ClientSecret = dexTestIssuer, "cid", "secret"

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	return &models.SSOProvider{ID: "id-" + slug, Slug: slug, Kind: "oidc", Enabled: true, OIDCConfig: raw, UpdatedAt: time.Now()}
}

func TestDexProvider_AuthURLAndScopes(t *testing.T) {
	newPolicyIdP(t, dexTestIssuer)

	tests := []struct {
		name       string
		cfg        models.OIDCConfig
		wantHint   string
		wantScopes []string
	}{
		{"plain provider has no connector hint", models.OIDCConfig{}, "", []string{"openid", "email", "profile"}},
		{
			"connector id defaults to the Dex scopes",
			models.OIDCConfig{DexConnectorID: "ldap"},
			"ldap",
			[]string{"openid", "email", "profile", "groups", "federated:id"},
		},
		{
			"explicit scopes are kept",
			models.OIDCConfig{DexConnectorID: "gitlab", Scopes: []string{"openid", "federated:id"}},
			"gitlab",
			[]string{"openid", "federated:id"},
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := LoadOIDCProviderRow(context.Background(), dexRow(t, "dex-url-"+string(rune('a'+i)), tc.cfg), "http://localhost")
			if err != nil {
				t.Fatalf("load: %v", err)
			}

			if !slices.Equal(p.Config.Scopes, tc.wantScopes) {
				t.Errorf("scopes = %v, want %v", p.Config.Scopes, tc.wantScopes)
			}

			u, err := url.Parse(p.AuthURL("st", "nn", "ch"))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			q := u.Query()
			if got := q.Get("connector_id"); got != tc.wantHint {
				t.Errorf("connector_id = %q, want %q", got, tc.wantHint)
			}

			if _, present := q["connector_id"]; present != (tc.wantHint != "") {
				t.Errorf("connector_id presence = %v", present)
			}

			if q.Get("scope") != strings.Join(tc.wantScopes, " ") {
				t.Errorf("scope param = %q", q.Get("scope"))
			}
		})
	}
}

func TestDexProvider_ConnectorCheck(t *testing.T) {
	tests := []struct {
		name   string
		cfg    models.OIDCConfig
		claims map[string]any
		deny   bool
	}{
		{
			"matching connector",
			models.OIDCConfig{DexConnectorID: "ldap"},
			map[string]any{"federated_claims": map[string]any{"connector_id": "ldap", "user_id": "alice"}},
			false,
		},
		{
			"another connector of the same Dex",
			models.OIDCConfig{DexConnectorID: "ldap"},
			map[string]any{"federated_claims": map[string]any{"connector_id": "local", "user_id": "alice"}},
			true,
		},
		{"no federated_claims", models.OIDCConfig{DexConnectorID: "ldap"}, map[string]any{"email": "a@example.com"}, true},
		{"federated_claims is not an object", models.OIDCConfig{DexConnectorID: "ldap"}, map[string]any{"federated_claims": "ldap"}, true},
		{
			"connector_id is not a string",
			models.OIDCConfig{DexConnectorID: "ldap"},
			map[string]any{"federated_claims": map[string]any{"connector_id": 1}},
			true,
		},
		{
			"connector ids compare exactly",
			models.OIDCConfig{DexConnectorID: "ldap"},
			map[string]any{"federated_claims": map[string]any{"connector_id": "LDAP"}},
			true,
		},
		{
			"provider without a pin ignores the claim",
			models.OIDCConfig{},
			map[string]any{"federated_claims": map[string]any{"connector_id": "local"}},
			false,
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idp := newPolicyIdP(t, dexTestIssuer)
			tc.cfg.Issuer = dexTestIssuer
			idp.sign(t, tc.claims)

			claims, err := loginIdentity(t, "dex-check-"+string(rune('a'+i)), tc.cfg)
			if tc.deny {
				if !errors.Is(err, ErrSSOLoginDenied) {
					t.Fatalf("expected ErrSSOLoginDenied, got %v", err)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if claims.ExternalID != "subject" {
				t.Errorf("claims = %+v", claims)
			}
		})
	}
}

func TestValidDexConnectorID(t *testing.T) {
	for id, want := range map[string]bool{
		"ldap": true, "my-gitlab_2": true, "A": true, strings.Repeat("a", 64): true,
		"": false, strings.Repeat("a", 65): false, "has space": false, "a/b": false, "a.b": false, "x\n": false, "é": false,
	} {
		if got := ValidDexConnectorID(id); got != want {
			t.Errorf("ValidDexConnectorID(%q) = %v, want %v", id, got, want)
		}
	}
}
