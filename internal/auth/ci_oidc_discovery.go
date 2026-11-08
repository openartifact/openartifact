// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/telemetry"
)

const (
	// ciMaxStaticJWKSBytes bounds the key set stored on a policy.
	ciMaxStaticJWKSBytes = 64 << 10
	ciMaxStaticKeys      = 32
	// ciMaxLocalFileBytes bounds the token and the CA bundle read from disk.
	ciMaxLocalFileBytes = 1 << 20
	ciClientTimeout     = 30 * time.Second
)

var (
	errNoServiceAccountToken = errors.New("no service account token file is configured")
	errEmptyToken            = errors.New("the service account token file is empty")
	errBearerOverPlainHTTP   = errors.New("refusing to send the service account token over plain http")
	errNoCACerts             = errors.New("the CA file holds no certificate")
	errFileTooLarge          = errors.New("file is too large")
)

// ciProfile is how the keys of one issuer are found, as set by the policies
// that trust the issuer. It is comparable: it keys the discovery cache (jwks
// empty, as a static key set is never fetched or cached) and groups policies.
type ciProfile struct {
	issuer  string
	mode    string
	jwks    string
	private bool
}

func profileOf(p *models.OIDCTrustPolicy) ciProfile {
	prof := ciProfile{issuer: p.Issuer, mode: p.Discovery.EffectiveMode(), private: p.AllowPrivateIssuer}

	if prof.mode == models.OIDCDiscoveryStatic {
		prof.jwks = string(p.Discovery.JWKS)
	}

	return prof
}

type ciPolicyGroup struct {
	profile  ciProfile
	policies []*models.OIDCTrustPolicy
}

// groupByProfile splits policies (oldest first) by the way their issuer's keys
// are found, keeping the order of the groups and of the policies in each.
func groupByProfile(policies []*models.OIDCTrustPolicy) []ciPolicyGroup {
	var groups []ciPolicyGroup

	for _, p := range policies {
		prof := profileOf(p)

		i := slices.IndexFunc(groups, func(g ciPolicyGroup) bool { return g.profile == prof })
		if i < 0 {
			groups = append(groups, ciPolicyGroup{profile: prof})
			i = len(groups) - 1
		}

		groups[i].policies = append(groups[i].policies, p)
	}

	return groups
}

// ciFetcher reads the documents of an issuer: with the shared SSRF-guarded
// client, or one that may reach private addresses and trusts the cluster CA,
// and, when tokenFile is set, with the process's service account token as bearer.
type ciFetcher struct {
	client    *http.Client
	tokenFile string
}

// fetcherFor returns the fetcher the profile's mode needs. A client with
// private-address or CA exceptions is built per discovery (the cluster CA is
// read then, so a rotated bundle is picked up within ciIssuerTTL) and keeps no
// idle connections.
func (x *CIOIDC) fetcherFor(p ciProfile) (*ciFetcher, error) {
	if p.mode == models.OIDCDiscoveryAnonymous && !p.private {
		return &ciFetcher{client: oidcHTTPClient}, nil
	}

	opts := netutil.ClientOptions{AllowPrivate: p.private, DisableKeepAlives: true}
	f := &ciFetcher{}

	if p.mode == models.OIDCDiscoveryServiceAccount {
		if x.saTokenFile == "" {
			return nil, fmt.Errorf("%w: %w", ErrOIDCDiscoveryFailed, errNoServiceAccountToken)
		}

		pool, err := clusterCAPool(x.saCAFile)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrOIDCDiscoveryFailed, err)
		}

		opts.RootCAs, f.tokenFile = pool, x.saTokenFile
	}

	f.client = telemetry.WrapClient(netutil.SafeHTTPClientWithOptions(ciClientTimeout, opts))

	return f, nil
}

// clusterCAPool is the system roots plus the PEM bundle in caFile (when set).
func clusterCAPool(caFile string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}

	if caFile == "" {
		return pool, nil
	}

	pemData, err := readLocalFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read the CA file: %w", err)
	}

	if !pool.AppendCertsFromPEM(pemData) {
		return nil, errNoCACerts
	}

	return pool, nil
}

func readLocalFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, ciMaxLocalFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	if len(data) > ciMaxLocalFileBytes {
		return nil, errFileTooLarge
	}

	return data, nil
}

// get downloads url (at most ciMaxJWKSBytes). The bearer token is read from its
// file for every request, as it is rotated, and only ever sent over https; a
// redirect to another origin drops it (netutil.SafeHTTPClient).
func (f *ciFetcher) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	if f.tokenFile != "" {
		if req.URL.Scheme != "https" {
			return nil, errBearerOverPlainHTTP
		}

		token, err := readLocalFile(f.tokenFile)
		if err != nil {
			return nil, fmt.Errorf("read the service account token: %w", err)
		}

		if token = bytes.TrimSpace(token); len(token) == 0 {
			return nil, errEmptyToken
		}

		req.Header.Set("Authorization", "Bearer "+string(token))
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrOIDCDiscoveryFailed, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, ciMaxJWKSBytes))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	return body, nil
}

// discoverCIIssuer reads the issuer's discovery document and returns a verifier
// over its key set (fetched lazily, see ciKeySet).
func discoverCIIssuer(ctx context.Context, f *ciFetcher, issuer string, now func() time.Time) (*gooidc.IDTokenVerifier, error) {
	ctx, cancel := context.WithTimeout(ctx, ciFetchTimeout)
	defer cancel()

	body, err := f.get(ctx, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration")
	if err != nil {
		return nil, fmt.Errorf("discover issuer: %w", err)
	}

	var doc struct {
		Issuer  string   `json:"issuer"`
		JWKSURI string   `json:"jwks_uri"`
		Algs    []string `json:"id_token_signing_alg_values_supported"`
	}

	if err := json.Unmarshal(body, &doc); err != nil || doc.JWKSURI == "" {
		return nil, fmt.Errorf("%w: no jwks_uri in the discovery document", ErrOIDCDiscoveryFailed)
	}

	if doc.Issuer != issuer {
		return nil, fmt.Errorf("%w: the discovery document names another issuer", ErrOIDCDiscoveryFailed)
	}

	if !netutil.IsHTTPSOrLoopbackURL(doc.JWKSURI) {
		return nil, fmt.Errorf("%w: jwks_uri must be https", ErrOIDCDiscoveryFailed)
	}

	algs := make([]string, 0, len(doc.Algs))

	for _, a := range doc.Algs {
		if slices.Contains(ciAlgorithms, jose.SignatureAlgorithm(a)) {
			algs = append(algs, a)
		}
	}

	if len(algs) == 0 {
		algs = []string{string(jose.RS256)}
	}

	keys := &ciKeySet{url: doc.JWKSURI, fetcher: f, now: now}

	return gooidc.NewVerifier(issuer, keys, &gooidc.Config{
		SkipClientIDCheck:    true, // the audience is the policy's, checked by selectPolicy
		SupportedSigningAlgs: algs,
		Now:                  now,
	}), nil
}

// staticVerifier returns a verifier over the key set stored on the policy. The
// issuer, expiry and times of the tokens are checked as for a discovered one.
func staticVerifier(p ciProfile, now func() time.Time) (*gooidc.IDTokenVerifier, error) {
	keys, err := parseStaticJWKS([]byte(p.jwks))
	if err != nil {
		return nil, fmt.Errorf("static key set: %w", err)
	}

	algs := make([]string, len(ciAlgorithms))
	for i, a := range ciAlgorithms {
		algs[i] = string(a)
	}

	return gooidc.NewVerifier(p.issuer, &ciKeySet{keys: keys, now: now}, &gooidc.Config{
		SkipClientIDCheck:    true,
		SupportedSigningAlgs: algs,
		Now:                  now,
	}), nil
}

// parseStaticJWKS parses the key set of a static policy strictly: a JSON object
// with a "keys" array of at most ciMaxStaticKeys valid public keys with no
// symmetric or private key (a policy never holds key material that could sign),
// at least one of them for signatures.
func parseStaticJWKS(raw []byte) ([]jose.JSONWebKey, error) {
	if len(raw) == 0 || len(raw) > ciMaxStaticJWKSBytes {
		return nil, policyErrorf("jwks must be a JSON key set of at most %d bytes", ciMaxStaticJWKSBytes)
	}

	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}

	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, policyErrorf("jwks must be a JSON object with a keys array")
	}

	if len(set.Keys) == 0 || len(set.Keys) > ciMaxStaticKeys {
		return nil, policyErrorf("jwks must hold between 1 and %d keys", ciMaxStaticKeys)
	}

	keys := make([]jose.JSONWebKey, 0, len(set.Keys))

	for i, k := range set.Keys {
		var key jose.JSONWebKey
		if err := json.Unmarshal(k, &key); err != nil || !key.Valid() {
			return nil, policyErrorf("jwks.keys[%d] is not a valid JSON web key", i)
		}

		if !key.IsPublic() {
			return nil, policyErrorf("jwks.keys[%d] is not a public key: private and symmetric keys are not accepted", i)
		}

		if key.Algorithm != "" && !slices.Contains(ciAlgorithms, jose.SignatureAlgorithm(key.Algorithm)) {
			return nil, policyErrorf("jwks.keys[%d].alg %q is not an asymmetric signature algorithm", i, key.Algorithm)
		}

		keys = append(keys, key)
	}

	if len(matchingKeys(keys, "")) == 0 {
		return nil, policyErrorf("jwks holds no signing key")
	}

	return keys, nil
}

// ciKeySet is the key set of an issuer: fetched from its jwks_uri, or fixed (no
// fetcher). Fetched keys are kept for the life of the verifier (ciIssuerTTL); a
// key id it does not hold triggers a fetch, at most one per ciJWKSMinRefetch, so
// rotation is followed and a token naming random key ids cannot turn the server
// into a request amplifier.
type ciKeySet struct {
	url     string
	fetcher *ciFetcher
	now     func() time.Time

	mu        sync.Mutex
	keys      []jose.JSONWebKey
	fetchedAt time.Time
}

// VerifySignature implements gooidc.KeySet.
func (k *ciKeySet) VerifySignature(ctx context.Context, raw string) ([]byte, error) {
	jws, err := jose.ParseSignedCompact(raw, ciAlgorithms)
	if err != nil {
		return nil, fmt.Errorf("malformed jwt: %w", err)
	}

	if len(jws.Signatures) != 1 {
		return nil, errors.New("jwt must carry exactly one signature") //nolint:err113 // logged only
	}

	header := jws.Signatures[0].Header

	for _, key := range k.keysFor(ctx, header.KeyID) {
		if key.Algorithm != "" && key.Algorithm != header.Algorithm {
			continue
		}

		if payload, err := jws.Verify(&key); err == nil {
			return payload, nil
		}
	}

	return nil, errors.New("no key of the issuer verifies the signature") //nolint:err113 // logged only
}

// keysFor returns the signing keys with the key id (all of them for a token
// that names none), fetching the key set when the id is not held and the last
// fetch is old enough.
func (k *ciKeySet) keysFor(ctx context.Context, kid string) []jose.JSONWebKey {
	k.mu.Lock()
	defer k.mu.Unlock()

	if keys := matchingKeys(k.keys, kid); len(keys) > 0 || k.fetcher == nil {
		return keys
	}

	if at := k.now(); k.fetchedAt.IsZero() || at.Sub(k.fetchedAt) >= ciJWKSMinRefetch {
		k.fetchedAt = at

		if keys, err := k.fetcher.jwks(ctx, k.url); err == nil {
			k.keys = keys
		}
	}

	return matchingKeys(k.keys, kid)
}

func matchingKeys(keys []jose.JSONWebKey, kid string) []jose.JSONWebKey {
	var out []jose.JSONWebKey

	for _, key := range keys {
		if key.Use == "enc" || (kid != "" && key.KeyID != kid) {
			continue
		}

		out = append(out, key)
	}

	return out
}

// jwks downloads a key set, skipping the keys it cannot use.
func (f *ciFetcher) jwks(ctx context.Context, url string) ([]jose.JSONWebKey, error) {
	ctx, cancel := context.WithTimeout(ctx, ciFetchTimeout)
	defer cancel()

	body, err := f.get(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}

	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}

	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("decode jwks: %w", err)
	}

	keys := make([]jose.JSONWebKey, 0, len(set.Keys))

	for _, raw := range set.Keys {
		var key jose.JSONWebKey
		if json.Unmarshal(raw, &key) == nil && key.Valid() {
			keys = append(keys, key)
		}
	}

	return keys, nil
}
