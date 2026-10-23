// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	gooidc "github.com/coreos/go-oidc/v3/oidc"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// ErrAccessTokenNotAccepted means a bearer credential is not an OIDC access
// token this server accepts (unknown issuer, wrong audience, unknown user…).
var ErrAccessTokenNotAccepted = errors.New("oidc access token not accepted")

// OIDCIssuers returns the issuer URL of every enabled OIDC provider. It is
// what the protected-resource metadata advertises as authorization servers.
func OIDCIssuers(ctx context.Context, db database.Database) ([]string, error) {
	rows, err := db.ListEnabledSSOProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sso providers: %w", err)
	}

	issuers := make([]string, 0, len(rows))

	for _, row := range rows {
		cfg, ok := oidcRowConfig(row)
		if ok && cfg.Issuer != "" && !slices.Contains(issuers, cfg.Issuer) {
			issuers = append(issuers, cfg.Issuer)
		}
	}

	return issuers, nil
}

func oidcRowConfig(row *models.SSOProvider) (models.OIDCConfig, bool) {
	var cfg models.OIDCConfig

	if row == nil || row.Kind != "oidc" || json.Unmarshal(row.OIDCConfig, &cfg) != nil {
		return cfg, false
	}

	// Plain OAuth 2.0 providers (github, oauth2) issue no JWT access tokens.
	return cfg, cfg.Type == models.OIDCTypeOIDC
}

// unverifiedIssuer reads the iss claim of a compact JWT without verifying it.
// The value only selects which provider's keys verify the token afterwards.
func unverifiedIssuer(raw string) (string, bool) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 { //nolint:mnd // header.payload.signature
		return "", false
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}

	var claims struct {
		Iss string `json:"iss"`
	}

	if json.Unmarshal(payload, &claims) != nil || claims.Iss == "" {
		return "", false
	}

	return claims.Iss, true
}

// VerifyOIDCAccessToken validates a JWT access token issued by one of the
// enabled OIDC providers for this server and returns the user it maps to.
//
// The token must be signed by the provider that its iss names, be unexpired and
// carry audience (the canonical MCP resource URI, RFC 8707) in aud; ID tokens
// minted for the web login (aud = client ID) therefore never qualify. The
// subject must already be linked to a local user through that provider
// (ListEnabledSSOProviders/GetUserByExternalID): access tokens never provision
// users, and disabled users are refused.
func VerifyOIDCAccessToken(ctx context.Context, db database.Database, baseURL, rawToken, audience string) (*models.User, error) {
	issuer, ok := unverifiedIssuer(rawToken)
	if !ok {
		return nil, ErrAccessTokenNotAccepted
	}

	rows, err := db.ListEnabledSSOProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sso providers: %w", err)
	}

	for _, row := range rows {
		cfg, ok := oidcRowConfig(row)
		if !ok || cfg.Issuer != issuer {
			continue
		}

		user, err := verifyAccessTokenWith(ctx, db, row, baseURL, rawToken, audience)
		if err != nil {
			return nil, err
		}

		return user, nil
	}

	return nil, ErrAccessTokenNotAccepted
}

func verifyAccessTokenWith(ctx context.Context, db database.Database, row *models.SSOProvider, baseURL, rawToken, audience string) (*models.User, error) {
	p, err := LoadOIDCProviderRow(ctx, row, baseURL)
	if err != nil {
		return nil, fmt.Errorf("load oidc provider: %w", err)
	}

	token, err := verifyAccessToken(ctx, p, rawToken)
	if err != nil && IsUnknownKeyError(err) && allowOIDCReload(row.Slug) {
		// Possible key rotation: retry once with a freshly loaded provider.
		oidcCache.CompareAndDelete(row.Slug, p)

		if p, err = LoadOIDCProviderRow(ctx, row, baseURL); err != nil {
			return nil, fmt.Errorf("reload oidc provider: %w", err)
		}

		token, err = verifyAccessToken(ctx, p, rawToken)
	}

	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAccessTokenNotAccepted, err)
	}

	if !audienceMatches(token.Audience, audience) {
		return nil, fmt.Errorf("%w: audience", ErrAccessTokenNotAccepted)
	}

	user, err := db.GetUserByExternalID(ctx, token.Subject, row.Slug)
	if err != nil {
		return nil, fmt.Errorf("get user by external id: %w", err)
	}

	if user == nil || user.Disabled {
		return nil, fmt.Errorf("%w: subject", ErrAccessTokenNotAccepted)
	}

	return user, nil
}

// verifyAccessToken checks signature, issuer and expiry. The client-ID
// audience check is skipped because an access token is not addressed to the
// login client; the caller enforces the resource audience instead.
func verifyAccessToken(ctx context.Context, p *OIDCProvider, raw string) (*gooidc.IDToken, error) {
	verifier := p.Provider.Verifier(&gooidc.Config{SkipClientIDCheck: true})

	return verifier.Verify(oidcContext(ctx), raw) //nolint:wrapcheck // callers wrap
}

// audienceMatches compares aud entries to the resource URI ignoring a trailing
// slash, the only normalization RFC 8707 clients commonly differ on.
func audienceMatches(aud []string, want string) bool {
	want = strings.TrimRight(want, "/")

	return slices.ContainsFunc(aud, func(a string) bool { return strings.TrimRight(a, "/") == want })
}
