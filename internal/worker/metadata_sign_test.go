// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"errors"
	"io"
	"testing"

	"github.com/openartifact/openartifact/pkg/gpg"
)

var errSignFailed = errors.New("sign failed")

// failingSigner embeds the Signer interface; only the signing methods are used.
type failingSigner struct{ gpg.Signer }

func (failingSigner) CleartextSign(io.Reader) (io.Reader, error) { return nil, errSignFailed }
func (failingSigner) DetachedSign(io.Reader) ([]byte, error)     { return nil, errSignFailed }

func TestSigningFailureAddsNoMetadata(t *testing.T) {
	tests := []struct {
		name string
		sign func(blobs map[string][]byte) error
	}{
		{"release", func(b map[string][]byte) error {
			return addSignedRelease(b, "dists/stable/", []byte("Origin: x\n"), failingSigner{})
		}},
		{"repomd", func(b map[string][]byte) error {
			return addSignedRepomd(b, []byte("<repomd/>"), failingSigner{})
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blobs := map[string][]byte{}

			if err := tt.sign(blobs); !errors.Is(err, errSignFailed) {
				t.Fatalf("error = %v, want the signing error", err)
			}

			if len(blobs) != 0 {
				t.Errorf("blobs = %v, want none", blobs)
			}
		})
	}
}
