// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

type oidcRowDB struct {
	database.BaseMockDB

	row *models.SSOProvider
}

func (d *oidcRowDB) GetSSOProviderBySlug(_ context.Context, _ string) (*models.SSOProvider, error) {
	return d.row, nil
}

// fakeIdP is a minimal OIDC provider. Its token endpoint returns an ID token
// signed with a key that is NOT in the published JWKS, so verification always
// fails with go-oidc's signature error.
type fakeIdP struct {
	srv           *httptest.Server
	discoveryHits atomic.Int32
	tokenHits     atomic.Int32
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()

	published, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	rogue, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	f := &fakeIdP{}
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		f.discoveryHits.Add(1)

		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.srv.URL,
			"authorization_endpoint":                f.srv.URL + "/auth",
			"token_endpoint":                        f.srv.URL + "/token",
			"jwks_uri":                              f.srv.URL + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &published.PublicKey, KeyID: "k1", Algorithm: string(jose.RS256), Use: "sig"},
		}})
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		f.tokenHits.Add(1)

		sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: rogue},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		raw, err := josejwt.Signed(sig).Claims(map[string]any{
			"iss": f.srv.URL, "aud": []string{"cid"}, "sub": "u1",
			"iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(),
		}).Serialize()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// oauth2 picks the parser from the content type; without it the body
		// is treated as a form and the access token is "missing".
		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": raw})
	})

	// The fake IdP listens on loopback, which the SSRF dial guard blocks.
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	return f
}

func exchangeErr(t *testing.T, db database.Database, code string) error {
	t.Helper()

	res, err := ExchangeWithRetry(context.Background(), db, "oidc-retry", "http://localhost", code, "verifier")
	if res != nil {
		t.Fatalf("no result may be returned on failure, got %v", res)
	}

	return err
}

func oidcRow(t *testing.T, slug, issuer, secret string, enabled bool) *models.SSOProvider {
	t.Helper()

	cfg, err := json.Marshal(models.OIDCConfig{Issuer: issuer, ClientID: "cid", ClientSecret: secret})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	return &models.SSOProvider{
		ID: "id-" + slug, Slug: slug, Kind: "oidc", Enabled: enabled,
		OIDCConfig: cfg, UpdatedAt: time.Now(), CreatedAt: time.Now(),
	}
}

func TestLoadOIDCProvider_DisabledRejected(t *testing.T) {
	db := &oidcRowDB{row: oidcRow(t, "oidc-disabled", "http://127.0.0.1:1", "s", false)}

	_, err := LoadOIDCProvider(context.Background(), db, "oidc-disabled", "http://localhost")
	if !errors.Is(err, ErrSSOProviderDisabled) {
		t.Fatalf("expected ErrSSOProviderDisabled, got %v", err)
	}
}

func TestLoadOIDCProvider_UnknownProvider(t *testing.T) {
	_, err := LoadOIDCProvider(context.Background(), &oidcRowDB{}, "nope", "http://localhost")
	if !errors.Is(err, ErrSSOProviderNotFound) {
		t.Fatalf("expected ErrSSOProviderNotFound, got %v", err)
	}
}

func TestLoadOIDCProviderRow_DecryptsStoredSecret(t *testing.T) {
	if err := crypto.Init("test-secret-key-0123456789"); err != nil {
		t.Fatalf("crypto.Init: %v", err)
	}

	t.Cleanup(func() { _ = crypto.Init("") })

	enc, err := crypto.Encrypt("the-client-secret")
	if err != nil || !crypto.IsEncrypted(enc) {
		t.Fatalf("expected encrypted value, got %q (err %v)", enc, err)
	}

	idp := newFakeIdP(t)

	p, err := LoadOIDCProviderRow(context.Background(), oidcRow(t, "oidc-enc", idp.srv.URL, enc, true), "http://localhost")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if p.Config.ClientSecret != "the-client-secret" {
		t.Errorf("oauth2 client secret must be the decrypted value, got %q", p.Config.ClientSecret)
	}
}

func TestLoadOIDCProviderRow_UndecryptableSecretIsAnError(t *testing.T) {
	if err := crypto.Init(""); err != nil {
		t.Fatalf("crypto.Init: %v", err)
	}

	idp := newFakeIdP(t)

	_, err := LoadOIDCProviderRow(context.Background(), oidcRow(t, "oidc-nokey", idp.srv.URL, "enc:v1:AAAAAAAAAAAAAAAAAAAAAAAA", true), "http://localhost")
	if !errors.Is(err, ErrOIDCSecretUnusable) {
		t.Fatalf("expected ErrOIDCSecretUnusable (no plaintext fallback), got %v", err)
	}

	if idp.discoveryHits.Load() != 0 {
		t.Error("must not contact the IdP with an unusable client secret")
	}
}

func TestLoadOIDCProviderRow_LegacyPlaintextSecretStillLoads(t *testing.T) {
	idp := newFakeIdP(t)

	p, err := LoadOIDCProviderRow(context.Background(), oidcRow(t, "oidc-legacy", idp.srv.URL, "plain-legacy", true), "http://localhost")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if p.Config.ClientSecret != "plain-legacy" {
		t.Errorf("legacy plaintext secret must be used as-is, got %q", p.Config.ClientSecret)
	}
}

func TestLoadOIDCProviderRow_ChangedSecretNeverServedStale(t *testing.T) {
	idp := newFakeIdP(t)
	row := oidcRow(t, "oidc-stale", idp.srv.URL, "first", true)

	p1, err := LoadOIDCProviderRow(context.Background(), row, "http://localhost")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// Same UpdatedAt, different stored config.
	row2 := oidcRow(t, "oidc-stale", idp.srv.URL, "second", true)
	row2.UpdatedAt = row.UpdatedAt

	p2, err := LoadOIDCProviderRow(context.Background(), row2, "http://localhost")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if p1.Config.ClientSecret != "first" || p2.Config.ClientSecret != "second" {
		t.Errorf("stale provider served: %q then %q", p1.Config.ClientSecret, p2.Config.ClientSecret)
	}
}

func TestIsUnknownKeyError_OnlyMatchesGoOIDCSignatureFailure(t *testing.T) {
	yes := []error{
		errors.New("verify id token: failed to verify signature: failed to verify id token signature"),
		errors.New("failed to verify id token signature"),
	}
	no := []error{
		nil,
		errors.New("verify id token: oidc: token is expired (Token Expiry: 2020-01-01 00:00:00 +0000 UTC)"),
		errors.New("verify id token: oidc: expected audience \"x\" got [\"y\"]"),
		errors.New("verify id token: failed to verify id token"),
		errors.New("unknown kid"),
		errors.New("key not found"),
	}

	for _, err := range yes {
		if !IsUnknownKeyError(err) {
			t.Errorf("expected match for %v", err)
		}
	}

	for _, err := range no {
		if IsUnknownKeyError(err) {
			t.Errorf("unexpected match for %v", err)
		}
	}
}

func TestAllowOIDCReload_RateLimitedPerSlug(t *testing.T) {
	slug := "reload-limit-" + time.Now().Format(time.RFC3339Nano)

	if !allowOIDCReload(slug) {
		t.Fatal("first reload must be allowed")
	}

	if allowOIDCReload(slug) {
		t.Error("second reload within the minimum interval must be denied")
	}

	if !allowOIDCReload(slug + "-other") {
		t.Error("another provider must have its own budget")
	}
}

func TestExchangeWithRetry_RedeemsCodeOnceAndBoundsReloads(t *testing.T) {
	idp := newFakeIdP(t)
	db := &oidcRowDB{row: oidcRow(t, "oidc-retry", idp.srv.URL, "s", true)}

	err := exchangeErr(t, db, "code")
	if err == nil || !IsUnknownKeyError(err) {
		t.Fatalf("expected a signature verification error, got %v", err)
	}

	if got := idp.tokenHits.Load(); got != 1 {
		t.Errorf("the single-use authorization code must be redeemed exactly once, token endpoint hit %d times", got)
	}

	// One discovery for the initial load plus exactly one forced reload.
	if got := idp.discoveryHits.Load(); got != 2 {
		t.Errorf("expected 2 discovery fetches (load + one bounded reload), got %d", got)
	}

	// A second failure inside the minimum interval must not reload again.
	err = exchangeErr(t, db, "code2")
	if err == nil {
		t.Fatal("expected an error")
	}

	if got := idp.discoveryHits.Load(); got != 2 {
		t.Errorf("reload must be rate limited to once per minute per provider, discovery hits: %d", got)
	}

	if got := idp.tokenHits.Load(); got != 2 {
		t.Errorf("each callback redeems its own code once, token endpoint hit %d times", got)
	}
}

func TestOIDCDiscoveryBlockedForPrivateIssuer(t *testing.T) {
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "")

	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	_, err := gooidc.NewProvider(oidcContext(context.Background()), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("discovery against loopback issuer must hit the dial guard, got %v", err)
	}
}
