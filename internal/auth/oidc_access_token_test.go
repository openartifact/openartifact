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
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	accessTokenResource = "https://artifacts.example/mcp"
	accessTokenBaseURL  = "http://localhost"
	accessTokenSubject  = "idp-subject-1"
	accessTokenKeyID    = "access-k1"
)

var errAccessTokenDB = errors.New("db unavailable")

// accessTokenDB serves the SSO provider rows and the subject-to-user links the
// access token verifier reads. Disabled providers are filtered out like the
// real ListEnabledSSOProviders does.
type accessTokenDB struct {
	database.BaseMockDB

	providers []*models.SSOProvider
	users     map[string]*models.User // key: slug + "|" + subject
	listErr   error
	listCalls atomic.Int32
}

func (d *accessTokenDB) ListEnabledSSOProviders(_ context.Context) ([]*models.SSOProvider, error) {
	d.listCalls.Add(1)

	if d.listErr != nil {
		return nil, d.listErr
	}

	var enabled []*models.SSOProvider

	for _, p := range d.providers {
		if p.Enabled {
			enabled = append(enabled, p)
		}
	}

	return enabled, nil
}

func (d *accessTokenDB) GetUserByExternalID(_ context.Context, externalID, providerSlug string) (*models.User, error) {
	return d.users[providerSlug+"|"+externalID], nil
}

// accessTokenIdP is an OIDC provider whose published JWKS key is the one that
// signs the tokens the tests mint.
type accessTokenIdP struct {
	srv           *httptest.Server
	key           *rsa.PrivateKey
	discoveryHits atomic.Int32
}

func newAccessTokenIdP(t *testing.T) *accessTokenIdP {
	t.Helper()

	key := newRSAKey(t)
	idp := &accessTokenIdP{key: key}
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		idp.discoveryHits.Add(1)

		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.srv.URL,
			"authorization_endpoint":                idp.srv.URL + "/auth",
			"token_endpoint":                        idp.srv.URL + "/token",
			"jwks_uri":                              idp.srv.URL + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &key.PublicKey, KeyID: accessTokenKeyID, Algorithm: string(jose.RS256), Use: "sig"},
		}})
	})

	// The IdP listens on loopback, which the SSRF dial guard blocks.
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)

	return idp
}

func newRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	return key
}

// mintAccessToken signs a JWT access token with key under the published key ID.
func mintAccessToken(t *testing.T, key *rsa.PrivateKey, iss string, aud []string, expiresIn time.Duration) string {
	t.Helper()

	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", accessTokenKeyID))
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}

	now := time.Now()

	raw, err := josejwt.Signed(sig).Claims(map[string]any{
		"iss": iss, "sub": accessTokenSubject, "aud": aud,
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(expiresIn).Unix(),
	}).Serialize()
	if err != nil {
		t.Fatalf("serialize token: %v", err)
	}

	return raw
}

type accessTokenCase struct {
	name string
	// Token shape.
	aud       []string
	iss       string // empty = the IdP's issuer
	expiresIn time.Duration
	rogueKey  bool // sign with a key the IdP does not publish
	// Server-side state.
	user             *models.User // linked to the subject; nil = no linked user
	providerDisabled bool
	// Expectation.
	wantUserID string // empty = must be rejected with ErrAccessTokenNotAccepted
}

func accessTokenCases() []accessTokenCase {
	active := &models.User{ID: "user-1", Username: "alice"}
	validExp := time.Hour

	return []accessTokenCase{
		{name: "valid", aud: []string{accessTokenResource}, expiresIn: validExp, user: active, wantUserID: "user-1"},
		{name: "valid among several audiences", aud: []string{"other", accessTokenResource}, expiresIn: validExp, user: active, wantUserID: "user-1"},
		{name: "trailing slash difference accepted", aud: []string{accessTokenResource + "/"}, expiresIn: validExp, user: active, wantUserID: "user-1"},
		{name: "wrong audience", aud: []string{"https://other.example/mcp"}, expiresIn: validExp, user: active},
		{name: "id token style client id audience", aud: []string{"cid"}, expiresIn: validExp, user: active},
		{name: "unknown issuer", aud: []string{accessTokenResource}, iss: "https://unknown.example", expiresIn: validExp, user: active},
		{name: "expired", aud: []string{accessTokenResource}, expiresIn: -time.Hour, user: active},
		{name: "signed by a different key", aud: []string{accessTokenResource}, expiresIn: validExp, rogueKey: true, user: active},
		{name: "subject without linked user", aud: []string{accessTokenResource}, expiresIn: validExp},
		{name: "disabled user", aud: []string{accessTokenResource}, expiresIn: validExp, user: &models.User{ID: "user-2", Username: "bob", Disabled: true}},
		{name: "disabled provider", aud: []string{accessTokenResource}, expiresIn: validExp, user: active, providerDisabled: true},
	}
}

func TestVerifyOIDCAccessToken(t *testing.T) {
	for i, tc := range accessTokenCases() {
		t.Run(tc.name, func(t *testing.T) {
			runAccessTokenCase(t, "oat-verify-"+string(rune('a'+i)), tc)
		})
	}
}

func runAccessTokenCase(t *testing.T, slug string, tc accessTokenCase) {
	t.Helper()

	idp := newAccessTokenIdP(t)

	row := oidcRow(t, slug, idp.srv.URL, "", !tc.providerDisabled)
	db := &accessTokenDB{providers: []*models.SSOProvider{row}, users: map[string]*models.User{}}

	if tc.user != nil {
		db.users[slug+"|"+accessTokenSubject] = tc.user
	}

	signer := idp.key
	if tc.rogueKey {
		signer = newRSAKey(t)
	}

	iss := tc.iss
	if iss == "" {
		iss = idp.srv.URL
	}

	raw := mintAccessToken(t, signer, iss, tc.aud, tc.expiresIn)

	user, err := VerifyOIDCAccessToken(context.Background(), db, accessTokenBaseURL, raw, accessTokenResource)

	assertAccessTokenResult(t, tc.wantUserID, user, err)
}

func assertAccessTokenResult(t *testing.T, wantUserID string, user *models.User, err error) {
	t.Helper()

	if wantUserID == "" {
		if !errors.Is(err, ErrAccessTokenNotAccepted) {
			t.Fatalf("expected ErrAccessTokenNotAccepted, got user=%v err=%v", user, err)
		}

		if user != nil {
			t.Fatalf("a rejected token must not return a user, got %v", user)
		}

		return
	}

	if err != nil {
		t.Fatalf("expected the token to be accepted, got %v", err)
	}

	if user == nil || user.ID != wantUserID {
		t.Fatalf("expected user %q, got %v", wantUserID, user)
	}
}

func TestVerifyOIDCAccessToken_NonJWTRejectedWithoutContactingIdP(t *testing.T) {
	idp := newAccessTokenIdP(t)
	db := &accessTokenDB{
		providers: []*models.SSOProvider{oidcRow(t, "oat-nonjwt", idp.srv.URL, "", true)},
		users:     map[string]*models.User{"oat-nonjwt|" + accessTokenSubject: {ID: "user-1", Username: "alice"}},
	}

	for _, raw := range []string{"oa_pat_x", "garbage", "", "a.b", "a.b.c", "a.!!!.c"} {
		user, err := VerifyOIDCAccessToken(context.Background(), db, accessTokenBaseURL, raw, accessTokenResource)
		if !errors.Is(err, ErrAccessTokenNotAccepted) || user != nil {
			t.Errorf("token %q: expected ErrAccessTokenNotAccepted, got user=%v err=%v", raw, user, err)
		}
	}

	if hits := idp.discoveryHits.Load(); hits != 0 {
		t.Errorf("non-JWT credentials must not reach the IdP, got %d discovery requests", hits)
	}
}

func TestVerifyOIDCAccessToken_ProviderListErrorIsNotAcceptedSentinel(t *testing.T) {
	idp := newAccessTokenIdP(t)
	db := &accessTokenDB{listErr: errAccessTokenDB}
	raw := mintAccessToken(t, idp.key, idp.srv.URL, []string{accessTokenResource}, time.Hour)

	_, err := VerifyOIDCAccessToken(context.Background(), db, accessTokenBaseURL, raw, accessTokenResource)
	if !errors.Is(err, errAccessTokenDB) {
		t.Fatalf("expected the database error, got %v", err)
	}

	if errors.Is(err, ErrAccessTokenNotAccepted) {
		t.Errorf("an infrastructure failure must not look like a rejected credential: %v", err)
	}
}

func samlRow(slug string) *models.SSOProvider {
	return &models.SSOProvider{ID: "id-" + slug, Slug: slug, Kind: "saml", Enabled: true}
}

func TestOIDCIssuers(t *testing.T) {
	const (
		issuerA = "https://idp-a.example"
		issuerB = "https://idp-b.example"
	)

	samlWithOIDCConfig := samlRow("oat-iss-saml")
	samlWithOIDCConfig.OIDCConfig = oidcRow(t, "x", "https://saml-leak.example", "", true).OIDCConfig

	cases := []struct {
		name string
		rows []*models.SSOProvider
		want []string
	}{
		{name: "no providers", rows: nil, want: nil},
		{
			name: "enabled oidc issuers",
			rows: []*models.SSOProvider{oidcRow(t, "oat-iss-a", issuerA, "", true), oidcRow(t, "oat-iss-b", issuerB, "", true)},
			want: []string{issuerA, issuerB},
		},
		{
			name: "duplicate issuers collapse",
			rows: []*models.SSOProvider{oidcRow(t, "oat-iss-a1", issuerA, "", true), oidcRow(t, "oat-iss-a2", issuerA, "", true), oidcRow(t, "oat-iss-b1", issuerB, "", true)},
			want: []string{issuerA, issuerB},
		},
		{
			name: "disabled oidc providers are ignored",
			rows: []*models.SSOProvider{oidcRow(t, "oat-iss-on", issuerA, "", true), oidcRow(t, "oat-iss-off", issuerB, "", false)},
			want: []string{issuerA},
		},
		{
			name: "saml providers are ignored",
			rows: []*models.SSOProvider{samlRow("oat-iss-saml1"), samlWithOIDCConfig, oidcRow(t, "oat-iss-c", issuerA, "", true)},
			want: []string{issuerA},
		},
		{
			name: "only saml providers",
			rows: []*models.SSOProvider{samlRow("oat-iss-saml2")},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OIDCIssuers(context.Background(), &accessTokenDB{providers: tc.rows})
			if err != nil {
				t.Fatalf("OIDCIssuers: %v", err)
			}

			if len(got) == 0 && len(tc.want) == 0 {
				return
			}

			if !slices.Equal(got, tc.want) {
				t.Errorf("issuers = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOIDCIssuers_ListErrorPropagates(t *testing.T) {
	_, err := OIDCIssuers(context.Background(), &accessTokenDB{listErr: errAccessTokenDB})
	if !errors.Is(err, errAccessTokenDB) {
		t.Fatalf("expected the database error, got %v", err)
	}
}
