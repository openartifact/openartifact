// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Reasons an ID token is refused (CIOIDCDenialError.Reason). They go to the log and
// the audit trail, never to the caller.
const (
	CIOIDCMalformed         = "malformed"
	CIOIDCUnknownIssuer     = "unknown_issuer"
	CIOIDCIssuerUnreachable = "issuer_unreachable"
	CIOIDCInvalidToken      = "invalid_token"
	CIOIDCAudience          = "audience_mismatch"
	CIOIDCConditions        = "conditions_not_met"
	CIOIDCAccountDisabled   = "service_account_disabled"
)

const (
	// ciIssuerTTL bounds how long the discovery document and the signing keys
	// of an issuer are trusted before they are fetched again, so a key the
	// issuer withdrew stops verifying within the hour.
	ciIssuerTTL = time.Hour
	// ciIssuerRetry is how long a failed discovery is remembered, so an issuer
	// that is down is not fetched once per request.
	ciIssuerRetry = 30 * time.Second
	// ciJWKSMinRefetch is the shortest interval between two fetches of one
	// issuer's key set; a token with an unknown key id cannot make the server
	// fetch faster.
	ciJWKSMinRefetch = 30 * time.Second
	ciFetchTimeout   = 10 * time.Second
	ciMaxJWKSBytes   = 1 << 20
	// ciIATSkew is how far in the future an iat claim may lie.
	ciIATSkew = time.Minute
)

// ciAlgorithms are the signature algorithms an ID token may use: asymmetric
// ones only, so a key set can never be used as an HMAC secret.
//
//nolint:gochecknoglobals // immutable registry
var ciAlgorithms = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512, jose.PS256, jose.PS384, jose.PS512,
	jose.ES256, jose.ES384, jose.ES512, jose.EdDSA,
}

// errTokenFromFuture is the logged cause of a token whose iat lies ahead of the clock.
var errTokenFromFuture = errors.New("token issued in the future")

// CIOIDCDenialError is the refusal of an ID token. Verified is set when the token's
// signature, issuer and times were checked before it was refused, so Issuer and
// Subject are authentic; the audit trail records only such refusals as an
// attempt (anything else may be noise from anyone).
type CIOIDCDenialError struct {
	Reason   string
	Verified bool
	Issuer   string
	Subject  string
	Err      error
}

func (d *CIOIDCDenialError) Error() string {
	if d.Err != nil {
		return "ci oidc token refused: " + d.Reason + ": " + d.Err.Error()
	}

	return "ci oidc token refused: " + d.Reason
}

func (d *CIOIDCDenialError) Unwrap() error { return d.Err }

// CIOIDCStore is the part of the database CIOIDC needs.
type CIOIDCStore interface {
	ListEnabledOIDCPoliciesByIssuer(ctx context.Context, issuer string) ([]*models.OIDCTrustPolicy, error)
}

// CIOIDCMatch is an accepted ID token: the policy that trusts it and its
// verified claims.
type CIOIDCMatch struct {
	Policy  *models.OIDCTrustPolicy
	Claims  map[string]any
	Subject string
}

// CIOIDC authenticates ID tokens of CI systems against the trust policies. It
// only ever contacts issuers an administrator named in an enabled policy; the
// iss claim of a token is read unverified solely to pick the policies (and so
// the issuer) its signature is then checked against.
type CIOIDC struct {
	store CIOIDCStore
	now   func() time.Time

	mu      sync.Mutex
	issuers map[string]*ciIssuer
}

// NewCIOIDC returns an authenticator over store.
func NewCIOIDC(store CIOIDCStore) *CIOIDC {
	return &CIOIDC{store: store, now: time.Now, issuers: make(map[string]*ciIssuer)}
}

// Authenticate verifies raw (signature through the issuer's published keys,
// issuer, expiry, not-before, issued-at) and returns the first policy, oldest
// first, that trusts it: the audience is the one the policy names, every claim
// condition holds and the service account is enabled. When policyName is not
// empty only that policy is considered. A refusal is a *CIOIDCDenialError; any other
// error is the store's.
func (x *CIOIDC) Authenticate(ctx context.Context, raw, policyName string) (*CIOIDCMatch, error) {
	issuer, ok := unverifiedIssuer(raw)
	if !ok {
		return nil, &CIOIDCDenialError{Reason: CIOIDCMalformed}
	}

	policies, err := x.store.ListEnabledOIDCPoliciesByIssuer(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("list oidc policies: %w", err)
	}

	if policyName != "" {
		policies = slices.DeleteFunc(policies, func(p *models.OIDCTrustPolicy) bool { return p.Name != policyName })
	}

	if len(policies) == 0 {
		return nil, &CIOIDCDenialError{Reason: CIOIDCUnknownIssuer}
	}

	claims, err := x.verify(ctx, issuer, raw)
	if err != nil {
		return nil, err
	}

	subject, _ := claims["sub"].(string)

	return selectPolicy(policies, claims, issuer, subject)
}

// selectPolicy returns the first policy that trusts the verified claims. The
// refusal names the nearest miss: a disabled account, else conditions, else
// audience.
func selectPolicy(policies []*models.OIDCTrustPolicy, claims map[string]any, issuer, subject string) (*CIOIDCMatch, error) {
	reason := CIOIDCAudience

	for _, p := range policies {
		if !slices.Contains(claimStrings(claims["aud"]), p.Audience) {
			continue
		}

		if !ConditionsMatch(p.Conditions, claims) {
			if reason == CIOIDCAudience {
				reason = CIOIDCConditions
			}

			continue
		}

		if p.ServiceAccountDisabled {
			reason = CIOIDCAccountDisabled

			continue
		}

		return &CIOIDCMatch{Policy: p, Claims: claims, Subject: subject}, nil
	}

	return nil, &CIOIDCDenialError{Reason: reason, Verified: true, Issuer: issuer, Subject: subject}
}

// verify checks the token against the issuer and returns its claims.
func (x *CIOIDC) verify(ctx context.Context, issuer, raw string) (map[string]any, error) {
	verifier, err := x.issuer(issuer).load(ctx, issuer, x.now)
	if err != nil {
		return nil, &CIOIDCDenialError{Reason: CIOIDCIssuerUnreachable, Err: err}
	}

	token, err := verifier.Verify(oidcContext(ctx), raw)
	if err != nil {
		return nil, &CIOIDCDenialError{Reason: CIOIDCInvalidToken, Err: err}
	}

	var claims map[string]any
	if err := token.Claims(&claims); err != nil {
		return nil, &CIOIDCDenialError{Reason: CIOIDCInvalidToken, Err: err}
	}

	iat, ok := claims["iat"].(float64)
	if !ok {
		return nil, &CIOIDCDenialError{Reason: CIOIDCInvalidToken, Err: errors.New("iat claim missing")} //nolint:err113 // logged only
	}

	if time.Unix(int64(iat), 0).After(x.now().Add(ciIATSkew)) {
		return nil, &CIOIDCDenialError{
			Reason: CIOIDCInvalidToken,
			Err:    errTokenFromFuture,
		}
	}

	return claims, nil
}

func (x *CIOIDC) issuer(issuer string) *ciIssuer {
	x.mu.Lock()
	defer x.mu.Unlock()

	i, ok := x.issuers[issuer]
	if !ok {
		i = &ciIssuer{}
		x.issuers[issuer] = i
	}

	return i
}

// ciIssuer is the discovered state of one issuer.
type ciIssuer struct {
	mu       sync.Mutex
	verifier *gooidc.IDTokenVerifier
	loadedAt time.Time
	failedAt time.Time
	failErr  error
}

// load returns the issuer's verifier, discovering the issuer (through the
// SSRF-guarded client) when it is not loaded, older than ciIssuerTTL or its
// last attempt failed more than ciIssuerRetry ago.
func (i *ciIssuer) load(ctx context.Context, issuer string, now func() time.Time) (*gooidc.IDTokenVerifier, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	at := now()

	if i.verifier != nil && at.Sub(i.loadedAt) < ciIssuerTTL {
		return i.verifier, nil
	}

	if i.failErr != nil && at.Sub(i.failedAt) < ciIssuerRetry {
		return nil, i.failErr
	}

	verifier, err := discoverCIIssuer(ctx, issuer, now)
	if err != nil {
		i.failedAt, i.failErr = at, err

		return nil, err
	}

	i.verifier, i.loadedAt, i.failErr = verifier, at, nil

	return verifier, nil
}

func discoverCIIssuer(ctx context.Context, issuer string, now func() time.Time) (*gooidc.IDTokenVerifier, error) {
	ctx, cancel := context.WithTimeout(ctx, ciFetchTimeout)
	defer cancel()

	provider, err := gooidc.NewProvider(oidcContext(ctx), issuer)
	if err != nil {
		return nil, fmt.Errorf("discover issuer: %w", err)
	}

	var doc struct {
		JWKSURI string   `json:"jwks_uri"`
		Algs    []string `json:"id_token_signing_alg_values_supported"`
	}

	if err := provider.Claims(&doc); err != nil || doc.JWKSURI == "" {
		return nil, fmt.Errorf("%w: no jwks_uri in the discovery document", ErrOIDCDiscoveryFailed)
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

	keys := &ciKeySet{url: doc.JWKSURI, now: now}

	return gooidc.NewVerifier(issuer, keys, &gooidc.Config{
		SkipClientIDCheck:    true, // the audience is the policy's, checked by selectPolicy
		SupportedSigningAlgs: algs,
		Now:                  now,
	}), nil
}

// ciKeySet is a key set fetched from an issuer's jwks_uri. Keys are kept for the
// life of the verifier (ciIssuerTTL); a key id it does not hold triggers a fetch,
// at most one per ciJWKSMinRefetch, so rotation is followed and a token naming
// random key ids cannot turn the server into a request amplifier.
type ciKeySet struct {
	url string
	now func() time.Time

	mu        sync.Mutex
	keys      []jose.JSONWebKey
	fetchedAt time.Time
}

// VerifySignature implements gooidc.KeySet.
func (k *ciKeySet) VerifySignature(ctx context.Context, raw string) ([]byte, error) {
	jws, err := jose.ParseSigned(raw, ciAlgorithms)
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

	if keys := matchingKeys(k.keys, kid); len(keys) > 0 {
		return keys
	}

	if at := k.now(); k.fetchedAt.IsZero() || at.Sub(k.fetchedAt) >= ciJWKSMinRefetch {
		k.fetchedAt = at

		if keys, err := fetchJWKS(ctx, k.url); err == nil {
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

// fetchJWKS downloads a key set, skipping the keys it cannot use.
func fetchJWKS(ctx context.Context, url string) ([]jose.JSONWebKey, error) {
	ctx, cancel := context.WithTimeout(ctx, ciFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("jwks request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch jwks: %w: status %d", ErrOIDCDiscoveryFailed, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, ciMaxJWKSBytes))
	if err != nil {
		return nil, fmt.Errorf("read jwks: %w", err)
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
