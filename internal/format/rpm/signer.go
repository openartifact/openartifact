// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/pkg/gpg"
)

// Sentinel errors for RPM signer.
var (
	ErrSignerNil   = errors.New("rpm: signer is nil")
	ErrEmptyRepomd = errors.New("rpm: empty repomd.xml")
)

func SignRepomdXML(signer gpg.Signer, repomdXML []byte) ([]byte, error) {
	if signer == nil {
		return nil, ErrSignerNil
	}

	if len(repomdXML) == 0 {
		return nil, ErrEmptyRepomd
	}

	signature, err := signer.DetachedSign(bytes.NewReader(repomdXML))
	if err != nil {
		return nil, fmt.Errorf("rpm: signing repomd.xml: %w", err)
	}

	return signature, nil
}

func PublicKey(signer gpg.Signer) ([]byte, error) {
	if signer == nil {
		return nil, ErrSignerNil
	}

	return signer.PublicKey()
}
