// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package attest signs generated SBOM and VEX documents as DSSE envelopes
// carrying in-toto attestations, verifiable with `cosign verify-blob-attestation`.
//
// The signing key is an ECDSA (P-256/384/521) or Ed25519 key, loaded from a PEM
// file (attestation.keyPath) or generated once and kept encrypted in the
// database (internal/crypto).
package attest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	_ "crypto/sha512" // registers SHA-384/512 for crypto.Hash.New
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
)

const (
	// PayloadType is the DSSE payload type of an in-toto statement.
	PayloadType = "application/vnd.in-toto+json"
	// EnvelopeContentType is the media type of a serialized DSSE envelope.
	EnvelopeContentType = "application/vnd.dsse.envelope.v1+json"
	// StatementType is the in-toto statement version emitted.
	StatementType = "https://in-toto.io/Statement/v1"
)

// Predicate types, as used by `cosign attest --type`.
const (
	PredicateCycloneDX = "https://cyclonedx.org/bom"
	PredicateSPDX      = "https://spdx.dev/Document"
	PredicateSPDX3     = "https://spdx.dev/Document/v3.0"
	PredicateOpenVEX   = "https://openvex.dev/ns"
)

// ErrNoSubject is returned when a statement would have no subject.
var ErrNoSubject = errors.New("attestation has no subject")

// ErrInvalidPredicate is returned when the predicate is not a JSON document.
var ErrInvalidPredicate = errors.New("attestation predicate is not valid JSON")

// Subject is one in-toto subject: the thing the predicate talks about.
type Subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

// SHA256Subject returns a subject identified by a sha256 hex digest.
func SHA256Subject(name, sha256Hex string) Subject {
	return Subject{Name: name, Digest: map[string]string{"sha256": sha256Hex}}
}

// Statement is an in-toto v1 statement.
type Statement struct {
	Type          string          `json:"_type"`
	Subject       []Subject       `json:"subject"`
	PredicateType string          `json:"predicateType"`
	Predicate     json.RawMessage `json:"predicate"`
}

// Signature is one DSSE signature. KeyID is left empty: verifiers (cosign,
// sigstore-go) skip a signature whose keyid differs from the one they derive
// from the public key, and the derivations are not interoperable.
type Signature struct {
	KeyID string `json:"keyid,omitempty"`
	Sig   string `json:"sig"`
}

// Envelope is a DSSE envelope.
type Envelope struct {
	PayloadType string      `json:"payloadType"`
	Payload     string      `json:"payload"`
	Signatures  []Signature `json:"signatures"`
}

// PAE is the DSSE pre-authentication encoding of a payload.
func PAE(payloadType string, payload []byte) []byte {
	// The header is built separately and joined with slices.Concat, which
	// checks the total length itself, so no hand-computed capacity can overflow.
	header := make([]byte, 0, 64)
	header = append(header, "DSSEv1 "...)
	header = strconv.AppendInt(header, int64(len(payloadType)), 10)
	header = append(header, ' ')
	header = append(header, payloadType...)
	header = append(header, ' ')
	header = strconv.AppendInt(header, int64(len(payload)), 10)
	header = append(header, ' ')

	return slices.Concat(header, payload)
}

// Signer signs DSSE payloads with one key.
type Signer struct {
	key    crypto.Signer
	keyID  string
	pubPEM []byte // PKIX PEM
}

// NewSigner wraps an ECDSA or Ed25519 private key.
func NewSigner(key crypto.Signer) (*Signer, error) {
	switch key.Public().(type) {
	case *ecdsa.PublicKey, ed25519.PublicKey:
	default:
		return nil, fmt.Errorf("%w: %T", ErrUnsupportedKey, key.Public())
	}

	der, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}

	sum := sha256.Sum256(der)

	return &Signer{key: key, keyID: hex.EncodeToString(sum[:]), pubPEM: pemBlock("PUBLIC KEY", der)}, nil
}

// KeyID is the hex sha256 of the DER-encoded public key.
func (s *Signer) KeyID() string { return s.keyID }

// PublicKeyPEM is the PKIX PEM public key, the file `cosign verify-blob-attestation --key` reads.
func (s *Signer) PublicKeyPEM() []byte { return s.pubPEM }

// Algorithm names the key type, e.g. "ecdsa-p256" or "ed25519".
func (s *Signer) Algorithm() string {
	switch k := s.key.Public().(type) {
	case *ecdsa.PublicKey:
		return "ecdsa-" + curveName(k)
	default:
		return "ed25519"
	}
}

func curveName(k *ecdsa.PublicKey) string {
	switch k.Curve.Params().BitSize {
	case 384:
		return "p384"
	case 521:
		return "p521"
	default:
		return "p256"
	}
}

// ecdsaHash is the digest sigstore verifiers pair with the curve: SHA-256 for
// P-256, SHA-384 for P-384, SHA-512 for P-521.
func ecdsaHash(k *ecdsa.PublicKey) crypto.Hash {
	switch k.Curve.Params().BitSize {
	case 384:
		return crypto.SHA384
	case 521:
		return crypto.SHA512
	default:
		return crypto.SHA256
	}
}

func digest(h crypto.Hash, data []byte) []byte {
	hh := h.New()
	_, _ = hh.Write(data)

	return hh.Sum(nil)
}

// signPAE signs the DSSE pre-authentication encoding: ECDSA signs its digest
// (ASN.1 DER signature, as cosign and sigstore verify it), Ed25519 signs it as is.
func (s *Signer) signPAE(pae []byte) ([]byte, error) {
	if _, ok := s.key.Public().(ed25519.PublicKey); ok {
		sig, err := s.key.Sign(rand.Reader, pae, crypto.Hash(0))
		if err != nil {
			return nil, fmt.Errorf("sign: %w", err)
		}

		return sig, nil
	}

	h := ecdsaHash(s.key.Public().(*ecdsa.PublicKey)) //nolint:forcetypeassert // NewSigner admits only ECDSA and Ed25519

	sig, err := s.key.Sign(rand.Reader, digest(h, pae), h)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}

	return sig, nil
}

// Sign wraps statement in a DSSE envelope.
func (s *Signer) Sign(st *Statement) (*Envelope, error) {
	if len(st.Subject) == 0 {
		return nil, ErrNoSubject
	}

	payload, err := json.Marshal(st)
	if err != nil {
		return nil, fmt.Errorf("marshal statement: %w", err)
	}

	sig, err := s.signPAE(PAE(PayloadType, payload))
	if err != nil {
		return nil, err
	}

	return &Envelope{
		PayloadType: PayloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures:  []Signature{{Sig: base64.StdEncoding.EncodeToString(sig)}},
	}, nil
}

// Attest builds the in-toto statement for predicate (a JSON document) about
// subjects and signs it.
func (s *Signer) Attest(predicateType string, predicate []byte, subjects []Subject) (*Envelope, error) {
	if !json.Valid(predicate) {
		return nil, ErrInvalidPredicate
	}

	return s.Sign(&Statement{
		Type:          StatementType,
		Subject:       subjects,
		PredicateType: predicateType,
		Predicate:     predicate,
	})
}

// Encode serializes env as a plain DSSE envelope, or as a Sigstore bundle when
// asBundle is set, and returns the matching content type.
func (s *Signer) Encode(env *Envelope, asBundle bool) ([]byte, string, error) {
	if asBundle {
		body, err := s.Bundle(env)

		return body, BundleContentType, err
	}

	body, err := json.Marshal(env)
	if err != nil {
		return nil, "", fmt.Errorf("marshal envelope: %w", err)
	}

	return body, EnvelopeContentType, nil
}

// ErrBadSignature is returned by Verify when no signature matches.
var ErrBadSignature = errors.New("no valid signature")

// Verify checks the envelope against pub and returns the decoded statement.
func Verify(env *Envelope, pub crypto.PublicKey) (*Statement, error) {
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}

	pae := PAE(env.PayloadType, payload)

	for _, sg := range env.Signatures {
		raw, err := base64.StdEncoding.DecodeString(sg.Sig)
		if err != nil {
			continue
		}

		if verifyPAE(pub, pae, raw) {
			var st Statement
			if err := json.Unmarshal(payload, &st); err != nil {
				return nil, fmt.Errorf("decode statement: %w", err)
			}

			return &st, nil
		}
	}

	return nil, ErrBadSignature
}

func verifyPAE(pub crypto.PublicKey, pae, sig []byte) bool {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return ed25519.Verify(k, pae, sig)
	case *ecdsa.PublicKey:
		return ecdsa.VerifyASN1(k, digest(ecdsaHash(k), pae), sig)
	}

	return false
}
