// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"io"
	"testing"

	formatrpm "github.com/openartifact/openartifact/internal/format/rpm"
)

func TestSignerNilSigner(t *testing.T) {
	_, err := formatrpm.SignRepomdXML(nil, []byte("<repomd/>"))
	if err == nil {
		t.Error("SignRepomdXML(nil signer) expected error, got nil")
	}

	_, err = formatrpm.PublicKey(nil)
	if err == nil {
		t.Error("PublicKey(nil signer) expected error, got nil")
	}
}

func TestSignerEmptyData(t *testing.T) {
	mockSigner := &nilSigner{}

	_, err := formatrpm.SignRepomdXML(mockSigner, nil)
	if err == nil {
		t.Error("SignRepomdXML(empty data) expected error, got nil")
	}

	_, err = formatrpm.SignRepomdXML(mockSigner, []byte{})
	if err == nil {
		t.Error("SignRepomdXML(empty data) expected error, got nil")
	}
}

type nilSigner struct{}

func (n *nilSigner) CleartextSign(data io.Reader) (io.Reader, error) { return nil, nil }
func (n *nilSigner) DetachedSign(data io.Reader) ([]byte, error)     { return nil, nil }
func (n *nilSigner) PublicKey() ([]byte, error)                      { return nil, nil }
func (n *nilSigner) KeyFingerprint() string                          { return "" }
