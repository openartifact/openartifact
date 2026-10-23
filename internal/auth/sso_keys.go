// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/pkg/logger"
)

// SettingHMACKey is the system_settings key holding the generated login-state
// HMAC key (encrypted with internal/crypto when a secret key is set).
const SettingHMACKey = "auth.hmac_key"

// SettingsStore is the part of the database the generated SSO keys need.
type SettingsStore interface {
	GetSystemSetting(ctx context.Context, key string) (string, error)
	CreateSystemSettingIfAbsent(ctx context.Context, key, value string) (bool, error)
}

// ErrNoSettingsStore is returned when a generated key is needed but there is
// no database to persist it in.
var ErrNoSettingsStore = errors.New("no settings store to persist the generated key")

// loadOrCreateSetting returns the plaintext value stored under key. When
// absent, generate produces one that is stored (encrypted) unless another
// replica stored one first, in which case that winner is used: every replica
// ends up with the same value.
func loadOrCreateSetting(ctx context.Context, store SettingsStore, key string, generate func() (string, error)) (string, error) {
	if store == nil {
		return "", ErrNoSettingsStore
	}

	stored, err := store.GetSystemSetting(ctx, key)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", key, err)
	}

	if stored == "" {
		value, err := generate()
		if err != nil {
			return "", err
		}

		enc, err := crypto.Encrypt(value)
		if err != nil {
			return "", fmt.Errorf("encrypt %s: %w", key, err)
		}

		if _, err := store.CreateSystemSettingIfAbsent(ctx, key, enc); err != nil {
			return "", fmt.Errorf("store %s: %w", key, err)
		}

		// Re-read: when two replicas raced, both must use the stored winner.
		if stored, err = store.GetSystemSetting(ctx, key); err != nil {
			return "", fmt.Errorf("read %s: %w", key, err)
		}
	}

	plain, err := crypto.Decrypt(stored)
	if err != nil {
		return "", fmt.Errorf("decrypt %s: %w", key, err)
	}

	return plain, nil
}

// ResolveHMACKey returns the key that signs SAML RelayState and request
// trackers. A configured key (AUTH_HMAC_KEY) wins; otherwise a random key is
// generated once and stored in system_settings, so restarts and every replica
// share it.
func ResolveHMACKey(ctx context.Context, store SettingsStore, configured string, log *logger.Logger) ([]byte, error) {
	if configured != "" {
		return []byte(configured), nil
	}

	key, err := loadOrCreateSetting(ctx, store, SettingHMACKey, func() (string, error) {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return "", fmt.Errorf("generate hmac key: %w", err)
		}

		return hex.EncodeToString(raw), nil
	})
	if err != nil {
		return nil, err
	}

	if log != nil {
		log.Infow("AUTH_HMAC_KEY not set; using the generated key stored in the database (shared by all replicas)")

		if !crypto.Enabled() {
			log.Warnw("OPENARTIFACT_SECRET_KEY not set; the generated HMAC key is stored unencrypted")
		}
	}

	return []byte(key), nil
}
