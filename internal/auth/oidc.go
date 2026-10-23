// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/telemetry"
)

var (
	ErrSSOProviderNotFound = errors.New("sso provider not found")
	ErrSSOProviderDisabled = errors.New("sso provider disabled")
	ErrOIDCSecretUnusable  = errors.New("oidc client secret cannot be decrypted")
	ErrOIDCDiscoveryFailed = errors.New("oidc discovery failed")
	ErrIDTokenMissing      = errors.New("id token missing")
	ErrIDTokenExpired      = errors.New("id token expired")
)

//nolint:gochecknoglobals // process-lifetime OIDC provider cache
var oidcCache sync.Map

// oidcReloadMinInterval bounds how often a provider's discovery document and
// key set are re-fetched in response to signature verification failures.
const oidcReloadMinInterval = time.Minute

//nolint:gochecknoglobals // process-lifetime reload throttle
var (
	oidcReloadMu   sync.Mutex
	oidcReloadLast = map[string]time.Time{}
)

// allowOIDCReload reports whether slug may be reloaded now and, if so, records
// the reload so the next one is at least oidcReloadMinInterval away.
func allowOIDCReload(slug string) bool {
	oidcReloadMu.Lock()
	defer oidcReloadMu.Unlock()

	now := time.Now()
	if last, ok := oidcReloadLast[slug]; ok && now.Sub(last) < oidcReloadMinInterval {
		return false
	}

	oidcReloadLast[slug] = now

	return true
}

type OIDCProvider struct {
	Slug          string
	Provider      *gooidc.Provider
	Verifier      *gooidc.IDTokenVerifier
	Config        oauth2.Config
	Cfg           models.OIDCConfig
	UpdatedAt     time.Time
	EndSessionURL string
	rawSecret     string // stored (possibly encrypted) secret; cache validity key
}

// LoadOIDCProvider loads an enabled OIDC provider. Disabled providers are
// rejected with ErrSSOProviderDisabled.
func LoadOIDCProvider(ctx context.Context, db database.Database, slug, baseURL string) (*OIDCProvider, error) {
	row, err := db.GetSSOProviderBySlug(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("get sso provider: %w", err)
	}

	if row == nil {
		return nil, ErrSSOProviderNotFound
	}

	if !row.Enabled {
		return nil, ErrSSOProviderDisabled
	}

	return LoadOIDCProviderRow(ctx, row, baseURL)
}

// LoadOIDCProviderRow builds the provider for an already-fetched row without
// checking Enabled; it exists so admins can test-connect a disabled provider.
// Public login paths MUST use LoadOIDCProvider.
func LoadOIDCProviderRow(ctx context.Context, row *models.SSOProvider, baseURL string) (*OIDCProvider, error) {
	slug := row.Slug

	if row.Kind != "oidc" {
		return nil, fmt.Errorf("provider %q is not an oidc provider (kind=%s)", slug, row.Kind) //nolint:err113 // dynamic error with runtime context
	}

	var cfg models.OIDCConfig
	if err := json.Unmarshal(row.OIDCConfig, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal oidc_config: %w", err)
	}

	// The cache is keyed on slug and validated against the row's UpdatedAt and
	// the stored configuration, so an edited row never serves a stale provider.
	if cached, ok := oidcCache.Load(slug); ok {
		if p, ok := cached.(*OIDCProvider); ok {
			if p.UpdatedAt.Equal(row.UpdatedAt) && p.Cfg.Issuer == cfg.Issuer && p.Cfg.ClientID == cfg.ClientID && p.rawSecret == cfg.ClientSecret {
				return p, nil
			}
		}
	}

	rawSecret := cfg.ClientSecret

	secret, err := crypto.Decrypt(cfg.ClientSecret)
	if err != nil {
		// Never fall back to using the ciphertext as the secret.
		return nil, fmt.Errorf("%w: %w", ErrOIDCSecretUnusable, err)
	}

	cfg.ClientSecret = secret

	provider, err := gooidc.NewProvider(oidcContext(ctx), cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc_discovery_failed: %w", ErrOIDCDiscoveryFailed)
	}

	var pc struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}

	_ = provider.Claims(&pc)

	oauth2Cfg := oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  baseURL + "/api/v1/auth/sso/oidc/" + slug + "/callback",
		Scopes:       ensureOpenID(cfg.Scopes),
	}

	p := &OIDCProvider{
		Slug:          slug,
		Provider:      provider,
		Verifier:      provider.Verifier(&gooidc.Config{ClientID: cfg.ClientID}),
		Config:        oauth2Cfg,
		Cfg:           cfg,
		UpdatedAt:     row.UpdatedAt,
		EndSessionURL: pc.EndSessionEndpoint,
		rawSecret:     rawSecret,
	}

	oidcCache.Store(slug, p)

	return p, nil
}

// oidcHTTPClient performs every outbound OIDC request (discovery, JWKS, token
// exchange). Issuer metadata is attacker-influenceable (admin-configured URL,
// DNS rebinding), so all of it goes through the SSRF dial guard.
//
//nolint:gochecknoglobals // shared SSRF-guarded client, reuses connections
var oidcHTTPClient = telemetry.WrapClient(netutil.SafeHTTPClient(30 * time.Second))

// oidcContext attaches oidcHTTPClient to ctx for go-oidc and oauth2.
func oidcContext(ctx context.Context) context.Context {
	return gooidc.ClientContext(ctx, oidcHTTPClient)
}

func ensureOpenID(scopes []string) []string {
	if len(scopes) == 0 {
		return []string{"openid", "profile", "email", "groups"}
	}

	if slices.Contains(scopes, "openid") {
		return scopes
	}

	return append([]string{"openid"}, scopes...)
}

func (p *OIDCProvider) AuthURL(state, nonce, codeChallenge string) string {
	return p.Config.AuthCodeURL(
		state,
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		gooidc.Nonce(nonce),
	)
}

// ExchangeCode redeems the (single-use) authorization code and returns the raw
// ID token without verifying it.
func (p *OIDCProvider) ExchangeCode(ctx context.Context, code, codeVerifier string) (string, error) {
	token, err := p.Config.Exchange(oidcContext(ctx), code, oauth2.SetAuthURLParam("code_verifier", codeVerifier))
	if err != nil {
		return "", fmt.Errorf("code exchange: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return "", ErrIDTokenMissing
	}

	return rawIDToken, nil
}

// VerifyIDToken verifies rawIDToken against this provider's verifier and
// decodes its claims.
func (p *OIDCProvider) VerifyIDToken(ctx context.Context, rawIDToken string) (*gooidc.IDToken, map[string]any, error) {
	idToken, err := p.Verifier.Verify(oidcContext(ctx), rawIDToken)
	if err != nil {
		if _, expired := errors.AsType[*gooidc.TokenExpiredError](err); expired {
			return nil, nil, fmt.Errorf("%w: %w", ErrIDTokenExpired, err)
		}

		return nil, nil, fmt.Errorf("verify id token: %w", err)
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, nil, fmt.Errorf("decode claims: %w", err)
	}

	return idToken, claims, nil
}

// Exchange redeems the code and verifies the resulting ID token once.
func (p *OIDCProvider) Exchange(ctx context.Context, code, codeVerifier string) (*gooidc.IDToken, map[string]any, error) {
	rawIDToken, err := p.ExchangeCode(ctx, code, codeVerifier)
	if err != nil {
		return nil, nil, err
	}

	return p.VerifyIDToken(ctx, rawIDToken)
}

// oidcSignatureFailureMessage is go-oidc's RemoteKeySet error when no key in
// the (freshly re-fetched) JWKS verifies the token signature.
const oidcSignatureFailureMessage = "failed to verify id token signature"

// IsUnknownKeyError reports whether err is go-oidc's JWKS signature failure
// (no published key verified the token), the only kind a key rotation or a
// stale discovery document can explain. go-oidc errors are unstructured, so
// the specific message is matched; other verification failures (expiry,
// audience, issuer, malformed token) are not retried.
func IsUnknownKeyError(err error) bool {
	if err == nil {
		return false
	}

	return strings.Contains(err.Error(), oidcSignatureFailureMessage)
}

func (p *OIDCProvider) EndSession(idTokenHint, postLogoutRedirectURI string) (string, bool) {
	if p.EndSessionURL == "" {
		return "", false
	}

	u, err := url.Parse(p.EndSessionURL)
	if err != nil {
		return "", false
	}

	q := u.Query()
	q.Set("id_token_hint", idTokenHint)
	q.Set("post_logout_redirect_uri", postLogoutRedirectURI)
	u.RawQuery = q.Encode()

	return u.String(), true
}

// ExchangeWithRetry redeems the authorization code exactly once. If the ID
// token fails with go-oidc's signature-verification error (possible key
// rotation), the same raw ID token is re-verified with a freshly loaded
// provider; the code is never re-exchanged. Reloads are limited to one per
// provider per oidcReloadMinInterval.
func ExchangeWithRetry(ctx context.Context, db database.Database, slug, baseURL, code, codeVerifier string) (*gooidc.IDToken, map[string]any, *OIDCProvider, error) {
	p, err := LoadOIDCProvider(ctx, db, slug, baseURL)
	if err != nil {
		return nil, nil, nil, err
	}

	rawIDToken, err := p.ExchangeCode(ctx, code, codeVerifier)
	if err != nil {
		return nil, nil, p, err
	}

	idToken, claims, err := p.VerifyIDToken(ctx, rawIDToken)
	if err == nil {
		return idToken, claims, p, nil
	}

	if !IsUnknownKeyError(err) || !allowOIDCReload(slug) {
		return nil, nil, p, err
	}

	oidcCache.CompareAndDelete(slug, p)

	p2, err2 := LoadOIDCProvider(ctx, db, slug, baseURL)
	if err2 != nil {
		return nil, nil, nil, err2
	}

	idToken, claims, err = p2.VerifyIDToken(ctx, rawIDToken)

	return idToken, claims, p2, err
}
