// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
)

// validateOIDCConfig applies the create/patch rules to a (plaintext-secret)
// oidc_config: provider type, required fields per type, outbound-URL safety,
// tenant and organization gates, and sane claim names.
func validateOIDCConfig(cfg *models.OIDCConfig) error {
	switch cfg.Type {
	case models.OIDCTypeOIDC:
		if cfg.Issuer == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
			return errors.New("oidc_config requires issuer, client_id, and client_secret") //nolint:err113 // validation message surfaced to the admin
		}
	case models.OIDCTypeGitHub, models.OIDCTypeOAuth2:
		if cfg.ClientID == "" || cfg.ClientSecret == "" {
			return errors.New("oidc_config requires client_id and client_secret") //nolint:err113 // validation message surfaced to the admin
		}
	default:
		return errors.New(`oidc_config.type must be empty, "github" or "oauth2"`) //nolint:err113 // validation message surfaced to the admin
	}

	if crypto.IsEncrypted(cfg.ClientSecret) {
		return errors.New("oidc_config.client_secret must be the plaintext secret") //nolint:err113 // validation message surfaced to the admin
	}

	if err := validateOIDCURLs(cfg); err != nil {
		return err
	}

	if err := validateOIDCTypeRules(cfg); err != nil {
		return err
	}

	for name, v := range map[string]string{
		"username_claim": cfg.UsernameClaim, "email_claim": cfg.EmailClaim,
		"name_claim": cfg.NameClaim, "groups_claim": cfg.GroupsClaim,
		"hosted_domain": cfg.HostedDomain,
	} {
		if err := validateMappingName("oidc_config."+name, v); err != nil {
			return err
		}
	}

	for _, s := range cfg.Scopes {
		if s == "" || strings.ContainsAny(s, " \t\r\n") {
			return errors.New("oidc_config.scopes entries must be non-empty and contain no whitespace") //nolint:err113 // validation message surfaced to the admin
		}
	}

	return nil
}

// validateOIDCURLs requires every configured URL to be https (http only for
// loopback) and to pass the outbound SSRF validation; the issuer is mandatory
// for a standards-compliant provider and url fields mandatory for "oauth2".
func validateOIDCURLs(cfg *models.OIDCConfig) error {
	urls := []struct{ field, value string }{
		{"issuer", cfg.Issuer},
		{"authorization_url", cfg.AuthorizationURL},
		{"token_url", cfg.TokenURL},
		{"userinfo_url", cfg.UserinfoURL},
		{"auth_base_url", cfg.AuthBaseURL},
		{"api_base_url", cfg.APIBaseURL},
		{"graph_endpoint", cfg.GraphEndpoint},
	}

	if cfg.Type == models.OIDCTypeOAuth2 {
		for _, u := range urls[1:4] {
			if u.value == "" {
				return fmt.Errorf("oidc_config.%s is required for type oauth2", u.field) //nolint:err113 // validation message surfaced to the admin
			}
		}
	}

	for _, u := range urls {
		if u.value == "" {
			continue
		}

		if err := requireHTTPSIdentityURL(u.value); err != nil {
			return fmt.Errorf("oidc_config.%s: %w", u.field, err)
		}

		if err := netutil.ValidateOutboundURL(u.value); err != nil {
			return fmt.Errorf("oidc_config.%s: %w", u.field, err)
		}
	}

	return nil
}

// validateOIDCTypeRules checks the gates that make a provider safe to enable:
// allowed_orgs for GitHub, allowed_tenants for Entra multi-tenant authorities.
func validateOIDCTypeRules(cfg *models.OIDCConfig) error {
	if cfg.Type == models.OIDCTypeGitHub {
		if len(cfg.AllowedOrgs) == 0 {
			return errors.New("oidc_config.allowed_orgs is required for type github") //nolint:err113 // validation message surfaced to the admin
		}

		for _, o := range cfg.AllowedOrgs {
			if o == "" || strings.ContainsAny(o, " \t\r\n/") {
				return errors.New("oidc_config.allowed_orgs entries must be organization logins") //nolint:err113 // validation message surfaced to the admin
			}
		}
	}

	if cfg.Type == models.OIDCTypeOIDC && auth.IsEntraMultiTenantAuthority(cfg.Issuer) && len(cfg.AllowedTenants) == 0 {
		return errors.New("oidc_config.allowed_tenants is required for a multi-tenant Entra authority (common, organizations, consumers)") //nolint:err113 // validation message surfaced to the admin
	}

	for _, t := range cfg.AllowedTenants {
		if !auth.IsEntraGUID(t) {
			return errors.New("oidc_config.allowed_tenants entries must be tenant IDs (GUIDs)") //nolint:err113 // validation message surfaced to the admin
		}
	}

	return nil
}
