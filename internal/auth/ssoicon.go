// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Icon keys of the login button. The web UI maps each to a glyph; vendors
// without a glyph in the bundled icon set (Microsoft Entra, Ping, OneLogin,
// JumpCloud, Duo, Dex) resolve to the generic OpenID or SAML key instead.
const (
	SSOIconGoogle    = "google"
	SSOIconGitHub    = "github"
	SSOIconGitLab    = "gitlab"
	SSOIconOkta      = "okta"
	SSOIconAuth0     = "auth0"
	SSOIconKeycloak  = "keycloak"
	SSOIconAuthentik = "authentik"
	SSOIconGitea     = "gitea"
	SSOIconBitbucket = "bitbucket"
	SSOIconAtlassian = "atlassian"
	SSOIconOpenShift = "openshift"
	SSOIconOpenStack = "openstack"
	SSOIconOpenID    = "openid"
	SSOIconLDAP      = "ldap"
	SSOIconSAML      = "saml"
	SSOIconKey       = "key"
)

// ssoIconKeys are the keys an admin may store, besides the empty string
// (automatic).
func ssoIconKeys() []string {
	return []string{
		SSOIconGoogle, SSOIconGitHub, SSOIconGitLab, SSOIconOkta, SSOIconAuth0, SSOIconKeycloak,
		SSOIconAuthentik, SSOIconGitea, SSOIconBitbucket, SSOIconAtlassian, SSOIconOpenShift,
		SSOIconOpenStack, SSOIconOpenID, SSOIconLDAP, SSOIconSAML, SSOIconKey,
	}
}

// ValidSSOIcon reports whether key is storable: empty (automatic) or a known key.
func ValidSSOIcon(key string) bool {
	return key == "" || slices.Contains(ssoIconKeys(), key)
}

// dexConnectorIcons maps a fragment of a Dex connector id to an icon, checked
// in order, so "github-main" and "corp-ldap" pick the vendor of the upstream.
func dexConnectorIcons() [][2]string {
	return [][2]string{
		{"github", SSOIconGitHub}, {"gitlab", SSOIconGitLab}, {"google", SSOIconGoogle},
		{"ldap", SSOIconLDAP}, {"bitbucket", SSOIconBitbucket}, {"gitea", SSOIconGitea},
		{"openshift", SSOIconOpenShift}, {"keystone", SSOIconOpenStack},
		{"atlassian", SSOIconAtlassian}, {"crowd", SSOIconAtlassian},
	}
}

// ResolveSSOIcon is the icon the login button of p shows, never empty. An
// explicit icon wins; then a GitHub provider type, a Dex connector id naming a
// known vendor, and the host or path of the issuer (OIDC) or the metadata URL
// (SAML); else the generic OpenID or SAML key.
func ResolveSSOIcon(p *models.SSOProvider) string {
	if p.Icon != "" && ValidSSOIcon(p.Icon) {
		return p.Icon
	}

	fallback, endpoint := SSOIconOpenID, ""

	switch p.Kind {
	case "saml":
		fallback = SSOIconSAML

		var cfg models.SAMLConfig
		if json.Unmarshal(p.SAMLConfig, &cfg) == nil {
			endpoint = cfg.IDPMetadataURL
		}
	default:
		var cfg models.OIDCConfig
		if json.Unmarshal(p.OIDCConfig, &cfg) == nil {
			if cfg.Type == "github" {
				return SSOIconGitHub
			}

			if icon := dexConnectorIcon(cfg.DexConnectorID); icon != "" {
				return icon
			}

			endpoint = cfg.Issuer
		}
	}

	if icon := endpointIcon(endpoint); icon != "" {
		return icon
	}

	return fallback
}

func dexConnectorIcon(id string) string {
	id = strings.ToLower(id)
	for _, c := range dexConnectorIcons() {
		if strings.Contains(id, c[0]) {
			return c[1]
		}
	}

	return ""
}

// endpointIcon recognises a vendor from the host and path of an issuer or
// metadata URL; empty when none matches.
func endpointIcon(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}

	host := strings.ToLower(u.Hostname())

	switch {
	case host == "accounts.google.com":
		return SSOIconGoogle
	case hostIn(host, "okta.com", "oktapreview.com"):
		return SSOIconOkta
	case hostIn(host, "auth0.com"):
		return SSOIconAuth0
	case host == "gitlab.com":
		return SSOIconGitLab
	case host == "github.com":
		return SSOIconGitHub
	case strings.Contains(u.Path, "/realms/"):
		return SSOIconKeycloak
	case strings.Contains(u.Path, "/application/o/"):
		return SSOIconAuthentik
	}

	return ""
}

// hostIn reports whether host is one of the domains or a subdomain of it.
func hostIn(host string, domains ...string) bool {
	for _, d := range domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}

	return false
}

// OIDCCallbackURL is the redirect URI an identity provider must allow for the
// OIDC provider slug.
func OIDCCallbackURL(baseURL, slug string) string {
	return baseURL + "/api/v1/auth/sso/oidc/" + slug + "/callback"
}

// SAMLACSURL is the assertion consumer service URL of the SAML provider slug.
func SAMLACSURL(baseURL, slug string) string {
	return baseURL + "/api/v1/auth/sso/saml/" + slug + "/acs"
}

// SAMLEntityID is the service-provider entity id (and metadata URL) of the
// SAML provider slug.
func SAMLEntityID(baseURL, slug string) string {
	return baseURL + "/api/v1/auth/sso/saml/" + slug + "/metadata"
}
