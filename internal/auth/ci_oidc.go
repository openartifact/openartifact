// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"fmt"
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

	// saTokenFile and saCAFile are the projected service account token and the
	// cluster CA the serviceaccount discovery mode reads the issuer with.
	saTokenFile string
	saCAFile    string

	mu      sync.Mutex
	issuers map[ciProfile]*ciIssuer
}

// NewCIOIDC returns an authenticator over store.
func NewCIOIDC(store CIOIDCStore) *CIOIDC {
	return &CIOIDC{store: store, now: time.Now, issuers: make(map[ciProfile]*ciIssuer)}
}

// WithServiceAccountFiles sets the projected service account token and the CA
// bundle the serviceaccount discovery mode presents to, and trusts for, an issuer
// (in a pod: /var/run/secrets/kubernetes.io/serviceaccount/{token,ca.crt}). The
// token is read for every request, as the kubelet rotates it; the CA is read
// whenever an issuer is discovered.
func (x *CIOIDC) WithServiceAccountFiles(tokenFile, caFile string) *CIOIDC {
	x.saTokenFile, x.saCAFile = tokenFile, caFile

	return x
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

	// Policies of one issuer may find its keys differently (a static key set, an
	// authenticated read); the token is verified once per distinct way, and the
	// policies of the way that verifies it decide.
	var refusal error

	for _, g := range groupByProfile(policies) {
		claims, err := x.verify(ctx, g.profile, raw)
		if err != nil {
			if refusal == nil {
				refusal = err
			}

			continue
		}

		subject, _ := claims["sub"].(string)

		match, err := selectPolicy(g.policies, claims, issuer, subject)
		if err == nil {
			return match, nil
		}

		refusal = err
	}

	return nil, refusal
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

// verify checks the token against the issuer, whose keys are found the way
// profile says, and returns its claims.
func (x *CIOIDC) verify(ctx context.Context, profile ciProfile, raw string) (map[string]any, error) {
	verifier, err := x.verifierFor(ctx, profile)
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

// verifierFor returns the verifier of the profile's issuer: built from the
// stored key set for the static mode (nothing to fetch or cache), else the
// discovered, cached one.
func (x *CIOIDC) verifierFor(ctx context.Context, profile ciProfile) (*gooidc.IDTokenVerifier, error) {
	if profile.mode == models.OIDCDiscoveryStatic {
		return staticVerifier(profile, x.now)
	}

	return x.issuer(profile).load(ctx, x.now, func(ctx context.Context) (*gooidc.IDTokenVerifier, error) {
		f, err := x.fetcherFor(profile)
		if err != nil {
			return nil, err
		}

		return discoverCIIssuer(ctx, f, profile.issuer, x.now)
	})
}

func (x *CIOIDC) issuer(profile ciProfile) *ciIssuer {
	x.mu.Lock()
	defer x.mu.Unlock()

	i, ok := x.issuers[profile]
	if !ok {
		i = &ciIssuer{}
		x.issuers[profile] = i
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
func (i *ciIssuer) load(
	ctx context.Context, now func() time.Time, discover func(context.Context) (*gooidc.IDTokenVerifier, error),
) (*gooidc.IDTokenVerifier, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	at := now()

	if i.verifier != nil && at.Sub(i.loadedAt) < ciIssuerTTL {
		return i.verifier, nil
	}

	if i.failErr != nil && at.Sub(i.failedAt) < ciIssuerRetry {
		return nil, i.failErr
	}

	verifier, err := discover(ctx)
	if err != nil {
		i.failedAt, i.failErr = at, err

		return nil, err
	}

	i.verifier, i.loadedAt, i.failErr = verifier, at, nil

	return verifier, nil
}
