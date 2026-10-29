// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
)

// SettingsStore owns system settings.
type SettingsStore struct{ *core }

// GetSystemSetting retrieves a system setting by key.
func (p *SettingsStore) GetSystemSetting(ctx context.Context, key string) (string, error) {
	value, err := p.q.GetSystemSetting(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("failed to get system setting: %w", err)
	}

	return value, nil
}

// SetSystemSetting sets or updates a system setting.
func (p *SettingsStore) SetSystemSetting(ctx context.Context, key, value string) error {
	err := p.q.SetSystemSetting(ctx, dbsqlc.SetSystemSettingParams{
		Key:   key,
		Value: value,
	})
	if err != nil {
		return fmt.Errorf("failed to set system setting: %w", err)
	}

	return nil
}

// CreateSystemSettingIfAbsent stores value under key unless the key exists; it
// reports whether it was stored. Concurrent callers (several replicas) agree on one winner.
func (p *SettingsStore) CreateSystemSettingIfAbsent(ctx context.Context, key, value string) (bool, error) {
	n, err := p.q.CreateSystemSettingIfAbsent(ctx, dbsqlc.CreateSystemSettingIfAbsentParams{Key: key, Value: value})
	if err != nil {
		return false, fmt.Errorf("failed to create system setting: %w", err)
	}

	return n > 0, nil
}
