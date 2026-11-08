// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"encoding/json"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func iconOIDC(t *testing.T, icon string, cfg models.OIDCConfig) *models.SSOProvider {
	t.Helper()

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	return &models.SSOProvider{Kind: "oidc", Icon: icon, OIDCConfig: raw}
}

func iconSAML(t *testing.T, icon, metadataURL string) *models.SSOProvider {
	t.Helper()

	raw, err := json.Marshal(models.SAMLConfig{IDPMetadataURL: metadataURL})
	if err != nil {
		t.Fatal(err)
	}

	return &models.SSOProvider{Kind: "saml", Icon: icon, SAMLConfig: raw}
}

func TestResolveSSOIcon(t *testing.T) {
	tests := []struct {
		name string
		p    *models.SSOProvider
		want string
	}{
		{"explicit icon wins over everything", iconOIDC(t, "keycloak", models.OIDCConfig{
			Type: "github", DexConnectorID: "gitlab", Issuer: "https://accounts.google.com",
		}), "keycloak"},
		{"explicit icon on SAML", iconSAML(t, "key", "https://x.okta.com/app/metadata"), "key"},
		{"unknown stored icon is ignored", iconOIDC(t, "nope", models.OIDCConfig{Issuer: "https://accounts.google.com"}), "google"},
		{"github type wins over a Dex connector", iconOIDC(t, "", models.OIDCConfig{
			Type: "github", DexConnectorID: "gitlab",
		}), "github"},
		{"dex connector beats the issuer host", iconOIDC(t, "", models.OIDCConfig{
			DexConnectorID: "corp-gitlab", Issuer: "https://accounts.google.com",
		}), "gitlab"},
		{"dex github", iconOIDC(t, "", models.OIDCConfig{DexConnectorID: "GitHub-main", Issuer: "https://dex.example/dex"}), "github"},
		{"dex google", iconOIDC(t, "", models.OIDCConfig{DexConnectorID: "google"}), "google"},
		{"dex ldap", iconOIDC(t, "", models.OIDCConfig{DexConnectorID: "ldap"}), "ldap"},
		{"dex bitbucket", iconOIDC(t, "", models.OIDCConfig{DexConnectorID: "bitbucket-cloud"}), "bitbucket"},
		{"dex gitea", iconOIDC(t, "", models.OIDCConfig{DexConnectorID: "gitea"}), "gitea"},
		{"dex openshift", iconOIDC(t, "", models.OIDCConfig{DexConnectorID: "openshift"}), "openshift"},
		{"dex keystone is OpenStack", iconOIDC(t, "", models.OIDCConfig{DexConnectorID: "keystone"}), "openstack"},
		{"dex atlassian crowd", iconOIDC(t, "", models.OIDCConfig{DexConnectorID: "crowd"}), "atlassian"},
		{"dex connector without a vendor falls back to the issuer", iconOIDC(t, "", models.OIDCConfig{
			DexConnectorID: "local", Issuer: "https://corp.okta.com",
		}), "okta"},
		{"google issuer", iconOIDC(t, "", models.OIDCConfig{Issuer: "https://accounts.google.com"}), "google"},
		{"okta", iconOIDC(t, "", models.OIDCConfig{Issuer: "https://dev-1.okta.com/oauth2/default"}), "okta"},
		{"okta preview", iconOIDC(t, "", models.OIDCConfig{Issuer: "https://dev-1.oktapreview.com"}), "okta"},
		{"auth0", iconOIDC(t, "", models.OIDCConfig{Issuer: "https://tenant.eu.auth0.com/"}), "auth0"},
		{"gitlab.com", iconOIDC(t, "", models.OIDCConfig{Issuer: "https://gitlab.com"}), "gitlab"},
		{"github.com", iconOIDC(t, "", models.OIDCConfig{Issuer: "https://github.com"}), "github"},
		{"keycloak realm", iconOIDC(t, "", models.OIDCConfig{Issuer: "https://sso.corp.example/realms/main"}), "keycloak"},
		{"authentik application", iconOIDC(t, "", models.OIDCConfig{
			Issuer: "https://auth.corp.example/application/o/openartifact/",
		}), "authentik"},
		{"a look-alike host is not okta", iconOIDC(t, "", models.OIDCConfig{Issuer: "https://notokta.com"}), "openid"},
		{"a self-hosted gitlab has no rule", iconOIDC(t, "", models.OIDCConfig{Issuer: "https://gitlab.corp.example"}), "openid"},
		{"Microsoft has no glyph", iconOIDC(t, "", models.OIDCConfig{Issuer: "https://login.microsoftonline.com/x/v2.0"}), "openid"},
		{"generic OIDC", iconOIDC(t, "", models.OIDCConfig{Issuer: "https://idp.example"}), "openid"},
		{"OAuth2 without issuer", iconOIDC(t, "", models.OIDCConfig{Type: "oauth2"}), "openid"},
		{"unparseable config", &models.SSOProvider{Kind: "oidc", OIDCConfig: json.RawMessage(`[`)}, "openid"},
		{"generic SAML", iconSAML(t, "", "https://idp.example/metadata"), "saml"},
		{"SAML inline metadata", &models.SSOProvider{Kind: "saml", SAMLConfig: json.RawMessage(`{"idp_metadata_xml":"<x/>"}`)}, "saml"},
		{"SAML Okta", iconSAML(t, "", "https://corp.okta.com/app/abc/sso/saml/metadata"), "okta"},
		{"SAML Keycloak", iconSAML(t, "", "https://sso.example/realms/r/protocol/saml/descriptor"), "keycloak"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveSSOIcon(tt.p); got != tt.want {
				t.Errorf("ResolveSSOIcon = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidSSOIcon(t *testing.T) {
	for _, k := range ssoIconKeys() {
		if !ValidSSOIcon(k) {
			t.Errorf("%q must be valid", k)
		}
	}

	if !ValidSSOIcon("") {
		t.Error("empty (automatic) must be valid")
	}

	for _, k := range []string{"Google", "microsoft", "entra", "dex", " okta", "okta ", "../x"} {
		if ValidSSOIcon(k) {
			t.Errorf("%q must be rejected", k)
		}
	}
}

func TestSSOCallbackURLs(t *testing.T) {
	if got := OIDCCallbackURL("https://oa.example", "corp"); got != "https://oa.example/api/v1/auth/sso/oidc/corp/callback" {
		t.Errorf("OIDC callback = %q", got)
	}

	if got := SAMLACSURL("https://oa.example", "corp"); got != "https://oa.example/api/v1/auth/sso/saml/corp/acs" {
		t.Errorf("SAML ACS = %q", got)
	}
}
