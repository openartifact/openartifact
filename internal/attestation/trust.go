// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package attestation indexes and verifies the signatures, in-toto
// attestations and SBOMs attached to docker images: cosign ".sig/.att/.sbom"
// tags and Sigstore bundle / DSSE referrers. Verification is offline and per
// repository: against the public keys and the Fulcio roots and identities the
// repository trusts (TrustConfig). There is no transparency log (no Rekor).
package attestation

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
)

// Limits on a trust configuration, so a settings document stays reviewable and
// the verification of one envelope stays cheap.
const (
	maxTrustKeys       = 16
	maxTrustRoots      = 16
	maxTrustIdentities = 32
	maxIdentityPattern = 512
	maxFieldLen        = 512
)

// ErrInvalidTrust is returned (wrapped) for a trust configuration that cannot be used.
var ErrInvalidTrust = errors.New("invalid attestation trust configuration")

// Identity is one signer a keyless (Fulcio) signature may come from: the OIDC
// issuer the certificate records and its subject (a SAN, e.g. the e-mail or the
// URI of a service account), as an exact value or a regular expression that
// must match the whole subject.
type Identity struct {
	Issuer        string `json:"issuer"`
	Subject       string `json:"subject,omitempty"`
	SubjectRegexp string `json:"subject_regexp,omitempty"`
}

// Fulcio is the keyless trust: certificates must chain to one of Roots and
// name one of Identities.
type Fulcio struct {
	Roots      []string   `json:"roots"`
	Identities []Identity `json:"identities"`
}

// TrustConfig is the per-repository attestation trust, as stored (JSON) and
// edited through the API. Keys and Fulcio.Roots hold PEM documents.
type TrustConfig struct {
	Keys                      []string `json:"keys"`
	Fulcio                    *Fulcio  `json:"fulcio,omitempty"`
	RequireVerifiedProvenance bool     `json:"require_verified_provenance"`
}

type compiledIdentity struct {
	issuer  string
	subject string
	re      *regexp.Regexp
}

type trustedKey struct {
	pub crypto.PublicKey
	id  string
}

// Trust is a compiled TrustConfig.
type Trust struct {
	keys       []trustedKey
	roots      *x509.CertPool
	identities []compiledIdentity
}

// Empty reports whether the configuration trusts nothing: every envelope then
// stays unverified.
func (t *Trust) Empty() bool { return t == nil || (len(t.keys) == 0 && t.roots == nil) }

// Compile validates the configuration and parses its keys, roots and patterns.
func (c *TrustConfig) Compile() (*Trust, error) {
	t := &Trust{}

	if len(c.Keys) > maxTrustKeys {
		return nil, fmt.Errorf("%w: at most %d keys", ErrInvalidTrust, maxTrustKeys)
	}

	for i, doc := range c.Keys {
		keys, err := parsePublicKeys(doc)
		if err != nil {
			return nil, fmt.Errorf("%w: keys[%d]: %w", ErrInvalidTrust, i, err)
		}

		t.keys = append(t.keys, keys...)
	}

	if len(t.keys) > maxTrustKeys {
		return nil, fmt.Errorf("%w: at most %d keys", ErrInvalidTrust, maxTrustKeys)
	}

	if c.Fulcio != nil {
		if err := t.compileFulcio(c.Fulcio); err != nil {
			return nil, err
		}
	}

	return t, nil
}

func (t *Trust) compileFulcio(f *Fulcio) error {
	if len(f.Roots) == 0 && len(f.Identities) == 0 {
		return nil
	}

	if len(f.Roots) == 0 || len(f.Identities) == 0 {
		return fmt.Errorf("%w: fulcio needs both roots and identities", ErrInvalidTrust)
	}

	if len(f.Roots) > maxTrustRoots || len(f.Identities) > maxTrustIdentities {
		return fmt.Errorf("%w: at most %d fulcio roots and %d identities", ErrInvalidTrust, maxTrustRoots, maxTrustIdentities)
	}

	t.roots = x509.NewCertPool()

	for i, doc := range f.Roots {
		if !t.roots.AppendCertsFromPEM([]byte(doc)) {
			return fmt.Errorf("%w: fulcio.roots[%d] holds no PEM certificate", ErrInvalidTrust, i)
		}
	}

	for i, id := range f.Identities {
		ci, err := compileIdentity(id)
		if err != nil {
			return fmt.Errorf("%w: fulcio.identities[%d]: %w", ErrInvalidTrust, i, err)
		}

		t.identities = append(t.identities, ci)
	}

	return nil
}

var (
	errIdentityIssuer  = errors.New("issuer is required")
	errIdentitySubject = errors.New("exactly one of subject and subject_regexp is required")
	errIdentityLength  = errors.New("value is too long")
)

func compileIdentity(id Identity) (compiledIdentity, error) {
	if id.Issuer == "" {
		return compiledIdentity{}, errIdentityIssuer
	}

	if (id.Subject == "") == (id.SubjectRegexp == "") {
		return compiledIdentity{}, errIdentitySubject
	}

	if len(id.Issuer) > maxFieldLen || len(id.Subject) > maxFieldLen || len(id.SubjectRegexp) > maxIdentityPattern {
		return compiledIdentity{}, errIdentityLength
	}

	ci := compiledIdentity{issuer: id.Issuer, subject: id.Subject}

	if id.SubjectRegexp != "" {
		re, err := regexp.Compile("^(?:" + id.SubjectRegexp + ")$")
		if err != nil {
			return compiledIdentity{}, fmt.Errorf("subject_regexp: %w", err)
		}

		ci.re = re
	}

	return ci, nil
}

// matches reports whether a certificate of issuer with one of the SAN values is
// an accepted identity.
func (t *Trust) matches(issuer string, sans []string) bool {
	for _, id := range t.identities {
		if id.issuer != issuer {
			continue
		}

		for _, san := range sans {
			if id.subject == san || (id.re != nil && id.re.MatchString(san)) {
				return true
			}
		}
	}

	return false
}

// parsePublicKeys reads every PEM public key (PKIX, as `cosign generate-key-pair`
// writes it) of doc. ECDSA, RSA and Ed25519 keys are accepted.
func parsePublicKeys(doc string) ([]trustedKey, error) {
	var out []trustedKey

	rest := []byte(doc)

	for {
		var blk *pem.Block

		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}

		if blk.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("%w: PEM block %q (want PUBLIC KEY)", ErrInvalidTrust, blk.Type)
		}

		pub, err := x509.ParsePKIXPublicKey(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse public key: %w", err)
		}

		switch pub.(type) {
		case *ecdsa.PublicKey, *rsa.PublicKey, ed25519.PublicKey:
		default:
			return nil, fmt.Errorf("%w: key type %T", ErrInvalidTrust, pub)
		}

		sum := sha256.Sum256(blk.Bytes)
		out = append(out, trustedKey{pub: pub, id: hex.EncodeToString(sum[:])})
	}

	if len(out) == 0 {
		return nil, errors.New("no PEM public key found") //nolint:err113 // reported to the user verbatim
	}

	return out, nil
}

// keyIdentity is the signer name recorded for a signature made with a static
// key: "key:" and the hex sha256 of the DER public key.
func keyIdentity(id string) string { return "key:" + id }
