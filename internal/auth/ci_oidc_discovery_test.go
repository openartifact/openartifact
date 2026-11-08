// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
)

// k8sTestIssuer is a cluster API server: an https issuer on loopback, signed by
// a CA of its own, that answers only requests bearing the service account token.
type k8sTestIssuer struct {
	srv *httptest.Server

	mu        sync.Mutex
	keys      map[string]*rsa.PrivateKey
	wantToken string
	bearers   []string // the Authorization header of every request, in order

	hits atomic.Int32
}

func newK8sTestIssuer(t *testing.T, wantToken string) *k8sTestIssuer {
	t.Helper()

	iss := &k8sTestIssuer{keys: map[string]*rsa.PrivateKey{"k1": newRSAKey(t)}, wantToken: wantToken}
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if !iss.admit(w, r) {
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                iss.srv.URL,
			"jwks_uri":                              iss.srv.URL + "/openid/v1/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})

	mux.HandleFunc("/openid/v1/jwks", func(w http.ResponseWriter, r *http.Request) {
		if !iss.admit(w, r) {
			return
		}

		iss.mu.Lock()
		defer iss.mu.Unlock()

		set := jose.JSONWebKeySet{}
		for kid, key := range iss.keys {
			set.Keys = append(set.Keys, jose.JSONWebKey{Key: &key.PublicKey, KeyID: kid, Algorithm: string(jose.RS256), Use: "sig"})
		}

		_ = json.NewEncoder(w).Encode(set)
	})

	iss.srv = httptest.NewUnstartedServer(mux)
	iss.srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshakes of the CA cases
	iss.srv.StartTLS()
	t.Cleanup(iss.srv.Close)

	return iss
}

// admit records the request and refuses it unless it carries the token.
func (i *k8sTestIssuer) admit(w http.ResponseWriter, r *http.Request) bool {
	i.hits.Add(1)

	i.mu.Lock()
	defer i.mu.Unlock()

	i.bearers = append(i.bearers, r.Header.Get("Authorization"))

	if r.Header.Get("Authorization") != "Bearer "+i.wantToken {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}

	return true
}

func (i *k8sTestIssuer) seenBearers() []string {
	i.mu.Lock()
	defer i.mu.Unlock()

	return slices.Clone(i.bearers)
}

func (i *k8sTestIssuer) setToken(token string) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.wantToken = token
}

func (i *k8sTestIssuer) rotate(t *testing.T, kid string) *rsa.PrivateKey {
	t.Helper()

	key := newRSAKey(t)

	i.mu.Lock()
	i.keys[kid] = key
	i.mu.Unlock()

	return key
}

// caFile writes the server's certificate as a PEM bundle.
func (i *k8sTestIssuer) caFile(t *testing.T) string {
	t.Helper()

	return writeTestFile(t, "ca.crt", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: i.srv.Certificate().Raw})))
}

// podClaims is what a pod's projected service account token carries.
func (i *k8sTestIssuer) podClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss": i.srv.URL,
		"sub": "system:serviceaccount:team-a:build-pipeline-7",
		"aud": ciTestAudience,
		"iat": now.Add(-time.Minute).Unix(),
		"nbf": now.Add(-time.Minute).Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"kubernetes.io": map[string]any{
			"namespace":      "team-a",
			"serviceaccount": map[string]any{"name": "build-pipeline-7", "uid": "u-1"},
			"pod":            map[string]any{"name": "build-abc-pod"},
		},
	}
}

// unrelatedCAFile is the PEM bundle of a CA that signed nothing the tests serve
// (every httptest TLS server shares one certificate).
func unrelatedCAFile(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "unrelated CA"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	return writeTestFile(t, "other-ca.crt", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
}

func writeTestFile(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}

	return path
}

// k8sPolicy trusts the pods of namespace team-a through the given discovery.
func k8sPolicy(issuer, mode string, private bool) *models.OIDCTrustPolicy {
	p := ciPolicy(issuer, "tekton", models.OIDCCondition{Claim: "kubernetes.io.namespace", Patterns: []string{"team-a"}})
	p.Discovery.Mode = mode
	p.AllowPrivateIssuer = private

	return p
}

// authenticator is an authenticator over policies on a controllable clock.
func authenticator(policies ...*models.OIDCTrustPolicy) (*CIOIDC, *atomic.Int64) {
	var now atomic.Int64

	now.Store(time.Now().Unix())

	x := NewCIOIDC(&ciTestStore{policies: policies})
	x.now = func() time.Time { return time.Unix(now.Load(), 0) }

	return x, &now
}

// The CA bundle a case hands to the server.
const (
	clusterCA = "cluster" // the one that signed the server certificate
	otherCA   = "other"   // a CA that signed nothing
	noCA      = "none"    // no bundle configured
	missingCA = "missing" // a path that does not exist
)

func caFileFor(t *testing.T, which string, iss *k8sTestIssuer) string {
	t.Helper()

	switch which {
	case clusterCA:
		return iss.caFile(t)
	case otherCA:
		return unrelatedCAFile(t)
	case missingCA:
		return filepath.Join(t.TempDir(), "missing")
	default:
		return ""
	}
}

func TestCIOIDCServiceAccountDiscovery(t *testing.T) {
	t.Setenv(netutil.EnvAllowPrivate, "false")

	const saToken = "projected-token-1"

	t.Run("reads the issuer with the bearer token, trusting the cluster CA", func(t *testing.T) {
		iss := newK8sTestIssuer(t, saToken)
		x, _ := authenticator(k8sPolicy(iss.srv.URL, models.OIDCDiscoveryServiceAccount, true))
		x.WithServiceAccountFiles(writeTestFile(t, "token", saToken+"\n"), iss.caFile(t))

		raw := ciSign(t, iss.keys["k1"], "k1", iss.podClaims(time.Now()))

		match, err := x.Authenticate(context.Background(), raw, "")
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}

		if match.Policy.Name != "tekton" || match.Subject != "system:serviceaccount:team-a:build-pipeline-7" {
			t.Fatalf("match = %s / %s", match.Policy.Name, match.Subject)
		}

		// The discovery document and the key set were both fetched with the token.
		want := []string{"Bearer " + saToken, "Bearer " + saToken}
		if got := iss.seenBearers(); !slices.Equal(got, want) {
			t.Fatalf("Authorization headers = %q, want %q", got, want)
		}
	})

	t.Run("the rotated token is presented when keys are refetched", func(t *testing.T) {
		iss := newK8sTestIssuer(t, saToken)
		tokenFile := writeTestFile(t, "token", saToken)
		x, now := authenticator(k8sPolicy(iss.srv.URL, models.OIDCDiscoveryServiceAccount, true))
		x.WithServiceAccountFiles(tokenFile, iss.caFile(t))

		if _, err := x.Authenticate(context.Background(), ciSign(t, iss.keys["k1"], "k1", iss.podClaims(time.Now())), ""); err != nil {
			t.Fatalf("first token: %v", err)
		}

		// The kubelet rotates the projected token, the issuer rotates a key.
		if err := os.WriteFile(tokenFile, []byte("projected-token-2"), 0o600); err != nil {
			t.Fatalf("rotate the token: %v", err)
		}

		iss.setToken("projected-token-2")
		k2 := iss.rotate(t, "k2")

		now.Add(int64(ciJWKSMinRefetch.Seconds()) + 1)

		if _, err := x.Authenticate(context.Background(), ciSign(t, k2, "k2", iss.podClaims(time.Now())), ""); err != nil {
			t.Fatalf("token under the rotated key: %v", err)
		}

		bearers := iss.seenBearers()
		if got := bearers[len(bearers)-1]; got != "Bearer projected-token-2" {
			t.Fatalf("the refetch sent %q, want the rotated token", got)
		}
	})

	tests := []struct {
		name     string
		token    string // what the server accepts
		tokenSet bool   // the token file is configured
		ca       string // clusterCA, otherCA, noCA or missingCA
		private  bool
		wantHits bool // the server was reached
	}{
		{
			name: "a server that does not know the token refuses the discovery", token: "other", tokenSet: true, private: true,
			ca: clusterCA, wantHits: true,
		},
		{
			name: "a CA that did not sign the server certificate", token: saToken, tokenSet: true, private: true,
			ca: otherCA,
		},
		{
			name: "no CA bundle: the system roots do not know the cluster CA", token: saToken, tokenSet: true, private: true,
			ca: noCA,
		},
		{
			name: "an unreadable CA bundle", token: saToken, tokenSet: true, private: true,
			ca: missingCA,
		},
		{
			name: "no service account token configured", token: saToken, tokenSet: false, private: true,
			ca: clusterCA,
		},
		{
			name: "a loopback issuer without allow_private_issuer", token: saToken, tokenSet: true, private: false,
			ca: clusterCA,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iss := newK8sTestIssuer(t, tt.token)
			x, _ := authenticator(k8sPolicy(iss.srv.URL, models.OIDCDiscoveryServiceAccount, tt.private))

			tokenFile := ""
			if tt.tokenSet {
				tokenFile = writeTestFile(t, "token", saToken)
			}

			x.WithServiceAccountFiles(tokenFile, caFileFor(t, tt.ca, iss))

			_, err := x.Authenticate(context.Background(), ciSign(t, iss.keys["k1"], "k1", iss.podClaims(time.Now())), "")
			requireDenial(t, err, CIOIDCIssuerUnreachable, false)

			if got := iss.hits.Load() > 0; got != tt.wantHits {
				t.Fatalf("server reached = %v, want %v", got, tt.wantHits)
			}
		})
	}

	t.Run("the token is never sent over plain http", func(t *testing.T) {
		iss := newCITestIssuerOnLoopback(t)
		x, _ := authenticator(k8sPolicy(iss.srv.URL, models.OIDCDiscoveryServiceAccount, true))
		x.WithServiceAccountFiles(writeTestFile(t, "token", saToken), "")

		_, err := x.Authenticate(context.Background(), ciSign(t, iss.key("k1"), "k1", iss.ciClaims(time.Now())), "")
		requireDenial(t, err, CIOIDCIssuerUnreachable, false)

		if iss.discoveryHits.Load() != 0 {
			t.Fatal("the discovery document was requested")
		}
	})

	t.Run("the anonymous mode does not trust the cluster CA nor send the token", func(t *testing.T) {
		iss := newK8sTestIssuer(t, saToken)
		x, _ := authenticator(k8sPolicy(iss.srv.URL, models.OIDCDiscoveryAnonymous, true))
		x.WithServiceAccountFiles(writeTestFile(t, "token", saToken), iss.caFile(t))

		_, err := x.Authenticate(context.Background(), ciSign(t, iss.keys["k1"], "k1", iss.podClaims(time.Now())), "")
		requireDenial(t, err, CIOIDCIssuerUnreachable, false)

		if bearers := iss.seenBearers(); slices.ContainsFunc(bearers, func(b string) bool { return b != "" }) {
			t.Fatalf("the anonymous mode sent credentials: %q", bearers)
		}
	})
}

func TestCIOIDCAllowPrivateIssuerGate(t *testing.T) {
	t.Setenv(netutil.EnvAllowPrivate, "false")

	tests := []struct {
		name    string
		private bool
		wantErr bool
	}{
		{"loopback issuer is refused by the SSRF guard", false, true},
		{"allow_private_issuer admits it for that policy", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iss := newCITestIssuerOnLoopback(t)
			p := ciPolicy(iss.srv.URL, "p", repoIs("acme/*"))
			p.AllowPrivateIssuer = tt.private

			x, _ := authenticator(p)

			_, err := x.Authenticate(context.Background(), ciSign(t, iss.key("k1"), "k1", iss.ciClaims(time.Now())), "")
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("Authenticate: %v", err)
				}

				return
			}

			requireDenial(t, err, CIOIDCIssuerUnreachable, false)

			if iss.discoveryHits.Load() != 0 {
				t.Fatal("the guard let the request through")
			}
		})
	}

	t.Run("a policy without the flag stays blocked next to one with it", func(t *testing.T) {
		iss := newCITestIssuerOnLoopback(t)
		open := ciPolicy(iss.srv.URL, "open", repoIs("other/*"))
		open.AllowPrivateIssuer = true
		closed := ciPolicy(iss.srv.URL, "closed", repoIs("acme/*"))

		x, _ := authenticator(closed, open)

		_, err := x.Authenticate(context.Background(), ciSign(t, iss.key("k1"), "k1", iss.ciClaims(time.Now())), "closed")
		requireDenial(t, err, CIOIDCIssuerUnreachable, false)
	})

	t.Run("link-local addresses stay blocked whatever the flag says", func(t *testing.T) {
		p := ciPolicy("https://169.254.169.254", "meta", repoIs("acme/*"))
		p.AllowPrivateIssuer = true

		x, _ := authenticator(p)
		claims := map[string]any{"iss": p.Issuer, "aud": ciTestAudience, "iat": time.Now().Unix()}

		_, err := x.Authenticate(context.Background(), ciSign(t, newRSAKey(t), "k", claims), "")
		requireDenial(t, err, CIOIDCIssuerUnreachable, false)
	})
}

// staticJWKS publishes the public part of keys, by key id.
func staticJWKS(t *testing.T, keys map[string]*rsa.PrivateKey) json.RawMessage {
	t.Helper()

	set := jose.JSONWebKeySet{}
	for kid, key := range keys {
		set.Keys = append(set.Keys, jose.JSONWebKey{Key: &key.PublicKey, KeyID: kid, Algorithm: string(jose.RS256), Use: "sig"})
	}

	raw, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}

	return raw
}

func staticPolicy(t *testing.T, issuer, name string, jwks json.RawMessage, conditions ...models.OIDCCondition) *models.OIDCTrustPolicy {
	t.Helper()

	p := ciPolicy(issuer, name, conditions...)
	p.Discovery = models.OIDCDiscovery{Mode: models.OIDCDiscoveryStatic, JWKS: jwks}

	return p
}

func TestCIOIDCStaticDiscovery(t *testing.T) {
	// Nothing may be fetched: the issuer does not exist and the SSRF guard
	// would refuse it anyway.
	t.Setenv(netutil.EnvAllowPrivate, "false")

	const issuer = "https://oidc.cluster.example.invalid"

	trusted, other := newRSAKey(t), newRSAKey(t)
	jwks := staticJWKS(t, map[string]*rsa.PrivateKey{"static-1": trusted})
	claims := func() map[string]any {
		return map[string]any{
			"iss": issuer, "sub": "system:serviceaccount:team-a:build-pipeline-7", "aud": ciTestAudience,
			"iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(10 * time.Minute).Unix(),
		}
	}
	policy := func() *models.OIDCTrustPolicy {
		return staticPolicy(
			t,
			issuer,
			"static",
			jwks,
			models.OIDCCondition{Claim: "sub", Patterns: []string{"system:serviceaccount:team-a:*"}},
		)
	}

	tests := []struct {
		name       string
		sign       func() string
		wantReason string // "" accepts
	}{
		{"a token signed by the inline key", func() string { return ciSign(t, trusted, "static-1", claims()) }, ""},
		{"a token naming no key id", func() string { return ciSign(t, trusted, "", claims()) }, ""},
		{
			"a token signed by another key under the same id",
			func() string { return ciSign(t, other, "static-1", claims()) },
			CIOIDCInvalidToken,
		},
		{
			"a token naming a key id the set does not hold",
			func() string { return ciSign(t, trusted, "static-2", claims()) },
			CIOIDCInvalidToken,
		},
		{"a token of another issuer", func() string {
			c := claims()
			c["iss"] = "https://evil.example"

			return ciSign(t, trusted, "static-1", c)
		}, CIOIDCUnknownIssuer},
		{"an expired token", func() string {
			c := claims()
			c["exp"] = time.Now().Add(-time.Hour).Unix()

			return ciSign(t, trusted, "static-1", c)
		}, CIOIDCInvalidToken},
		{"a token without iat", func() string {
			c := claims()
			delete(c, "iat")

			return ciSign(t, trusted, "static-1", c)
		}, CIOIDCInvalidToken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x, _ := authenticator(policy())

			match, err := x.Authenticate(context.Background(), tt.sign(), "")
			if tt.wantReason == "" {
				if err != nil || match.Policy.Name != "static" {
					t.Fatalf("Authenticate = %v, %v, want the static policy", match, err)
				}

				return
			}

			requireDenial(t, err, tt.wantReason, false)
		})
	}

	t.Run("a policy edit takes effect at once", func(t *testing.T) {
		p := policy()
		x, _ := authenticator(p)

		raw := ciSign(t, other, "static-2", claims())
		_, err := x.Authenticate(context.Background(), raw, "")
		requireDenial(t, err, CIOIDCInvalidToken, false)

		p.Discovery.JWKS = staticJWKS(t, map[string]*rsa.PrivateKey{"static-2": other})

		if _, err := x.Authenticate(context.Background(), raw, ""); err != nil {
			t.Fatalf("after adding the key: %v", err)
		}
	})

	t.Run("policies of one issuer with different key sources each verify their own tokens", func(t *testing.T) {
		iss := newCITestIssuer(t) // anonymous discovery on loopback
		inline := newRSAKey(t)
		anon := ciPolicy(iss.srv.URL, "fetched", repoIs("acme/*"))
		inl := staticPolicy(t, iss.srv.URL, "inline", staticJWKS(t, map[string]*rsa.PrivateKey{"inl": inline}), repoIs("acme/*"))

		x, _ := authenticator(anon, inl)

		fromIssuer, err := x.Authenticate(context.Background(), ciSign(t, iss.key("k1"), "k1", iss.ciClaims(time.Now())), "")
		if err != nil || fromIssuer.Policy.Name != "fetched" {
			t.Fatalf("token of the issuer: %v, %v, want policy fetched", fromIssuer, err)
		}

		fromInline, err := x.Authenticate(context.Background(), ciSign(t, inline, "inl", iss.ciClaims(time.Now())), "")
		if err != nil || fromInline.Policy.Name != "inline" {
			t.Fatalf("token of the inline key: %v, %v, want policy inline", fromInline, err)
		}
	})
}

func TestValidateOIDCPolicyDiscovery(t *testing.T) {
	key := newRSAKey(t)
	public := staticJWKS(t, map[string]*rsa.PrivateKey{"k": key})

	private, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key, KeyID: "k", Algorithm: "RS256"}}})
	if err != nil {
		t.Fatalf("marshal private key set: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(p *models.OIDCTrustPolicy)
		wantErr bool
	}{
		{"no discovery means anonymous", func(*models.OIDCTrustPolicy) {}, false},
		{"anonymous", func(p *models.OIDCTrustPolicy) { p.Discovery.Mode = "anonymous" }, false},
		{"serviceaccount with a private issuer", func(p *models.OIDCTrustPolicy) {
			p.Discovery.Mode = "serviceaccount"
			p.AllowPrivateIssuer = true
		}, false},
		{"unknown mode", func(p *models.OIDCTrustPolicy) { p.Discovery.Mode = "magic" }, true},
		{
			"static with a public key set",
			func(p *models.OIDCTrustPolicy) { p.Discovery = models.OIDCDiscovery{Mode: "static", JWKS: public} },
			false,
		},
		{"static without a key set", func(p *models.OIDCTrustPolicy) { p.Discovery.Mode = "static" }, true},
		{
			"static with a private key",
			func(p *models.OIDCTrustPolicy) { p.Discovery = models.OIDCDiscovery{Mode: "static", JWKS: private} },
			true,
		},
		{"static with a symmetric key", func(p *models.OIDCTrustPolicy) {
			p.Discovery = models.OIDCDiscovery{
				Mode: "static",
				JWKS: json.RawMessage(`{"keys":[{"kty":"oct","k":"c2VjcmV0c2VjcmV0c2VjcmV0"}]}`),
			}
		}, true},
		{"static with an HMAC algorithm", func(p *models.OIDCTrustPolicy) {
			p.Discovery = models.OIDCDiscovery{Mode: "static", JWKS: json.RawMessage(
				`{"keys":[{"kty":"RSA","alg":"HS256","n":"sXchDaQebHnPiGvyDOAT4saGEUetSyo9MKLOoWFsueri23bOdgWp4Dy1WlUzewbgBHod5pcM9H95GQRV3JDXboIRROSBigeC5yjU1hGzHHyXss8UDprecbAYxknTcQkhslANGRUZmdTOQ5qTRsLAt6BTYuyvVRdhS8exSZEy_c4gs_7svlJJQ4H9_NxsiIoLwAEk7-Q3UXERGYw_75IDrGA84-lA_-Ct4eTlXHBIY2EaV7t7LjJaynVJCpkv4LKjTTAumiGUIuQhrNhZLuF_RJLqHpM2kgWFLU7-VTdL1VbC2tejvcI2BlMkEpk1BzBZI0KQB0GaDWFLN-aEAw3vRw","e":"AQAB"}]}`,
			)}
		}, true},
		{"static with a malformed set", func(p *models.OIDCTrustPolicy) {
			p.Discovery = models.OIDCDiscovery{Mode: "static", JWKS: json.RawMessage(`{"keys":[]}`)}
		}, true},
		{"static with garbage", func(p *models.OIDCTrustPolicy) {
			p.Discovery = models.OIDCDiscovery{Mode: "static", JWKS: json.RawMessage(`[1]`)}
		}, true},
		{
			"keys outside the static mode",
			func(p *models.OIDCTrustPolicy) { p.Discovery = models.OIDCDiscovery{Mode: "anonymous", JWKS: public} },
			true,
		},
		{"private issuer with static discovery", func(p *models.OIDCTrustPolicy) {
			p.Discovery = models.OIDCDiscovery{Mode: "static", JWKS: public}
			p.AllowPrivateIssuer = true
		}, true},
		{"nested claim", func(p *models.OIDCTrustPolicy) {
			p.Conditions = []models.OIDCCondition{{Claim: "kubernetes.io.namespace", Patterns: []string{"team-a"}}}
		}, false},
		{"claim path with an empty segment", func(p *models.OIDCTrustPolicy) {
			p.Conditions = []models.OIDCCondition{{Claim: "kubernetes..namespace", Patterns: []string{"team-a"}}}
		}, true},
		{"claim path starting with a dot", func(p *models.OIDCTrustPolicy) {
			p.Conditions = []models.OIDCCondition{{Claim: ".kubernetes.io", Patterns: []string{"team-a"}}}
		}, true},
		{"claim path ending with a dot", func(p *models.OIDCTrustPolicy) {
			p.Conditions = []models.OIDCCondition{{Claim: "kubernetes.io.", Patterns: []string{"team-a"}}}
		}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &models.OIDCTrustPolicy{
				Name: "tekton", Issuer: "https://kubernetes.default.svc", Audience: "https://oa.example", TokenTTLSeconds: 600,
				Conditions: []models.OIDCCondition{{Claim: "sub", Patterns: []string{"system:serviceaccount:team-a:*"}}},
			}
			tt.mutate(p)

			err := ValidateOIDCPolicy(p)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateOIDCPolicy = %v, wantErr %v", err, tt.wantErr)
			}

			if err == nil && p.Discovery.Mode == "" {
				t.Fatal("the mode was not normalized")
			}
		})
	}
}

func TestClusterCAPoolKeepsSystemRoots(t *testing.T) {
	pool, err := clusterCAPool("")
	if err != nil || pool == nil {
		t.Fatalf("clusterCAPool(\"\") = %v, %v", pool, err)
	}

	garbage := writeTestFile(t, "ca.crt", "not a certificate")
	if _, err := clusterCAPool(garbage); err == nil {
		t.Fatal("a bundle without a certificate was accepted")
	}

	// A bundle that parses is added to a pool that is not the shared system one.
	iss := newK8sTestIssuer(t, "x")

	withCA, err := clusterCAPool(iss.caFile(t))
	if err != nil {
		t.Fatalf("clusterCAPool: %v", err)
	}

	if _, err := iss.srv.Certificate().Verify(x509.VerifyOptions{Roots: withCA}); err != nil {
		t.Fatalf("the cluster CA is not trusted after loading its bundle: %v", err)
	}
}
