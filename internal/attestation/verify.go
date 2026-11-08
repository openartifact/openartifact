// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package attestation

import (
	"crypto"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/attest"
)

// Sigstore certificate extensions that record the OIDC issuer: the legacy raw
// string and the DER UTF8String form.
const (
	oidIssuerRawArc = 1
	oidIssuerDERArc = 8
)

// oidIssuer returns the Sigstore issuer extension OID that ends in arc.
func oidIssuer(arc int) asn1.ObjectIdentifier {
	return asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, arc}
}

// Material is what travels with a signature besides the signature itself: the
// certificate of a keyless signature (leaf first) and the chain that came with it.
type Material struct {
	Leaf  *x509.Certificate
	Chain []*x509.Certificate
}

// Outcome is the result of verifying one signature. Signer and Issuer name the
// certificate's identity even when it did not verify (what the signature claims),
// or the key id of a static key that did.
type Outcome struct {
	Verified bool
	Signer   string
	Issuer   string
	Reason   string
}

// Verifier checks signatures against a Trust. The zero value is ready to use.
type Verifier struct{}

// Errors reported (as Outcome.Reason) for signatures that do not verify.
var (
	errNoTrust    = errors.New("the repository trusts no key and no Fulcio identity")
	errNoMatch    = errors.New("the signature matches no trusted key")
	errCertSig    = errors.New("the signature does not match the certificate's key")
	errIdentity   = errors.New("the certificate identity is not trusted")
	errNoFulcio   = errors.New("the signature carries a certificate but the repository trusts no Fulcio root")
	errSubjectDig = errors.New("the signed statement does not cover the image digest")
)

// VerifyEnvelope checks a DSSE envelope. at is the time the signature is judged
// at (the first time it was seen: Fulcio certificates live minutes).
func (v *Verifier) VerifyEnvelope(t *Trust, env *attest.Envelope, mat Material, at time.Time) Outcome {
	return v.verify(t, mat, at, func(pub crypto.PublicKey) bool {
		_, err := attest.Verify(env, pub)
		return err == nil
	})
}

// VerifyBlob checks a detached signature over payload, the form of a cosign
// ".sig" layer.
func (v *Verifier) VerifyBlob(t *Trust, payload, sig []byte, mat Material, at time.Time) Outcome {
	return v.verify(t, mat, at, func(pub crypto.PublicKey) bool {
		return attest.VerifySignature(pub, payload, sig)
	})
}

func (v *Verifier) verify(t *Trust, mat Material, at time.Time, check func(crypto.PublicKey) bool) Outcome {
	var out Outcome

	if mat.Leaf != nil {
		out.Signer, out.Issuer = certIdentity(mat.Leaf)
	}

	if t.Empty() {
		out.Reason = errNoTrust.Error()
		return out
	}

	reason := errNoMatch

	if mat.Leaf != nil {
		err := v.verifyCert(t, mat, at, check)
		if err == nil {
			out.Verified = true
			return out
		}

		reason = err
	}

	for _, k := range t.keys {
		if check(k.pub) {
			return Outcome{Verified: true, Signer: keyIdentity(k.id)}
		}
	}

	out.Reason = reason.Error()

	return out
}

// verifyCert accepts a keyless signature: the certificate chains to a trusted
// Fulcio root at time at, names a trusted identity, and its key made the signature.
func (v *Verifier) verifyCert(t *Trust, mat Material, at time.Time, check func(crypto.PublicKey) bool) error {
	if t.roots == nil {
		return errNoFulcio
	}

	inter := x509.NewCertPool()
	for _, c := range mat.Chain {
		inter.AddCert(c)
	}

	_, err := mat.Leaf.Verify(x509.VerifyOptions{
		Roots:         t.roots,
		Intermediates: inter,
		CurrentTime:   at,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	})
	if err != nil {
		return fmt.Errorf("certificate does not chain to a trusted Fulcio root at %s: %w", at.UTC().Format(time.RFC3339), err)
	}

	signer, issuer := certIdentity(mat.Leaf)
	if !t.matches(issuer, certSANs(mat.Leaf)) {
		return fmt.Errorf("%w: %s issued by %s", errIdentity, signer, issuer)
	}

	if !check(mat.Leaf.PublicKey) {
		return errCertSig
	}

	return nil
}

// certSANs lists the identities a certificate carries: URIs and e-mail addresses.
func certSANs(c *x509.Certificate) []string {
	sans := make([]string, 0, len(c.URIs)+len(c.EmailAddresses))

	for _, u := range c.URIs {
		sans = append(sans, u.String())
	}

	return append(sans, c.EmailAddresses...)
}

// certIdentity returns the first SAN and the OIDC issuer extension of a
// Sigstore certificate.
func certIdentity(c *x509.Certificate) (string, string) {
	var subject string
	if sans := certSANs(c); len(sans) > 0 {
		subject = sans[0]
	}

	var issuer string

	for _, e := range c.Extensions {
		switch {
		case e.Id.Equal(oidIssuer(oidIssuerDERArc)):
			var s string
			if _, err := asn1.Unmarshal(e.Value, &s); err == nil {
				return subject, s
			}
		case e.Id.Equal(oidIssuer(oidIssuerRawArc)):
			issuer = string(e.Value)
		}
	}

	return subject, issuer
}
