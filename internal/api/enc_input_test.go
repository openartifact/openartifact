// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"testing"

	"github.com/openartifact/openartifact/internal/crypto"
)

// A submitted credential that already looks like a stored ciphertext must be
// refused at every boundary that seals it, so the sealing path cannot be used
// to smuggle a stored value into a field that is later decrypted for the caller.
func TestSealedLookingCredentialsAreRejected(t *testing.T) {
	if err := crypto.Init("0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = crypto.Init("") })

	sealed, err := crypto.Encrypt("hunter2")
	if err != nil || !crypto.IsEncrypted(sealed) {
		t.Fatalf("setup: Encrypt = %q, %v", sealed, err)
	}

	t.Run("upstream password", func(t *testing.T) {
		if _, err := encryptUpstreamPassword(sealed); !errors.As(err, new(validationError)) {
			t.Fatalf("encryptUpstreamPassword(sealed) = %v, want a validation error", err)
		}
	})

	t.Run("webhook secret", func(t *testing.T) {
		if _, err := encryptWebhookSecret(sealed); !errors.As(err, new(validationError)) {
			t.Fatalf("encryptWebhookSecret(sealed) = %v, want a validation error", err)
		}
	})

	t.Run("storage backend secret", func(t *testing.T) {
		_, err := prepareStorageConfig(t.Context(), "s3", []byte(`{"secret_key":"`+sealed+`"}`), nil)
		if !errors.As(err, new(validationError)) {
			t.Fatalf("prepareStorageConfig(sealed secret_key) = %v, want a validation error", err)
		}
	})

	t.Run("plaintext still sealed", func(t *testing.T) {
		got, err := encryptUpstreamPassword("hunter2")
		if err != nil || !crypto.IsEncrypted(got) {
			t.Fatalf("encryptUpstreamPassword(plain) = %q, %v; want ciphertext", got, err)
		}
	})
}
