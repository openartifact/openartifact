// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package attest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// The service only signs; consumers verify with cosign
// (`cosign verify-blob-attestation`). Verify is the signing tests' oracle.

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
