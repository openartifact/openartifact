// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Public IP literals keep the SSRF validation free of DNS lookups.
const (
	testIssuer  = "https://8.8.8.8/realm"
	testAuthURL = "https://8.8.4.4/authorize"
)

func TestValidateOIDCConfig(t *testing.T) {
	const tenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

	base := func(mut func(*models.OIDCConfig)) models.OIDCConfig {
		c := models.OIDCConfig{ClientID: "id", ClientSecret: "secret"}
		mut(&c)

		return c
	}

	tests := []struct {
		name    string
		cfg     models.OIDCConfig
		wantErr string // substring; empty means valid
	}{
		{"plain oidc", base(func(c *models.OIDCConfig) { c.Issuer = testIssuer }), ""},
		{"oidc needs issuer", base(func(*models.OIDCConfig) {}), "requires issuer"},
		{"unknown type", base(func(c *models.OIDCConfig) { c.Type = "saml" }), "type"},
		{"issuer must be https", base(func(c *models.OIDCConfig) { c.Issuer = "http://8.8.8.8/realm" }), "issuer"},
		{"issuer must not be private", base(func(c *models.OIDCConfig) { c.Issuer = "https://10.0.0.1/realm" }), "issuer"},
		{"scopes may not contain whitespace", base(func(c *models.OIDCConfig) { c.Issuer = testIssuer; c.Scopes = []string{"openid email"} }), "scopes"},
		{"graph endpoint is SSRF validated", base(func(c *models.OIDCConfig) { c.Issuer = testIssuer; c.GraphEndpoint = "https://169.254.169.254" }), "graph_endpoint"},
		{"allowed tenants must be guids", base(func(c *models.OIDCConfig) { c.Issuer = testIssuer; c.AllowedTenants = []string{"contoso"} }), "allowed_tenants"},
		{"guid tenants are fine", base(func(c *models.OIDCConfig) { c.Issuer = testIssuer; c.AllowedTenants = []string{tenant} }), ""},

		{"github with orgs", base(func(c *models.OIDCConfig) { c.Type = "github"; c.AllowedOrgs = []string{"acme"} }), ""},
		{"github does not need an issuer but needs orgs", base(func(c *models.OIDCConfig) { c.Type = "github" }), "allowed_orgs"},
		{"github org entries are logins", base(func(c *models.OIDCConfig) { c.Type = "github"; c.AllowedOrgs = []string{"acme/team"} }), "allowed_orgs"},
		{"github enterprise urls", base(func(c *models.OIDCConfig) {
			c.Type = "github"
			c.AllowedOrgs = []string{"acme"}
			c.AuthBaseURL = "https://8.8.8.8"
			c.APIBaseURL = "https://8.8.8.8/api/v3"
		}), ""},
		{"github api url must not be private", base(func(c *models.OIDCConfig) {
			c.Type = "github"
			c.AllowedOrgs = []string{"acme"}
			c.APIBaseURL = "https://192.168.1.1/api/v3"
		}), "api_base_url"},
		{"github needs client credentials", models.OIDCConfig{Type: "github", AllowedOrgs: []string{"acme"}}, "client_id"},

		{"oauth2 complete", base(func(c *models.OIDCConfig) {
			c.Type = "oauth2"
			c.AuthorizationURL = testAuthURL
			c.TokenURL = "https://8.8.4.4/token"
			c.UserinfoURL = "https://8.8.4.4/userinfo"
		}), ""},
		{"oauth2 requires userinfo_url", base(func(c *models.OIDCConfig) {
			c.Type = "oauth2"
			c.AuthorizationURL = testAuthURL
			c.TokenURL = "https://8.8.4.4/token"
		}), "userinfo_url"},
		{"oauth2 urls must be https", base(func(c *models.OIDCConfig) {
			c.Type = "oauth2"
			c.AuthorizationURL = "http://8.8.4.4/authorize"
			c.TokenURL = "https://8.8.4.4/token"
			c.UserinfoURL = "https://8.8.4.4/userinfo"
		}), "authorization_url"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOIDCConfig(&tc.cfg)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

// Multi-tenant Entra authorities cannot be validated end to end here (the host
// would need DNS), so the gate itself is exercised directly.
func TestValidateOIDCTypeRules_EntraMultiTenantRequiresAllowedTenants(t *testing.T) {
	const tenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

	for _, authority := range []string{
		"https://login.microsoftonline.com/common/v2.0",
		"https://login.microsoftonline.com/organizations/v2.0/",
		"https://login.microsoftonline.com/consumers/v2.0",
	} {
		t.Run(authority, func(t *testing.T) {
			cfg := models.OIDCConfig{Issuer: authority}
			if err := validateOIDCTypeRules(&cfg); err == nil || !strings.Contains(err.Error(), "allowed_tenants") {
				t.Fatalf("a multi-tenant authority without allowed_tenants must be rejected, got %v", err)
			}

			cfg.AllowedTenants = []string{tenant}
			if err := validateOIDCTypeRules(&cfg); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	single := models.OIDCConfig{Issuer: "https://login.microsoftonline.com/" + tenant + "/v2.0"}
	if err := validateOIDCTypeRules(&single); err != nil {
		t.Errorf("a single-tenant authority needs no allow-list: %v", err)
	}
}

func TestMergeOIDCPatch(t *testing.T) {
	stored := json.RawMessage(`{"issuer":"` + testIssuer + `","client_id":"id","client_secret":"old-secret","scopes":["openid"]}`)

	t.Run("untouched fields and the secret survive", func(t *testing.T) {
		merged, err := mergeOIDCPatch(stored, json.RawMessage(`{"hosted_domain":"example.com","require_email_verified":true}`))
		if err != nil {
			t.Fatalf("merge: %v", err)
		}

		var cfg models.OIDCConfig
		if err := json.Unmarshal(merged, &cfg); err != nil {
			t.Fatalf("decode: %v", err)
		}

		if cfg.Issuer != testIssuer || cfg.ClientSecret == "" || cfg.HostedDomain != "example.com" || !cfg.RequireEmailVerified {
			t.Errorf("merged = %+v", cfg)
		}
	})

	t.Run("switching to github enforces its rules on the merged result", func(t *testing.T) {
		if _, err := mergeOIDCPatch(stored, json.RawMessage(`{"type":"github"}`)); err == nil || !strings.Contains(err.Error(), "allowed_orgs") {
			t.Fatalf("expected an allowed_orgs error, got %v", err)
		}

		if _, err := mergeOIDCPatch(stored, json.RawMessage(`{"type":"github","allowed_orgs":["acme"]}`)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("clearing hosted_domain with an empty string works", func(t *testing.T) {
		withHD := json.RawMessage(`{"issuer":"` + testIssuer + `","client_id":"id","client_secret":"s","hosted_domain":"example.com"}`)

		merged, err := mergeOIDCPatch(withHD, json.RawMessage(`{"hosted_domain":""}`))
		if err != nil {
			t.Fatalf("merge: %v", err)
		}

		if strings.Contains(string(merged), "example.com") {
			t.Errorf("hosted_domain not cleared: %s", merged)
		}
	})

	t.Run("a non-object patch is rejected", func(t *testing.T) {
		if _, err := mergeOIDCPatch(stored, json.RawMessage(`[]`)); err == nil {
			t.Fatal("expected an error")
		}
	})
}
