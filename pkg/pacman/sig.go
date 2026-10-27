// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pacman

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/openartifact/openartifact/pkg/gpg"
)

// MaxSignatureBytes bounds a detached signature (a few hundred bytes for an
// RSA 4096 or Ed25519 signature; the rest is room for hashed subpackets).
const MaxSignatureBytes = 16 << 10

// ErrInvalidSignature reports a detached signature that is not exactly one
// OpenPGP binary-document signature.
var ErrInvalidSignature = errors.New("pacman: not a detached OpenPGP signature")

// NormalizeSignature returns the binary form of a detached package signature
// given either as binary packets or ASCII-armored (gpg --armor), after checking
// it is a single OpenPGP signature over a binary document. The signature is not
// verified: the repository has no way to know which key the uploader signs with.
func NormalizeSignature(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > MaxSignatureBytes {
		return nil, ErrInvalidSignature
	}

	if bytes.HasPrefix(bytes.TrimLeft(data, " \t\r\n"), []byte("-----BEGIN PGP SIGNATURE-----")) {
		block, err := armor.Decode(bytes.NewReader(data))
		if err != nil || block.Type != "PGP SIGNATURE" {
			return nil, ErrInvalidSignature
		}

		bin, err := io.ReadAll(io.LimitReader(block.Body, MaxSignatureBytes+1))
		if err != nil || len(bin) > MaxSignatureBytes {
			return nil, ErrInvalidSignature
		}

		data = bin
	}

	rd := bytes.NewReader(data)

	p, err := packet.Read(rd)
	if err != nil {
		return nil, ErrInvalidSignature
	}

	sig, ok := p.(*packet.Signature)
	if !ok || sig.SigType != packet.SigTypeBinary || rd.Len() != 0 {
		return nil, ErrInvalidSignature
	}

	return data, nil
}

// SignDetached signs data with signer and returns the binary detached
// signature, the form pacman reads from a .sig file and from %PGPSIG%.
func SignDetached(signer gpg.Signer, data io.Reader) ([]byte, error) {
	armored, err := signer.DetachedSign(data)
	if err != nil {
		return nil, fmt.Errorf("pacman: signing: %w", err)
	}

	block, err := armor.Decode(bytes.NewReader(armored))
	if err != nil {
		return nil, fmt.Errorf("pacman: decoding signature: %w", err)
	}

	bin, err := io.ReadAll(io.LimitReader(block.Body, MaxSignatureBytes+1))
	if err != nil || len(bin) > MaxSignatureBytes {
		return nil, fmt.Errorf("pacman: decoding signature: %w", ErrInvalidSignature)
	}

	return bin, nil
}
