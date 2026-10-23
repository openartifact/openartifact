// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/pkg/logger"
)

// ErrSSOLoginDenied marks a login the identity provider authenticated but
// local policy rejects (unverified email, wrong hosted domain or tenant,
// organization gate). Handlers answer it with one generic 403 and log the
// wrapped detail.
var ErrSSOLoginDenied = errors.New("sso login denied")

// denied wraps ErrSSOLoginDenied with a log-only reason.
func denied(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSSOLoginDenied, fmt.Sprintf(format, args...))
}

// googleIssuer is the issuer of Google's OpenID Connect endpoint.
const googleIssuer = "https://accounts.google.com"

func isGoogleIssuer(issuer string) bool {
	return strings.TrimSuffix(issuer, "/") == googleIssuer
}

// Identity turns a completed exchange into the claims the provisioner
// consumes, after applying the provider's login policy. For OIDC it maps the
// verified ID token claims (plus Entra groups overage resolution); for the
// OAuth 2.0 flavours it queries the provider's API with the access token.
// A policy rejection is an ErrSSOLoginDenied.
func (p *OIDCProvider) Identity(ctx context.Context, res *ExchangeResult, log *logger.Logger) (SSOClaims, error) {
	var (
		claims SSOClaims
		err    error
	)

	if p.IsOAuth2() {
		claims, err = p.oauth2Identity(ctx, res.AccessToken, log)
	} else {
		claims, err = p.oidcIdentity(ctx, res, log)
	}

	if err != nil {
		return SSOClaims{}, err
	}

	claims.ProviderSlug = p.Slug

	return claims, nil
}

func (p *OIDCProvider) oidcIdentity(ctx context.Context, res *ExchangeResult, log *logger.Logger) (SSOClaims, error) {
	cfg := p.Cfg
	claims := res.Claims

	if (isGoogleIssuer(cfg.Issuer) || cfg.RequireEmailVerified) && !boolClaimTrue(claims, "email_verified") {
		return SSOClaims{}, denied("email_verified is not true")
	}

	if cfg.HostedDomain != "" {
		if hd := stringClaim(claims, "hd"); !strings.EqualFold(hd, strings.TrimSpace(cfg.HostedDomain)) {
			return SSOClaims{}, denied("hd claim %q does not match hosted_domain", hd)
		}
	}

	externalID := res.IDToken.Subject
	if p.tenancy != nil {
		// A subject is only unique within its tenant.
		externalID = stringClaim(claims, "tid") + ":" + externalID
	}

	out := SSOClaims{
		ExternalID: externalID,
		Username:   stringClaim(claims, firstNonEmpty(cfg.UsernameClaim, "preferred_username")),
		Email:      stringClaim(claims, firstNonEmpty(cfg.EmailClaim, "email")),
		FullName:   stringClaim(claims, firstNonEmpty(cfg.NameClaim, "name")),
		// email_verified is only meaningful for the standard "email" claim; a
		// custom email claim has no verification assertion.
		EmailVerified: (cfg.EmailClaim == "" || cfg.EmailClaim == "email") && boolClaimTrue(claims, "email_verified"),
	}

	out.Groups, out.GroupsPresent = p.oidcGroups(ctx, res, log)

	return out, nil
}

// oidcGroups reads the groups claim. When Entra signals a groups overage the
// claim is not in the token, so the user's groups are unknown (present=false:
// existing memberships are kept) unless graph_groups resolves them.
func (p *OIDCProvider) oidcGroups(ctx context.Context, res *ExchangeResult, log *logger.Logger) ([]string, bool) {
	key := firstNonEmpty(p.Cfg.GroupsClaim, "groups")

	if key != "groups" || !groupsOverage(res.Claims) {
		return groupsFromClaims(res.Claims, key)
	}

	if !p.Cfg.GraphGroups {
		warnf(log, "oidc login: groups overage, groups are not in the token; enable graph_groups to resolve them", "slug", p.Slug)
		return nil, false
	}

	ids, err := graphMemberObjects(ctx, firstNonEmpty(p.Cfg.GraphEndpoint, defaultGraphEndpoint), res.AccessToken)
	if err != nil {
		warnf(log, "oidc login: groups overage, resolving groups through Microsoft Graph failed; keeping existing memberships",
			"slug", p.Slug, "error", err)

		return nil, false
	}

	return ids, true
}

func warnf(log *logger.Logger, msg string, kv ...any) {
	if log != nil {
		log.Warnw(msg, kv...)
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}

	return b
}

func stringClaim(claims map[string]any, key string) string {
	if s, ok := claims[key].(string); ok {
		return s
	}

	return ""
}

// boolClaimTrue reports whether claims[key] is boolean true. Some IdPs
// (e.g. Cognito) encode it as the string "true"; that is accepted too.
func boolClaimTrue(claims map[string]any, key string) bool {
	switch v := claims[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	}

	return false
}

// groupsFromClaims reads the groups claim. present is false when the claim is
// missing (or null, or of an unusable type): the IdP then said nothing about
// the user's groups, which is different from an empty list.
func groupsFromClaims(claims map[string]any, key string) ([]string, bool) {
	switch t := claims[key].(type) {
	case []string:
		return t, true
	case []any:
		out := make([]string, 0, len(t))

		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}

		return out, true
	case string:
		// Some IdPs emit a lone group as a bare string.
		if t == "" {
			return []string{}, true
		}

		return []string{t}, true
	}

	return nil, false
}
