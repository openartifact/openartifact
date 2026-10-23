// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"encoding/json"
	"time"
)

type SSOProvider struct {
	ID                 string            `json:"id,omitempty"`
	Slug               string            `json:"slug"`
	Kind               string            `json:"kind"`
	DisplayName        string            `json:"display_name"`
	Enabled            bool              `json:"enabled"`
	OIDCConfig         json.RawMessage   `json:"oidc_config,omitempty"`
	SAMLConfig         json.RawMessage   `json:"saml_config,omitempty"`
	ClaimMapping       json.RawMessage   `json:"claim_mapping,omitempty"`
	GroupMapping       map[string]string `json:"group_mapping,omitempty"`
	PermissionMapping  map[string]string `json:"permission_mapping,omitempty"`
	AutoProvisionUsers bool              `json:"auto_provision_users"`
	LinkByEmail        bool              `json:"link_by_email"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

// OIDCConfig configures an "oidc" kind provider. Type selects the flavour:
// empty is a standards-compliant OIDC provider (discovery + ID token);
// "github" and "oauth2" are plain OAuth 2.0 providers without an ID token
// (identity comes from the userinfo endpoint).
type OIDCConfig struct {
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// Scopes defaults to "openid email profile"; "groups" and any other
	// IdP-specific scope are opt-in.
	Scopes        []string `json:"scopes"`
	UsernameClaim string   `json:"username_claim"`
	EmailClaim    string   `json:"email_claim"`
	NameClaim     string   `json:"name_claim"`
	GroupsClaim   string   `json:"groups_claim"`

	// Type is "", "github" or "oauth2".
	Type string `json:"type,omitempty"`

	// AllowedTenants lists the Entra tenant IDs accepted for a multi-tenant
	// authority (issuer .../common/v2.0, .../organizations/v2.0). The ID
	// token's tid claim must be one of them. Required for those authorities.
	AllowedTenants []string `json:"allowed_tenants,omitempty"`
	// HostedDomain restricts Google logins to one Workspace domain: the ID
	// token's hd claim must equal it (the hd request parameter is only a hint).
	HostedDomain string `json:"hosted_domain,omitempty"`
	// RequireEmailVerified rejects logins whose ID token does not carry
	// email_verified=true. Always enforced for Google (accounts.google.com).
	RequireEmailVerified bool `json:"require_email_verified,omitempty"`
	// GraphGroups fetches the user's groups from Microsoft Graph when Entra
	// signals a groups overage (more groups than fit in the token). Needs the
	// Graph scope in Scopes (for example https://graph.microsoft.com/User.Read)
	// and admin consent for the matching permission.
	GraphGroups bool `json:"graph_groups,omitempty"`
	// GraphEndpoint overrides https://graph.microsoft.com (national clouds).
	GraphEndpoint string `json:"graph_endpoint,omitempty"`

	// OAuth2-only (Type "github" or "oauth2"). For "github" every URL defaults
	// to github.com; set AuthBaseURL/APIBaseURL for GitHub Enterprise Server.
	AuthorizationURL string `json:"authorization_url,omitempty"`
	TokenURL         string `json:"token_url,omitempty"`
	UserinfoURL      string `json:"userinfo_url,omitempty"`
	AuthBaseURL      string `json:"auth_base_url,omitempty"`
	APIBaseURL       string `json:"api_base_url,omitempty"`
	// AllowedOrgs (github) lists the organizations a user must belong to; a
	// login from outside them is rejected, and "org" / "org/team-slug" become
	// the user's groups.
	AllowedOrgs []string `json:"allowed_orgs,omitempty"`
}

// OIDC provider flavours (OIDCConfig.Type).
const (
	OIDCTypeOIDC   = ""
	OIDCTypeGitHub = "github"
	OIDCTypeOAuth2 = "oauth2"
)

type SAMLConfig struct {
	IDPMetadataURL string `json:"idp_metadata_url"`
	IDPMetadataXML string `json:"idp_metadata_xml"`
	// IDPMetadataRefreshSeconds is how long fetched IdP metadata is reused
	// before it is re-read from IDPMetadataURL (default 3600, minimum 60).
	IDPMetadataRefreshSeconds int    `json:"idp_metadata_refresh_seconds,omitempty"`
	UsernameAttribute         string `json:"username_attribute"`
	EmailAttribute            string `json:"email_attribute"`
	NameAttribute             string `json:"name_attribute"`
	GroupsAttribute           string `json:"groups_attribute"`
	// AllowIDPInitiated accepts unsolicited responses (IdP tiles, Entra My
	// Apps). Off by default: such responses carry no InResponseTo binding.
	AllowIDPInitiated bool `json:"allow_idp_initiated,omitempty"`
}

type ClaimMapping struct {
	UsernameClaim string `json:"username_claim"`
	EmailClaim    string `json:"email_claim"`
	NameClaim     string `json:"name_claim"`
	GroupsClaim   string `json:"groups_claim"`
}
