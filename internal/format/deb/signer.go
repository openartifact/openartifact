// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/openartifact/openartifact/pkg/gpg"
)

type ReleaseSigner struct {
	signer gpg.Signer
}

func NewReleaseSigner(signer gpg.Signer) *ReleaseSigner {
	return &ReleaseSigner{signer: signer}
}

func (rs *ReleaseSigner) SignRelease(releaseData []byte) (*SignedRelease, error) {
	if rs.signer == nil {
		return nil, errors.New("no signer configured") //nolint:err113 // static error
	}

	detachedSig, err := rs.signer.DetachedSign(bytes.NewReader(releaseData))
	if err != nil {
		return nil, fmt.Errorf("failed to create detached signature: %w", err)
	}

	inReleaseReader, err := rs.signer.CleartextSign(bytes.NewReader(releaseData))
	if err != nil {
		return nil, fmt.Errorf("failed to create cleartext signature: %w", err)
	}

	inReleaseData, err := io.ReadAll(inReleaseReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read InRelease data: %w", err)
	}

	return &SignedRelease{
		Release:     releaseData,
		InRelease:   inReleaseData,
		ReleaseGPG:  detachedSig,
		Fingerprint: rs.signer.KeyFingerprint(),
	}, nil
}

type SignedRelease struct {
	Release     []byte
	InRelease   []byte
	ReleaseGPG  []byte
	Fingerprint string
}
