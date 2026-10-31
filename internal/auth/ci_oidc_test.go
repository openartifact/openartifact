// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
)

const ciTestAudience = "https://artifacts.example"

// ciTestIssuer is a CI system: a discovery document, a JWKS that can be
// rotated, and a signer for the tokens its jobs get.
type ciTestIssuer struct {
	srv *httptest.Server

	mu   sync.Mutex
	keys map[string]*rsa.PrivateKey // published, by key id

	discoveryHits atomic.Int32
	jwksHits      atomic.Int32
	discoveryDown atomic.Bool
}

func newCITestIssuer(t *testing.T) *ciTestIssuer {
	t.Helper()

	iss := &ciTestIssuer{keys: map[string]*rsa.PrivateKey{"k1": newRSAKey(t)}}
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		iss.discoveryHits.Add(1)

		if iss.discoveryDown.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                iss.srv.URL,
			"jwks_uri":                              iss.srv.URL + "/jwks",
			"response_types_supported":              []string{"id_token"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256", "HS256"},
		})
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		iss.jwksHits.Add(1)
		iss.mu.Lock()
		defer iss.mu.Unlock()

		set := jose.JSONWebKeySet{}
		for kid, key := range iss.keys {
			set.Keys = append(set.Keys, jose.JSONWebKey{Key: &key.PublicKey, KeyID: kid, Algorithm: string(jose.RS256), Use: "sig"})
		}

		_ = json.NewEncoder(w).Encode(set)
	})

	// The issuer listens on loopback, which the SSRF dial guard blocks.
	t.Setenv(netutil.EnvAllowPrivate, "true")

	iss.srv = httptest.NewServer(mux)
	t.Cleanup(iss.srv.Close)

	return iss
}

// rotate publishes a new key under kid and returns it.
func (i *ciTestIssuer) rotate(t *testing.T, kid string) *rsa.PrivateKey {
	t.Helper()

	key := newRSAKey(t)

	i.mu.Lock()
	i.keys[kid] = key
	i.mu.Unlock()

	return key
}

func (i *ciTestIssuer) key(kid string) *rsa.PrivateKey {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.keys[kid]
}

// ciClaims returns the claims of a healthy GitHub-style job token at now.
func (i *ciTestIssuer) ciClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss":         i.srv.URL,
		"sub":         "repo:acme/widgets:ref:refs/heads/main",
		"aud":         ciTestAudience,
		"repository":  "acme/widgets",
		"ref":         "refs/heads/main",
		"environment": "production",
		"iat":         now.Add(-time.Minute).Unix(),
		"nbf":         now.Add(-time.Minute).Unix(),
		"exp":         now.Add(10 * time.Minute).Unix(),
	}
}

func ciSign(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()

	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", jose.HeaderKey(kid)))
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}

	raw, err := josejwt.Signed(sig).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("serialize token: %v", err)
	}

	return raw
}

// ciTestStore serves policies by issuer like ListEnabledOIDCPoliciesByIssuer.
type ciTestStore struct {
	policies []*models.OIDCTrustPolicy
	err      error
}

func (s *ciTestStore) ListEnabledOIDCPoliciesByIssuer(_ context.Context, issuer string) ([]*models.OIDCTrustPolicy, error) {
	if s.err != nil {
		return nil, s.err
	}

	var out []*models.OIDCTrustPolicy

	for _, p := range s.policies {
		if p.Issuer == issuer && p.Enabled {
			out = append(out, p)
		}
	}

	return out, nil
}

func ciPolicy(issuer, name string, conditions ...models.OIDCCondition) *models.OIDCTrustPolicy {
	return &models.OIDCTrustPolicy{
		ID: "id-" + name, Name: name, Issuer: issuer, Audience: ciTestAudience, Conditions: conditions,
		ServiceAccountID: "sa-" + name, ServiceAccountName: "sa-" + name, TokenTTLSeconds: 900, Enabled: true,
	}
}

func repoIs(patterns ...string) models.OIDCCondition {
	return models.OIDCCondition{Claim: "repository", Patterns: patterns}
}

// ciTestRig is an authenticator on a controllable clock.
type ciTestRig struct {
	iss   *ciTestIssuer
	store *ciTestStore
	x     *CIOIDC
	now   atomic.Int64 // unix seconds
}

func newCITestRig(t *testing.T, policies func(issuer string) []*models.OIDCTrustPolicy) *ciTestRig {
	t.Helper()

	rig := &ciTestRig{iss: newCITestIssuer(t)}
	rig.now.Store(time.Now().Unix())
	rig.store = &ciTestStore{policies: policies(rig.iss.srv.URL)}
	rig.x = NewCIOIDC(rig.store)
	rig.x.now = func() time.Time { return time.Unix(rig.now.Load(), 0) }

	return rig
}

func (r *ciTestRig) clock() time.Time { return time.Unix(r.now.Load(), 0) }

func (r *ciTestRig) advance(d time.Duration) { r.now.Add(int64(d.Seconds())) }

func (r *ciTestRig) token(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()

	claims := r.iss.ciClaims(r.clock())
	if mutate != nil {
		mutate(claims)
	}

	return ciSign(t, r.iss.key("k1"), "k1", claims)
}

func requireDenial(t *testing.T, err error, reason string, verified bool) *CIOIDCDenialError {
	t.Helper()

	d, ok := errors.AsType[*CIOIDCDenialError](err)
	if !ok {
		t.Fatalf("err = %v, want a *CIOIDCDenialError", err)
	}

	if d.Reason != reason || d.Verified != verified {
		t.Fatalf("denial = {reason %q verified %v} (%v), want {%q %v}", d.Reason, d.Verified, d.Err, reason, verified)
	}

	return d
}

func TestCIOIDCAuthenticate_Accepts(t *testing.T) {
	rig := newCITestRig(t, func(issuer string) []*models.OIDCTrustPolicy {
		return []*models.OIDCTrustPolicy{ciPolicy(issuer, "publish", repoIs("acme/*"))}
	})

	match, err := rig.x.Authenticate(context.Background(), rig.token(t, nil), "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	if match.Policy.Name != "publish" || match.Subject != "repo:acme/widgets:ref:refs/heads/main" ||
		match.Claims["repository"] != "acme/widgets" {
		t.Fatalf("match = %+v", match)
	}
}

func TestCIOIDCAuthenticate_VerificationFailures(t *testing.T) {
	rig := newCITestRig(t, func(issuer string) []*models.OIDCTrustPolicy {
		return []*models.OIDCTrustPolicy{ciPolicy(issuer, "publish", repoIs("acme/*"))}
	})

	otherKey := newRSAKey(t)

	tests := []struct {
		name  string
		token func(t *testing.T) string
	}{
		{"expired", func(t *testing.T) string {
			t.Helper()
			return rig.token(t, func(c map[string]any) { c["exp"] = rig.clock().Add(-10 * time.Minute).Unix() })
		}},
		{"not yet valid", func(t *testing.T) string {
			t.Helper()
			return rig.token(t, func(c map[string]any) { c["nbf"] = rig.clock().Add(time.Hour).Unix() })
		}},
		{"issued in the future", func(t *testing.T) string {
			t.Helper()
			return rig.token(t, func(c map[string]any) { c["iat"] = rig.clock().Add(time.Hour).Unix() })
		}},
		{"no iat", func(t *testing.T) string {
			t.Helper()
			return rig.token(t, func(c map[string]any) { delete(c, "iat") })
		}},
		{"no exp", func(t *testing.T) string {
			t.Helper()
			return rig.token(t, func(c map[string]any) { delete(c, "exp") })
		}},
		{"signed with a key the issuer does not publish", func(t *testing.T) string {
			t.Helper()
			return ciSign(t, otherKey, "k1", rig.iss.ciClaims(rig.clock()))
		}},
		{"signed under an unknown key id", func(t *testing.T) string {
			t.Helper()
			return ciSign(t, otherKey, "nope", rig.iss.ciClaims(rig.clock()))
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rig.x.Authenticate(context.Background(), tt.token(t), "")
			requireDenial(t, err, CIOIDCInvalidToken, false)
		})
	}
}

func TestCIOIDCAuthenticate_RejectsMalformedAndSymmetricTokens(t *testing.T) {
	rig := newCITestRig(t, func(issuer string) []*models.OIDCTrustPolicy {
		return []*models.OIDCTrustPolicy{ciPolicy(issuer, "publish", repoIs("acme/*"))}
	})

	for _, raw := range []string{"", "garbage", "a.b.c", "not.a.jwt.at.all"} {
		_, err := rig.x.Authenticate(context.Background(), raw, "")
		requireDenial(t, err, CIOIDCMalformed, false)
	}

	// An HS256 token "signed" with the public key bytes must not verify, whatever
	// the discovery document claims to support.
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("0123456789abcdef0123456789abcdef")},
		(&jose.SignerOptions{}).WithHeader("kid", "k1"))
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}

	raw, err := josejwt.Signed(sig).Claims(rig.iss.ciClaims(rig.clock())).Serialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}

	_, err = rig.x.Authenticate(context.Background(), raw, "")
	requireDenial(t, err, CIOIDCInvalidToken, false)
}

func TestCIOIDCAuthenticate_PolicySelection(t *testing.T) {
	disabledAccount := ciPolicy("", "frozen", repoIs("acme/*"))
	disabledAccount.ServiceAccountDisabled = true

	tests := []struct {
		name       string
		policies   func(issuer string) []*models.OIDCTrustPolicy
		mutate     func(map[string]any)
		policyName string
		wantPolicy string
		wantReason string
	}{
		{
			name: "audience mismatch",
			policies: func(i string) []*models.OIDCTrustPolicy {
				return []*models.OIDCTrustPolicy{ciPolicy(i, "p", repoIs("acme/*"))}
			},
			mutate:     func(c map[string]any) { c["aud"] = "https://elsewhere.example" },
			wantReason: CIOIDCAudience,
		},
		{
			name: "audience as a list holds the policy's",
			policies: func(i string) []*models.OIDCTrustPolicy {
				return []*models.OIDCTrustPolicy{ciPolicy(i, "p", repoIs("acme/*"))}
			},
			mutate:     func(c map[string]any) { c["aud"] = []string{"other", ciTestAudience} },
			wantPolicy: "p",
		},
		{
			name: "repository glob mismatch",
			policies: func(i string) []*models.OIDCTrustPolicy {
				return []*models.OIDCTrustPolicy{ciPolicy(i, "p", repoIs("acme/gadgets", "other/*"))}
			},
			wantReason: CIOIDCConditions,
		},
		{
			name: "claim missing from the token",
			policies: func(i string) []*models.OIDCTrustPolicy {
				return []*models.OIDCTrustPolicy{
					ciPolicy(i, "p", repoIs("acme/*"), models.OIDCCondition{Claim: "job_workflow_ref", Patterns: []string{"*"}}),
				}
			},
			wantReason: CIOIDCConditions,
		},
		{
			name: "every condition must hold",
			policies: func(i string) []*models.OIDCTrustPolicy {
				return []*models.OIDCTrustPolicy{ciPolicy(i, "p", repoIs("acme/*"),
					models.OIDCCondition{Claim: "ref", Patterns: []string{"refs/tags/v*"}})}
			},
			wantReason: CIOIDCConditions,
		},
		{
			name: "ref and environment globs both match",
			policies: func(i string) []*models.OIDCTrustPolicy {
				return []*models.OIDCTrustPolicy{ciPolicy(i, "p", repoIs("acme/widgets"),
					models.OIDCCondition{Claim: "ref", Patterns: []string{"refs/tags/v*", "refs/heads/main"}},
					models.OIDCCondition{Claim: "environment", Patterns: []string{"prod*"}})}
			},
			wantPolicy: "p",
		},
		{
			name: "oldest matching policy wins",
			policies: func(i string) []*models.OIDCTrustPolicy {
				return []*models.OIDCTrustPolicy{
					ciPolicy(i, "no-match", repoIs("other/*")),
					ciPolicy(i, "first", repoIs("acme/*")),
					ciPolicy(i, "second", repoIs("acme/widgets")),
				}
			},
			wantPolicy: "first",
		},
		{
			name: "named policy is the only candidate",
			policies: func(i string) []*models.OIDCTrustPolicy {
				return []*models.OIDCTrustPolicy{ciPolicy(i, "first", repoIs("acme/*")), ciPolicy(i, "second", repoIs("acme/widgets"))}
			},
			policyName: "second",
			wantPolicy: "second",
		},
		{
			name: "named policy that does not exist",
			policies: func(i string) []*models.OIDCTrustPolicy {
				return []*models.OIDCTrustPolicy{ciPolicy(i, "first", repoIs("acme/*"))}
			},
			policyName: "ghost",
			wantReason: CIOIDCUnknownIssuer,
		},
		{
			name: "service account disabled",
			policies: func(i string) []*models.OIDCTrustPolicy {
				p := ciPolicy(i, "frozen", repoIs("acme/*"))
				p.ServiceAccountDisabled = true

				return []*models.OIDCTrustPolicy{p}
			},
			wantReason: CIOIDCAccountDisabled,
		},
		{
			name: "disabled account does not shadow a later policy",
			policies: func(i string) []*models.OIDCTrustPolicy {
				frozen := ciPolicy(i, "frozen", repoIs("acme/*"))
				frozen.ServiceAccountDisabled = true

				return []*models.OIDCTrustPolicy{frozen, ciPolicy(i, "live", repoIs("acme/*"))}
			},
			wantPolicy: "live",
		},
		{
			name: "disabled policy is not listed",
			policies: func(i string) []*models.OIDCTrustPolicy {
				p := ciPolicy(i, "off", repoIs("acme/*"))
				p.Enabled = false

				return []*models.OIDCTrustPolicy{p}
			},
			wantReason: CIOIDCUnknownIssuer,
		},
		{
			name: "array claim matches on any element",
			policies: func(i string) []*models.OIDCTrustPolicy {
				return []*models.OIDCTrustPolicy{ciPolicy(i, "p", models.OIDCCondition{Claim: "groups", Patterns: []string{"release-*"}})}
			},
			mutate:     func(c map[string]any) { c["groups"] = []string{"dev", "release-managers"} },
			wantPolicy: "p",
		},
		{
			name: "boolean and numeric claims match their JSON text",
			policies: func(i string) []*models.OIDCTrustPolicy {
				return []*models.OIDCTrustPolicy{ciPolicy(i, "p",
					models.OIDCCondition{Claim: "protected", Patterns: []string{"true"}},
					models.OIDCCondition{Claim: "project_id", Patterns: []string{"4242"}})}
			},
			mutate:     func(c map[string]any) { c["protected"] = true; c["project_id"] = 4242 },
			wantPolicy: "p",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newCITestRig(t, tt.policies)

			match, err := rig.x.Authenticate(context.Background(), rig.token(t, tt.mutate), tt.policyName)

			if tt.wantReason != "" {
				verified := tt.wantReason != CIOIDCUnknownIssuer
				d := requireDenial(t, err, tt.wantReason, verified)

				if verified && (d.Issuer != rig.iss.srv.URL || d.Subject == "") {
					t.Fatalf("denial of a verified token should name issuer and subject: %+v", d)
				}

				return
			}

			if err != nil {
				t.Fatalf("Authenticate: %v", err)
			}

			if match.Policy.Name != tt.wantPolicy {
				t.Fatalf("policy = %q, want %q", match.Policy.Name, tt.wantPolicy)
			}
		})
	}
}

func TestCIOIDCAuthenticate_UnknownIssuerIsNeverContacted(t *testing.T) {
	rig := newCITestRig(t, func(issuer string) []*models.OIDCTrustPolicy {
		return []*models.OIDCTrustPolicy{ciPolicy(issuer, "p", repoIs("acme/*"))}
	})

	// A token claiming an issuer no policy names: it is refused from the policy
	// list alone, nothing is fetched (a token must not make the server call
	// out), and the issuer a policy does name stays untouched too.
	stranger := newCITestIssuer(t)
	raw := ciSign(t, stranger.key("k1"), "k1", stranger.ciClaims(rig.clock()))

	_, err := rig.x.Authenticate(context.Background(), raw, "")
	requireDenial(t, err, CIOIDCUnknownIssuer, false)

	if n := stranger.discoveryHits.Load() + stranger.jwksHits.Load() + rig.iss.discoveryHits.Load(); n != 0 {
		t.Fatalf("%d outbound requests for an unknown issuer, want none", n)
	}
}

func TestCIOIDCAuthenticate_IssuerIsMatchedExactly(t *testing.T) {
	rig := newCITestRig(t, func(issuer string) []*models.OIDCTrustPolicy {
		return []*models.OIDCTrustPolicy{ciPolicy(issuer, "p", repoIs("acme/*"))}
	})

	// The token is signed by the policy's issuer's own key but names another
	// issuer, with and without a trailing slash.
	for _, iss := range []string{rig.iss.srv.URL + "/", "https://token.actions.githubusercontent.com"} {
		raw := rig.token(t, func(c map[string]any) { c["iss"] = iss })

		_, err := rig.x.Authenticate(context.Background(), raw, "")
		requireDenial(t, err, CIOIDCUnknownIssuer, false)
	}
}

func TestCIOIDCAuthenticate_StoreError(t *testing.T) {
	rig := newCITestRig(t, func(string) []*models.OIDCTrustPolicy { return nil })
	rig.store.err = context.DeadlineExceeded

	_, err := rig.x.Authenticate(context.Background(), rig.token(t, nil), "")

	if _, refused := errors.AsType[*CIOIDCDenialError](err); err == nil || refused {
		t.Fatalf("err = %v, a database failure is not a refusal of the token", err)
	}
}

func TestCIOIDCAuthenticate_KeyRotationIsFollowedAtMostOncePerInterval(t *testing.T) {
	rig := newCITestRig(t, func(issuer string) []*models.OIDCTrustPolicy {
		return []*models.OIDCTrustPolicy{ciPolicy(issuer, "p", repoIs("acme/*"))}
	})

	if _, err := rig.x.Authenticate(context.Background(), rig.token(t, nil), ""); err != nil {
		t.Fatalf("first token: %v", err)
	}

	if got := rig.iss.jwksHits.Load(); got != 1 {
		t.Fatalf("jwks fetched %d times for the first token, want 1", got)
	}

	// The issuer rotates. A token under the new key id arrives at once: the key
	// set was fetched seconds ago, so it is not fetched again.
	newKey := rig.iss.rotate(t, "k2")
	rotated := func() string { return ciSign(t, newKey, "k2", rig.iss.ciClaims(rig.clock())) }

	for range 5 {
		_, err := rig.x.Authenticate(context.Background(), rotated(), "")
		requireDenial(t, err, CIOIDCInvalidToken, false)
	}

	if got := rig.iss.jwksHits.Load(); got != 1 {
		t.Fatalf("jwks fetched %d times inside the refetch interval, want 1", got)
	}

	// Later the unknown key id triggers one fetch, and the token verifies.
	rig.advance(ciJWKSMinRefetch + time.Second)

	if _, err := rig.x.Authenticate(context.Background(), rotated(), ""); err != nil {
		t.Fatalf("rotated token after the interval: %v", err)
	}

	if got := rig.iss.jwksHits.Load(); got != 2 {
		t.Fatalf("jwks fetched %d times, want 2", got)
	}

	// Tokens under the old key keep verifying from the cache.
	if _, err := rig.x.Authenticate(context.Background(), rig.token(t, nil), ""); err != nil {
		t.Fatalf("token under the old key: %v", err)
	}

	if got := rig.iss.jwksHits.Load(); got != 2 {
		t.Fatalf("jwks fetched %d times, want 2", got)
	}
}

func TestCIOIDCAuthenticate_DiscoveryIsCachedAndRefreshed(t *testing.T) {
	rig := newCITestRig(t, func(issuer string) []*models.OIDCTrustPolicy {
		return []*models.OIDCTrustPolicy{ciPolicy(issuer, "p", repoIs("acme/*"))}
	})

	for range 3 {
		if _, err := rig.x.Authenticate(context.Background(), rig.token(t, nil), ""); err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
	}

	if got := rig.iss.discoveryHits.Load(); got != 1 {
		t.Fatalf("discovery fetched %d times for 3 tokens, want 1", got)
	}

	// The issuer withdraws k1 and publishes k9. Tokens under k1 keep verifying
	// from the cache for up to ciIssuerTTL, then the issuer is discovered again
	// and the withdrawn key is gone.
	k1 := rig.iss.key("k1")
	k9 := rig.iss.rotate(t, "k9")

	rig.iss.mu.Lock()
	delete(rig.iss.keys, "k1")
	rig.iss.mu.Unlock()

	if _, err := rig.x.Authenticate(context.Background(), ciSign(t, k1, "k1", rig.iss.ciClaims(rig.clock())), ""); err != nil {
		t.Fatalf("withdrawn key inside the cache lifetime: %v", err)
	}

	rig.advance(ciIssuerTTL + time.Second)

	_, err := rig.x.Authenticate(context.Background(), ciSign(t, k1, "k1", rig.iss.ciClaims(rig.clock())), "")
	requireDenial(t, err, CIOIDCInvalidToken, false)

	if _, err := rig.x.Authenticate(context.Background(), ciSign(t, k9, "k9", rig.iss.ciClaims(rig.clock())), ""); err != nil {
		t.Fatalf("new key after the refresh: %v", err)
	}

	if got := rig.iss.discoveryHits.Load(); got != 2 {
		t.Fatalf("discovery fetched %d times, want 2", got)
	}
}

func TestCIOIDCAuthenticate_UnreachableIssuerIsRemembered(t *testing.T) {
	rig := newCITestRig(t, func(issuer string) []*models.OIDCTrustPolicy {
		return []*models.OIDCTrustPolicy{ciPolicy(issuer, "p", repoIs("acme/*"))}
	})
	rig.iss.discoveryDown.Store(true)

	for range 4 {
		_, err := rig.x.Authenticate(context.Background(), rig.token(t, nil), "")
		requireDenial(t, err, CIOIDCIssuerUnreachable, false)
	}

	if got := rig.iss.discoveryHits.Load(); got != 1 {
		t.Fatalf("discovery attempted %d times while down, want 1", got)
	}

	// Once the retry delay passed and the issuer is back, tokens verify again.
	rig.iss.discoveryDown.Store(false)
	rig.advance(ciIssuerRetry + time.Second)

	if _, err := rig.x.Authenticate(context.Background(), rig.token(t, nil), ""); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
}

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern, s string
		want       bool
	}{
		{"acme/widgets", "acme/widgets", true},
		{"acme/widgets", "acme/widgets2", false},
		{"acme/widgets", "Acme/widgets", false},
		{"acme/*", "acme/widgets", true},
		{"acme/*", "acme/", true},
		{"acme/*", "acmex/widgets", false},
		{"refs/heads/*", "refs/heads/release/1.2", true},
		{"refs/tags/v*", "refs/heads/v1", false},
		{"*", "anything", true},
		{"*/widgets", "acme/widgets", true},
		{"*/widgets", "acme/widgets-old", false},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		{"a*b*c", "abcbc", true},
		{"v1.?", "v1.2", true},
		{"v1.?", "v1.22", false},
		{"v1.?", "v1.", false},
		{"é?", "éa", true},
		{"a**b", "ab", true},
		{"", "", true},
		{"", "x", false},
	}

	for _, tt := range tests {
		if got := GlobMatch(tt.pattern, tt.s); got != tt.want {
			t.Errorf("GlobMatch(%q, %q) = %v, want %v", tt.pattern, tt.s, got, tt.want)
		}
	}
}

func TestConditionsMatch_NoConditionsNeverMatch(t *testing.T) {
	if ConditionsMatch(nil, map[string]any{"repository": "acme/widgets"}) {
		t.Fatal("a policy without conditions must not trust every token")
	}
}

func TestValidateOIDCPolicy(t *testing.T) {
	valid := func() *models.OIDCTrustPolicy {
		return &models.OIDCTrustPolicy{
			Name: "publish", Issuer: "https://token.actions.githubusercontent.com", Audience: "https://oa.example",
			Conditions: []models.OIDCCondition{{Claim: "repository", Patterns: []string{"acme/*"}}}, TokenTTLSeconds: 600,
		}
	}

	tests := []struct {
		name    string
		mutate  func(p *models.OIDCTrustPolicy)
		wantErr bool
	}{
		{"valid", func(*models.OIDCTrustPolicy) {}, false},
		{"default ttl", func(p *models.OIDCTrustPolicy) { p.TokenTTLSeconds = 0 }, false},
		{"ttl above one hour", func(p *models.OIDCTrustPolicy) { p.TokenTTLSeconds = 3601 }, true},
		{"ttl below a minute", func(p *models.OIDCTrustPolicy) { p.TokenTTLSeconds = 59 }, true},
		{"negative ttl", func(p *models.OIDCTrustPolicy) { p.TokenTTLSeconds = -5 }, true},
		{"bad name", func(p *models.OIDCTrustPolicy) { p.Name = "-x y" }, true},
		{"no audience", func(p *models.OIDCTrustPolicy) { p.Audience = "" }, true},
		{"no conditions", func(p *models.OIDCTrustPolicy) { p.Conditions = nil }, true},
		{"empty patterns", func(p *models.OIDCTrustPolicy) { p.Conditions[0].Patterns = nil }, true},
		{"only wildcards", func(p *models.OIDCTrustPolicy) { p.Conditions[0].Patterns = []string{"*"} }, true},
		{"issuer not a url", func(p *models.OIDCTrustPolicy) { p.Issuer = "token.actions" }, true},
		{"issuer with a query", func(p *models.OIDCTrustPolicy) { p.Issuer = "https://token.actions.githubusercontent.com?x=1" }, true},
		{"issuer with credentials", func(p *models.OIDCTrustPolicy) { p.Issuer = "https://u:p@token.actions.githubusercontent.com" }, true},
		{"issuer over ftp", func(p *models.OIDCTrustPolicy) { p.Issuer = "ftp://token.actions.githubusercontent.com" }, true},
		{"shared issuer needs an identity condition", func(p *models.OIDCTrustPolicy) {
			p.Conditions = []models.OIDCCondition{{Claim: "ref", Patterns: []string{"refs/heads/main"}}}
		}, true},
		{"shared issuer, identity pattern starting with a wildcard", func(p *models.OIDCTrustPolicy) {
			p.Conditions = []models.OIDCCondition{{Claim: "repository", Patterns: []string{"*/widgets"}}}
		}, true},
		{"shared issuer, owner claim is an identity claim", func(p *models.OIDCTrustPolicy) {
			p.Conditions = []models.OIDCCondition{
				{Claim: "repository_owner", Patterns: []string{"acme"}},
				{Claim: "ref", Patterns: []string{"refs/*"}},
			}
		}, false},
		{"gitlab.com needs a project condition", func(p *models.OIDCTrustPolicy) {
			p.Issuer = "https://gitlab.com"
			p.Conditions = []models.OIDCCondition{{Claim: "ref", Patterns: []string{"main"}}}
		}, true},
		{"gitlab.com with project_path", func(p *models.OIDCTrustPolicy) {
			p.Issuer = "https://gitlab.com"
			p.Conditions = []models.OIDCCondition{{Claim: "project_path", Patterns: []string{"acme/widgets"}}}
		}, false},
		{"private issuer may constrain any claim", func(p *models.OIDCTrustPolicy) {
			p.Issuer = "https://gitlab.acme.example"
			p.Conditions = []models.OIDCCondition{{Claim: "ref", Patterns: []string{"main"}}}
		}, false},
		{"duplicate claim", func(p *models.OIDCTrustPolicy) {
			p.Conditions = append(p.Conditions, models.OIDCCondition{Claim: "repository", Patterns: []string{"acme/other"}})
		}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := valid()
			tt.mutate(p)

			err := ValidateOIDCPolicy(p)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateOIDCPolicy = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}

	p := valid()
	p.TokenTTLSeconds = 0

	if err := ValidateOIDCPolicy(p); err != nil || p.TokenTTLSeconds != OIDCPolicyDefaultTTL {
		t.Fatalf("default ttl: err %v, ttl %d", err, p.TokenTTLSeconds)
	}
}
