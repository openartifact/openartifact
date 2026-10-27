// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpmsign_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/rpmsign"
)

var errBoom = errors.New("boom")

// lockedSigner is a signer whose key cannot be used.
type lockedSigner struct{ gpg.Signer }

func (lockedSigner) SigningKey() (*packet.PrivateKey, error) { return nil, gpg.ErrKeyCannotSign }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errBoom }

// brokenSeeker reads normally but cannot be repositioned.
type brokenSeeker struct{ io.Reader }

func (brokenSeeker) Seek(int64, int) (int64, error) { return 0, errBoom }

// limitedWriter accepts n bytes, then fails.
type limitedWriter struct{ left int }

func (w *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > w.left {
		return 0, errBoom
	}

	w.left -= len(p)

	return len(p), nil
}

func TestSignFailures(t *testing.T) {
	signer, _ := newSigner(t, gpg.AlgorithmED25519)

	var signed bytes.Buffer
	if err := rpmsign.Sign(bytes.NewReader(fixture(t)), &signed, signer); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		src    io.ReadSeeker
		dst    io.Writer
		signer gpg.Signer
		want   error
	}{
		{"signer key unusable", bytes.NewReader(fixture(t)), io.Discard, lockedSigner{}, gpg.ErrKeyCannotSign},
		{"source cannot rewind", brokenSeeker{bytes.NewReader(fixture(t))}, io.Discard, signer, errBoom},
		{"signature header write fails", bytes.NewReader(fixture(t)), failingWriter{}, signer, errBoom},
		{"package body write fails", bytes.NewReader(fixture(t)), &limitedWriter{left: signed.Len() - 1}, signer, errBoom},
		{"empty source", bytes.NewReader(nil), io.Discard, signer, nil},
		{"truncated package", bytes.NewReader(fixture(t)[:200]), io.Discard, signer, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := rpmsign.Sign(tt.src, tt.dst, tt.signer)
			if err == nil {
				t.Fatal("Sign succeeded, want an error")
			}

			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}
