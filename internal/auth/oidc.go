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
	// ErrOIDCUnknownType is returned for a stored oidc_config.type this build
	// does not implement.
	ErrOIDCUnknownType = errors.New("unsupported oidc provider type")
	// ErrOIDCConfigInvalid is returned when a stored config cannot work (for
	// example a multi-tenant Entra authority without allowed tenants).
	ErrOIDCConfigInvalid = errors.New("oidc provider configuration invalid")
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

// OIDCProvider is a loaded "oidc" kind provider. For Type "" it carries the
// discovery result and ID token verifier; for the plain OAuth 2.0 flavours
// (github, oauth2) Provider and Verifier are nil and identity comes from API
// calls made with the access token (see Identity).
type OIDCProvider struct {
	Slug          string
	Provider      *gooidc.Provider
	Verifier      *gooidc.IDTokenVerifier
	Config        oauth2.Config
	Cfg           models.OIDCConfig
	UpdatedAt     time.Time
	EndSessionURL string

	cfgRaw  string        // stored oidc_config JSON; cache validity key
	tenancy *entraTenancy // non-nil for an Entra multi-tenant authority
}

// IsOAuth2 reports whether the provider is a plain OAuth 2.0 flavour (GitHub
// or generic) that has no discovery document, ID token or nonce.
func (p *OIDCProvider) IsOAuth2() bool {
	return p.Cfg.Type == models.OIDCTypeGitHub || p.Cfg.Type == models.OIDCTypeOAuth2
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
	cfgRaw := string(row.OIDCConfig)

	if cached, ok := oidcCache.Load(slug); ok {
		if p, ok := cached.(*OIDCProvider); ok && p.UpdatedAt.Equal(row.UpdatedAt) && p.cfgRaw == cfgRaw {
			return p, nil
		}
	}

	secret, err := crypto.Decrypt(cfg.ClientSecret)
	if err != nil {
		// Never fall back to using the ciphertext as the secret.
		return nil, fmt.Errorf("%w: %w", ErrOIDCSecretUnusable, err)
	}

	cfg.ClientSecret = secret
	redirectURL := baseURL + "/api/v1/auth/sso/oidc/" + slug + "/callback"

	var p *OIDCProvider

	switch cfg.Type {
	case models.OIDCTypeOIDC:
		p, err = newDiscoveredProvider(ctx, slug, cfg, redirectURL)
	case models.OIDCTypeGitHub, models.OIDCTypeOAuth2:
		p, err = newOAuth2Provider(slug, cfg, redirectURL)
	default:
		err = fmt.Errorf("%w: %q", ErrOIDCUnknownType, cfg.Type)
	}

	if err != nil {
		return nil, err
	}

	p.UpdatedAt = row.UpdatedAt
	p.cfgRaw = cfgRaw
	oidcCache.Store(slug, p)

	return p, nil
}

// newDiscoveredProvider performs OIDC discovery for a standards-compliant
// provider (including the Entra multi-tenant authorities).
func newDiscoveredProvider(ctx context.Context, slug string, cfg models.OIDCConfig, redirectURL string) (*OIDCProvider, error) {
	provider, tenancy, err := discoverProvider(ctx, cfg)
	if err != nil {
		return nil, err
	}

	var pc struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}

	_ = provider.Claims(&pc)

	return &OIDCProvider{
		Slug:     slug,
		Provider: provider,
		Verifier: provider.Verifier(&gooidc.Config{
			ClientID: cfg.ClientID,
			// The multi-tenant issuer is a template; it is verified against
			// the token's tid claim in entraTenancy.verify instead.
			SkipIssuerCheck: tenancy != nil,
		}),
		Config: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  redirectURL,
			Scopes:       ensureOpenID(cfg.Scopes),
		},
		Cfg:           cfg,
		EndSessionURL: pc.EndSessionEndpoint,
		tenancy:       tenancy,
	}, nil
}

// oidcHTTPClient performs every outbound OIDC request (discovery, JWKS, token
// exchange, userinfo and Graph calls). Issuer metadata is
// attacker-influenceable (admin-configured URL, DNS rebinding), so all of it
// goes through the SSRF dial guard.
//
//nolint:gochecknoglobals // shared SSRF-guarded client, reuses connections
var oidcHTTPClient = telemetry.WrapClient(netutil.SafeHTTPClient(30 * time.Second))

// oidcContext attaches oidcHTTPClient to ctx for go-oidc and oauth2.
func oidcContext(ctx context.Context) context.Context {
	return gooidc.ClientContext(ctx, oidcHTTPClient)
}

// ensureOpenID returns the scopes to request. The default is exactly
// "openid email profile": IdP-specific scopes such as "groups" are opt-in
// through the configured scopes, because some IdPs reject unknown scopes.
func ensureOpenID(scopes []string) []string {
	if len(scopes) == 0 {
		return []string{"openid", "email", "profile"}
	}

	if slices.Contains(scopes, "openid") {
		return scopes
	}

	return append([]string{"openid"}, scopes...)
}

// AuthURL builds the authorization redirect. The nonce is bound into the ID
// token and therefore only sent for OIDC providers.
func (p *OIDCProvider) AuthURL(state, nonce, codeChallenge string) string {
	opts := []oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	}

	if !p.IsOAuth2() {
		opts = append(opts, gooidc.Nonce(nonce))
	}

	if p.Cfg.HostedDomain != "" {
		// Only a UI hint for the account chooser; the ID token's hd claim is
		// what is enforced.
		opts = append(opts, oauth2.SetAuthURLParam("hd", p.Cfg.HostedDomain))
	}

	return p.Config.AuthCodeURL(state, opts...)
}

// ExchangeResult is the outcome of redeeming an authorization code.
type ExchangeResult struct {
	// Provider is the provider that produced the result; after a key-rotation
	// reload it is the freshly loaded one.
	Provider *OIDCProvider
	// AccessToken is the OAuth 2.0 access token. It is a credential: never log it.
	AccessToken string
	// RawIDToken, IDToken and Claims are set for OIDC providers only; Claims
	// are the verified ID token claims.
	RawIDToken string
	IDToken    *gooidc.IDToken
	Claims     map[string]any
}

// redeem exchanges the (single-use) authorization code. For OIDC providers it
// also extracts the raw ID token, which is not yet verified.
func (p *OIDCProvider) redeem(ctx context.Context, code, codeVerifier string) (*ExchangeResult, error) {
	token, err := p.Config.Exchange(oidcContext(ctx), code, oauth2.SetAuthURLParam("code_verifier", codeVerifier))
	if err != nil {
		return nil, fmt.Errorf("code exchange: %w", err)
	}

	res := &ExchangeResult{Provider: p, AccessToken: token.AccessToken}

	if p.IsOAuth2() {
		return res, nil
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, ErrIDTokenMissing
	}

	res.RawIDToken = rawIDToken

	return res, nil
}

// verifyIDToken verifies res.RawIDToken against this provider's verifier,
// applies the Entra multi-tenant checks and decodes the claims into res.
func (p *OIDCProvider) verifyIDToken(ctx context.Context, res *ExchangeResult) error {
	idToken, err := p.Verifier.Verify(oidcContext(ctx), res.RawIDToken)
	if err != nil {
		if _, expired := errors.AsType[*gooidc.TokenExpiredError](err); expired {
			return fmt.Errorf("%w: %w", ErrIDTokenExpired, err)
		}

		return fmt.Errorf("verify id token: %w", err)
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return fmt.Errorf("decode claims: %w", err)
	}

	if p.tenancy != nil {
		if err := p.tenancy.verify(idToken.Issuer, claims); err != nil {
			return err
		}
	}

	res.Provider = p
	res.IDToken = idToken
	res.Claims = claims

	return nil
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

// EndSession builds the RP-initiated logout URL (OIDC RP-Initiated Logout
// 1.0) for the IdP's end_session_endpoint. ok is false when the IdP has none,
// or when it is not an http(s) URL (the SPA navigates to the result).
func (p *OIDCProvider) EndSession(idTokenHint, postLogoutRedirectURI string) (string, bool) {
	if p.EndSessionURL == "" {
		return "", false
	}

	u, err := url.Parse(p.EndSessionURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return "", false
	}

	q := u.Query()
	q.Set("id_token_hint", idTokenHint)
	q.Set("client_id", p.Cfg.ClientID)
	q.Set("post_logout_redirect_uri", postLogoutRedirectURI)
	u.RawQuery = q.Encode()

	return u.String(), true
}

// ExchangeWithRetry redeems the authorization code exactly once. If the ID
// token fails with go-oidc's signature-verification error (possible key
// rotation), the same raw ID token is re-verified with a freshly loaded
// provider; the code is never re-exchanged. Reloads are limited to one per
// provider per oidcReloadMinInterval. Plain OAuth 2.0 providers only get the
// token exchange; their identity comes from Identity.
func ExchangeWithRetry(ctx context.Context, db database.Database, slug, baseURL, code, codeVerifier string) (*ExchangeResult, error) {
	p, err := LoadOIDCProvider(ctx, db, slug, baseURL)
	if err != nil {
		return nil, err
	}

	res, err := p.redeem(ctx, code, codeVerifier)
	if err != nil {
		return nil, err
	}

	if p.IsOAuth2() {
		return res, nil
	}

	err = p.verifyIDToken(ctx, res)
	if err == nil {
		return res, nil
	}

	if !IsUnknownKeyError(err) || !allowOIDCReload(slug) {
		return nil, err
	}

	oidcCache.CompareAndDelete(slug, p)

	p2, err2 := LoadOIDCProvider(ctx, db, slug, baseURL)
	if err2 != nil {
		return nil, err2
	}

	if err := p2.verifyIDToken(ctx, res); err != nil {
		return nil, err
	}

	return res, nil
}
