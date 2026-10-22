// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"strconv"
)

// SettingProjectsSelfCreate is the system setting that lets any authenticated
// human user create a project (and become its owner). When unset, the
// projects.selfCreate config value (OPENARTIFACT_PROJECTS_SELF_CREATE) decides.
const SettingProjectsSelfCreate = "projects.self_create"

// SettingStore reads and writes system settings (database.Database satisfies it).
type SettingStore interface {
	GetSystemSetting(ctx context.Context, key string) (string, error)
	SetSystemSetting(ctx context.Context, key, value string) error
}

// ProjectSettings is the effective project configuration: stored system
// settings layered over the config defaults.
type ProjectSettings struct {
	store      SettingStore
	envDefault bool
}

// NewProjectSettings returns the project settings; selfCreateDefault is the
// config/env value used until an administrator saves the setting.
func NewProjectSettings(store SettingStore, selfCreateDefault bool) *ProjectSettings {
	return &ProjectSettings{store: store, envDefault: selfCreateDefault}
}

// SelfCreate reports whether any authenticated user may create projects. A
// stored value wins over the config default; an unreadable or malformed value
// fails closed (off), because it widens who may create projects.
func (s *ProjectSettings) SelfCreate(ctx context.Context) (bool, error) {
	if s == nil || s.store == nil {
		return false, nil
	}

	raw, err := s.store.GetSystemSetting(ctx, SettingProjectsSelfCreate)
	if err != nil {
		return false, err //nolint:wrapcheck // database error, logged by callers
	}

	if raw == "" {
		return s.envDefault, nil
	}

	on, perr := strconv.ParseBool(raw)
	if perr != nil {
		return false, nil //nolint:nilerr // malformed stored value fails closed
	}

	return on, nil
}

// SetSelfCreate stores the setting; it overrides the config default from now on.
func (s *ProjectSettings) SetSelfCreate(ctx context.Context, on bool) error {
	return s.store.SetSystemSetting(ctx, SettingProjectsSelfCreate, strconv.FormatBool(on)) //nolint:wrapcheck // database error, logged by callers
}
