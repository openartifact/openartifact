// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package attest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrBadSignature is returned by Verify when no signature matches.
var ErrBadSignature = errors.New("no valid signature")

// DecodeBase64 decodes standard or URL-safe base64, padded or not: DSSE
// implementations disagree on the alphabet.
func DecodeBase64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}

	return nil, errors.New("not base64") //nolint:err113 // reported to the user verbatim
}

// Verify checks the envelope against pub and returns the decoded statement:
// some signature of the envelope must verify over the PAE of its payload.
func Verify(env *Envelope, pub crypto.PublicKey) (*Statement, error) {
	payload, err := DecodeBase64(env.Payload)
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}

	pae := PAE(env.PayloadType, payload)

	for _, sg := range env.Signatures {
		raw, err := DecodeBase64(sg.Sig)
		if err != nil {
			continue
		}

		if VerifySignature(pub, pae, raw) {
			var st Statement
			if err := json.Unmarshal(payload, &st); err != nil {
				return nil, fmt.Errorf("decode statement: %w", err)
			}

			return &st, nil
		}
	}

	return nil, ErrBadSignature
}

// VerifySignature checks sig over message the way cosign signs: ECDSA (ASN.1)
// over the digest paired with the curve, RSA PKCS#1 v1.5 over SHA-256, Ed25519
// over the message itself. Other key types never verify.
func VerifySignature(pub crypto.PublicKey, message, sig []byte) bool {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return ed25519.Verify(k, message, sig)
	case *ecdsa.PublicKey:
		return ecdsa.VerifyASN1(k, digest(ecdsaHash(k), message), sig)
	case *rsa.PublicKey:
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, digest(crypto.SHA256, message), sig) == nil
	}

	return false
}
