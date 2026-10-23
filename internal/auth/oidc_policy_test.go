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
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestEnsureOpenID(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"default has no groups scope", nil, []string{"openid", "email", "profile"}},
		{"openid is prepended when missing", []string{"email"}, []string{"openid", "email"}},
		{"explicit scopes are kept as-is", []string{"openid", "groups"}, []string{"openid", "groups"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ensureOpenID(tc.in); !slices.Equal(got, tc.want) {
				t.Errorf("ensureOpenID(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestEntraAuthorityDetection(t *testing.T) {
	tests := []struct {
		issuer     string
		production bool // IsEntraMultiTenantAuthority
		shape      bool // isTenantlessAuthorityPath
	}{
		{"https://login.microsoftonline.com/common/v2.0", true, true},
		{"https://login.microsoftonline.com/organizations/v2.0/", true, true},
		{"https://login.microsoftonline.com/consumers/v2.0", true, true},
		{"https://LOGIN.microsoftonline.com/common/v2.0", true, true},
		{"https://login.microsoftonline.com/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/v2.0", false, false},
		{"https://login.microsoftonline.com/common/v1.0", false, false},
		{"http://login.microsoftonline.com/common/v2.0", false, true},
		{"https://login.microsoftonline.com.evil.example/common/v2.0", false, true},
		{"https://accounts.google.com", false, false},
	}

	for _, tc := range tests {
		t.Run(tc.issuer, func(t *testing.T) {
			if got := IsEntraMultiTenantAuthority(tc.issuer); got != tc.production {
				t.Errorf("IsEntraMultiTenantAuthority = %v, want %v", got, tc.production)
			}

			if got := isTenantlessAuthorityPath(tc.issuer); got != tc.shape {
				t.Errorf("isTenantlessAuthorityPath = %v, want %v", got, tc.shape)
			}
		})
	}
}

func TestEntraTenancyVerify(t *testing.T) {
	const (
		tenantA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		tenantB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		tmpl    = "https://login.microsoftonline.com/{tenantid}/v2.0"
	)

	e := &entraTenancy{issuerTemplate: tmpl, allowed: []string{strings.ToUpper(tenantA)}}

	tests := []struct {
		name   string
		iss    string
		claims map[string]any
		deny   bool
	}{
		{"allowed tenant, case-insensitive", "https://login.microsoftonline.com/" + tenantA + "/v2.0", map[string]any{"tid": tenantA}, false},
		{"tid not allowed", "https://login.microsoftonline.com/" + tenantB + "/v2.0", map[string]any{"tid": tenantB}, true},
		{"iss tenant differs from tid", "https://login.microsoftonline.com/" + tenantB + "/v2.0", map[string]any{"tid": tenantA}, true},
		{"missing tid", "https://login.microsoftonline.com/" + tenantA + "/v2.0", map[string]any{}, true},
		{"tid is not a string", "https://login.microsoftonline.com/" + tenantA + "/v2.0", map[string]any{"tid": 12}, true},
		{"tid is not a guid", "https://login.microsoftonline.com/x/v2.0", map[string]any{"tid": "x"}, true},
		{"literal placeholder iss", tmpl, map[string]any{"tid": tenantA}, true},
		{"v1 issuer", "https://sts.windows.net/" + tenantA + "/", map[string]any{"tid": tenantA}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := e.verify(tc.iss, tc.claims)
			if tc.deny != errors.Is(err, ErrSSOLoginDenied) || (!tc.deny && err != nil) {
				t.Errorf("verify() = %v, deny=%v", err, tc.deny)
			}
		})
	}

	t.Run("empty allow-list denies everyone", func(t *testing.T) {
		none := &entraTenancy{issuerTemplate: tmpl}
		if err := none.verify("https://login.microsoftonline.com/"+tenantA+"/v2.0", map[string]any{"tid": tenantA}); !errors.Is(err, ErrSSOLoginDenied) {
			t.Errorf("verify() = %v, want ErrSSOLoginDenied", err)
		}
	})
}

// rewriteTransport sends every request to target regardless of its URL so a
// test can use the real issuer hostnames (accounts.google.com, ...).
type rewriteTransport struct{ target *url.URL }

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c := req.Clone(req.Context())
	c.URL.Scheme = r.target.Scheme
	c.URL.Host = r.target.Host

	return http.DefaultTransport.RoundTrip(c)
}

// policyIdP is an OIDC provider that advertises a fixed issuer string and
// signs with its published key.
type policyIdP struct {
	key     *rsa.PrivateKey
	issuer  string
	idToken atomic.Value // string
}

// newPolicyIdP starts an IdP reachable only through oidcHTTPClient, which is
// pointed at it for the duration of the test.
func newPolicyIdP(t *testing.T, advertisedIssuer string) *policyIdP {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	p := &policyIdP{key: key, issuer: advertisedIssuer}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                                advertisedIssuer,
				"authorization_endpoint":                "https://idp.test/authorize",
				"token_endpoint":                        "https://idp.test/token",
				"jwks_uri":                              "https://idp.test/jwks",
				"response_types_supported":              []string{"code"},
				"subject_types_supported":               []string{"public"},
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case r.URL.Path == "/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
				{Key: &key.PublicKey, KeyID: "k", Algorithm: string(jose.RS256), Use: "sig"},
			}})
		case r.URL.Path == "/token":
			tok, _ := p.idToken.Load().(string)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": tok})
		default:
			http.NotFound(w, r)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	target, _ := url.Parse(srv.URL)
	orig := oidcHTTPClient
	oidcHTTPClient = &http.Client{Transport: rewriteTransport{target: target}, Timeout: 10 * time.Second}

	t.Cleanup(func() { oidcHTTPClient = orig })

	return p
}

func (p *policyIdP) sign(t *testing.T, claims map[string]any) {
	t.Helper()

	for k, v := range map[string]any{
		"iss": p.issuer, "aud": []string{"cid"}, "sub": "subject",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	} {
		if _, ok := claims[k]; !ok {
			claims[k] = v
		}
	}

	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k"))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}

	raw, err := josejwt.Signed(sig).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	p.idToken.Store(raw)
}

// loginIdentity runs the code exchange and identity mapping for cfg.
func loginIdentity(t *testing.T, slug string, cfg models.OIDCConfig) (SSOClaims, error) {
	t.Helper()

	cfg.ClientID, cfg.ClientSecret = "cid", "secret"

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	db := &oidcRowDB{row: &models.SSOProvider{
		ID: "id-" + slug, Slug: slug, Kind: "oidc", Enabled: true, OIDCConfig: raw, UpdatedAt: time.Now(),
	}}

	res, err := ExchangeWithRetry(context.Background(), db, slug, "http://localhost", "code", "verifier")
	if err != nil {
		return SSOClaims{}, err
	}

	return res.Provider.Identity(context.Background(), res, nil)
}

func TestGoogleLoginPolicy(t *testing.T) {
	tests := []struct {
		name   string
		cfg    models.OIDCConfig
		claims map[string]any
		deny   bool
	}{
		{"verified email", models.OIDCConfig{}, map[string]any{"email": "a@example.com", "email_verified": true}, false},
		{"email_verified false is always rejected for Google", models.OIDCConfig{}, map[string]any{"email": "a@example.com", "email_verified": false}, true},
		{"email_verified absent is rejected for Google", models.OIDCConfig{}, map[string]any{"email": "a@example.com"}, true},
		{"email_verified \"false\" (string) is rejected", models.OIDCConfig{}, map[string]any{"email_verified": "false"}, true},
		{"email_verified \"true\" (string, Cognito style) is accepted", models.OIDCConfig{}, map[string]any{"email_verified": "true"}, false},
		{"hosted domain matches", models.OIDCConfig{HostedDomain: "Example.com"}, map[string]any{"email_verified": true, "hd": "EXAMPLE.com"}, false},
		{"hosted domain mismatch", models.OIDCConfig{HostedDomain: "example.com"}, map[string]any{"email_verified": true, "hd": "other.example"}, true},
		{"hosted domain claim missing (consumer account)", models.OIDCConfig{HostedDomain: "example.com"}, map[string]any{"email_verified": true}, true},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idp := newPolicyIdP(t, "https://accounts.google.com")
			tc.cfg.Issuer = "https://accounts.google.com"
			idp.sign(t, tc.claims)

			claims, err := loginIdentity(t, "google-"+string(rune('a'+i)), tc.cfg)
			if tc.deny {
				if !errors.Is(err, ErrSSOLoginDenied) {
					t.Fatalf("expected ErrSSOLoginDenied, got %v", err)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !claims.EmailVerified || claims.ExternalID != "subject" {
				t.Errorf("claims = %+v", claims)
			}
		})
	}
}

func TestEntraMultiTenantLoginOnMicrosoftHost(t *testing.T) {
	const (
		tenantA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		tenantB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		issuer  = "https://login.microsoftonline.com/organizations/v2.0"
	)

	tests := []struct {
		name    string
		claims  map[string]any
		allowed []string
		wantErr error
		wantID  string
	}{
		{"allowed tenant", map[string]any{"tid": tenantA, "iss": "https://login.microsoftonline.com/" + tenantA + "/v2.0"}, []string{tenantA}, nil, tenantA + ":subject"},
		{"tenant not allowed", map[string]any{"tid": tenantB, "iss": "https://login.microsoftonline.com/" + tenantB + "/v2.0"}, []string{tenantA}, ErrSSOLoginDenied, ""},
		{"iss/tid mismatch", map[string]any{"tid": tenantA, "iss": "https://login.microsoftonline.com/" + tenantB + "/v2.0"}, []string{tenantA, tenantB}, ErrSSOLoginDenied, ""},
		{"missing tid", map[string]any{"iss": "https://login.microsoftonline.com/" + tenantA + "/v2.0"}, []string{tenantA}, ErrSSOLoginDenied, ""},
		{"no allowed_tenants configured", map[string]any{"tid": tenantA, "iss": "https://login.microsoftonline.com/" + tenantA + "/v2.0"}, nil, ErrOIDCConfigInvalid, ""},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idp := newPolicyIdP(t, "https://login.microsoftonline.com/{tenantid}/v2.0")
			idp.sign(t, tc.claims)

			claims, err := loginIdentity(t, "entra-"+string(rune('a'+i)), models.OIDCConfig{Issuer: issuer, AllowedTenants: tc.allowed})

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}

				return
			}

			if err != nil || claims.ExternalID != tc.wantID {
				t.Fatalf("claims=%+v err=%v, want external id %q", claims, err, tc.wantID)
			}
		})
	}
}

func TestEntraDiscoveryIssuerMustBeTemplateOrConfigured(t *testing.T) {
	// A tenant-less authority whose discovery advertises some unrelated fixed
	// issuer must not be accepted: the issuer check is only relaxed for the
	// documented {tenantid} template.
	idp := newPolicyIdP(t, "https://evil.example")
	idp.sign(t, map[string]any{"tid": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"})

	_, err := loginIdentity(t, "entra-fixed", models.OIDCConfig{
		Issuer: "https://login.microsoftonline.com/common/v2.0", AllowedTenants: []string{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"},
	})
	if !errors.Is(err, ErrOIDCDiscoveryFailed) {
		t.Fatalf("expected ErrOIDCDiscoveryFailed, got %v", err)
	}
}

func TestGroupsOverage(t *testing.T) {
	tests := []struct {
		name   string
		claims map[string]any
		want   bool
	}{
		{"nothing", map[string]any{}, false},
		{"distributed claim", map[string]any{"_claim_names": map[string]any{"groups": "src1"}}, true},
		{"distributed claim for something else", map[string]any{"_claim_names": map[string]any{"roles": "src1"}}, false},
		{"hasgroups", map[string]any{"hasgroups": true}, true},
		{"hasgroups false", map[string]any{"hasgroups": false}, false},
		{"malformed _claim_names", map[string]any{"_claim_names": "groups"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := groupsOverage(tc.claims); got != tc.want {
				t.Errorf("groupsOverage = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGraphMemberObjects(t *testing.T) {
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	tests := []struct {
		name    string
		token   string
		handler func(self string, page int, w http.ResponseWriter)
		want    []string
		wantErr error
	}{
		{
			name:  "follows nextLink on the same origin",
			token: "tok",
			handler: func(self string, page int, w http.ResponseWriter) {
				if page == 1 {
					_ = json.NewEncoder(w).Encode(map[string]any{"value": []string{"a"}, "@odata.nextLink": self + "/v1.0/me/getMemberObjects?p=2"})
					return
				}

				_ = json.NewEncoder(w).Encode(map[string]any{"value": []string{"b"}})
			},
			want: []string{"a", "b"},
		},
		{
			name:  "refuses a nextLink on another origin",
			token: "tok",
			handler: func(_ string, _ int, w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(map[string]any{"value": []string{"a"}, "@odata.nextLink": "https://elsewhere.example/x"})
			},
			wantErr: errGraphForeignNext,
		},
		{
			name:    "non-200 is an error",
			token:   "tok",
			handler: func(_ string, _ int, w http.ResponseWriter) { w.WriteHeader(http.StatusForbidden) },
			wantErr: errGraphStatus,
		},
		{
			name:    "missing access token never reaches the network",
			token:   "",
			handler: func(_ string, _ int, _ http.ResponseWriter) { panic("must not be called") },
			wantErr: errGraphNoToken,
		},
		{
			name:  "endless pagination is capped",
			token: "tok",
			handler: func(self string, _ int, w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(map[string]any{"value": []string{"a"}, "@odata.nextLink": self + "/v1.0/me/getMemberObjects"})
			},
			wantErr: errGraphTooMany,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32

			var srv *httptest.Server

			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page := int(hits.Add(1))

				if tc.token != "" && r.Header.Get("Authorization") != "Bearer "+tc.token {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				tc.handler(srv.URL, page, w)
			}))
			defer srv.Close()

			got, err := graphMemberObjects(context.Background(), srv.URL, tc.token)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}

				return
			}

			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestEndSession(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     bool
	}{
		{"https endpoint", "https://idp.example/logout?tenant=1", true},
		{"no endpoint (Google-like)", "", false},
		{"javascript scheme", "javascript:alert(1)", false},
		{"relative", "/logout", false},
		{"no host", "https:///logout", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &OIDCProvider{EndSessionURL: tc.endpoint, Cfg: models.OIDCConfig{ClientID: "cid"}}

			got, ok := p.EndSession("hint.jwt", "https://oa.example/login")
			if ok != tc.want {
				t.Fatalf("ok = %v, want %v (%q)", ok, tc.want, got)
			}

			if !ok {
				return
			}

			u, err := url.Parse(got)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			q := u.Query()
			if q.Get("id_token_hint") != "hint.jwt" || q.Get("client_id") != "cid" ||
				q.Get("post_logout_redirect_uri") != "https://oa.example/login" || q.Get("tenant") != "1" {
				t.Errorf("params = %v", q)
			}
		})
	}
}

func TestAuthURL_NonceOnlyForOIDC(t *testing.T) {
	oidcP := &OIDCProvider{}
	oidcP.Config.Endpoint.AuthURL = "https://idp.example/authorize"

	oauthP := &OIDCProvider{Cfg: models.OIDCConfig{Type: models.OIDCTypeGitHub}}
	oauthP.Config.Endpoint.AuthURL = "https://idp.example/authorize"

	for name, tc := range map[string]struct {
		p         *OIDCProvider
		wantNonce bool
	}{"oidc": {oidcP, true}, "github": {oauthP, false}} {
		u, err := url.Parse(tc.p.AuthURL("st", "nn", "ch"))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}

		q := u.Query()
		if (q.Get("nonce") == "nn") != tc.wantNonce || q.Get("code_challenge") != "ch" || q.Get("state") != "st" {
			t.Errorf("%s: unexpected query %v", name, q)
		}
	}
}
