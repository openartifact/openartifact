// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package dtrack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
)

// SettingGlobal is the system_settings key of the global configuration.
const SettingGlobal = "dependency_track.global"

// Default project naming: one Dependency-Track project per artifact,
// "<repository>/<name>", at the artifact's version.
const (
	DefaultProjectName    = "{repo}/{name}"
	DefaultProjectVersion = "{version}"
	maxProjectField       = 255
)

// Sources of an effective configuration.
const (
	SourceNone       = "none"
	SourceGlobal     = "global"
	SourceRepository = "repository"
)

// Settings is a Dependency-Track configuration in plaintext (the API key is
// encrypted at rest).
type Settings struct {
	Enabled        bool   `json:"enabled"`
	BaseURL        string `json:"base_url"`
	APIKey         string `json:"api_key"`
	ProjectName    string `json:"project_name"`
	ProjectVersion string `json:"project_version"`
	AutoPush       bool   `json:"auto_push"`
	PushVEX        bool   `json:"push_vex"`
}

// SettingsStore is the part of the database the configuration lives in.
type SettingsStore interface {
	GetSystemSetting(ctx context.Context, key string) (string, error)
	SetSystemSetting(ctx context.Context, key, value string) error
	GetRepositoryDependencyTrack(ctx context.Context, repoID string) (*models.RepositoryDependencyTrack, error)
}

// LoadGlobal reads the global settings; none stored yields the zero value (disabled).
func LoadGlobal(ctx context.Context, db SettingsStore) (Settings, error) {
	raw, err := db.GetSystemSetting(ctx, SettingGlobal)
	if err != nil {
		return Settings{}, fmt.Errorf("read dependency-track settings: %w", err)
	}

	if raw == "" {
		return Settings{}, nil
	}

	var s Settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Settings{}, fmt.Errorf("decode dependency-track settings: %w", err)
	}

	if s.APIKey, err = crypto.Decrypt(s.APIKey); err != nil {
		return Settings{}, fmt.Errorf("decrypt dependency-track API key: %w", err)
	}

	return s, nil
}

// SaveGlobal stores the global settings, encrypting the API key.
func SaveGlobal(ctx context.Context, db SettingsStore, s Settings) error {
	enc, err := crypto.Encrypt(s.APIKey)
	if err != nil {
		return fmt.Errorf("encrypt dependency-track API key: %w", err)
	}

	s.APIKey = enc

	raw, err := json.Marshal(s) //nolint:gosec // APIKey is the encrypted value
	if err != nil {
		return fmt.Errorf("encode dependency-track settings: %w", err)
	}

	if err := db.SetSystemSetting(ctx, SettingGlobal, string(raw)); err != nil {
		return fmt.Errorf("store dependency-track settings: %w", err)
	}

	return nil
}

// Effective is the configuration that applies to a repository.
type Effective struct {
	Settings

	// Source is where Enabled came from: SourceRepository (an override row),
	// SourceGlobal, or SourceNone (nothing configured).
	Source string
}

// Ready reports whether pushing can work: enabled with a URL and an API key.
func (e Effective) Ready() bool {
	return e.Enabled && e.BaseURL != "" && e.APIKey != ""
}

// Resolve layers the repository override over the global settings. An override
// row applies explicitly (its enabled flag wins, so a repository can opt in or
// out); its empty strings and NULL booleans inherit the global values.
func Resolve(global Settings, repo *models.RepositoryDependencyTrack) (Effective, error) {
	eff := Effective{Settings: global, Source: SourceGlobal}
	if !global.Enabled && global.BaseURL == "" && global.APIKey == "" {
		eff.Source = SourceNone
	}

	if repo == nil {
		return eff, nil
	}

	eff.Source = SourceRepository
	eff.Enabled = repo.Enabled

	if repo.BaseURL != "" {
		eff.BaseURL = repo.BaseURL
	}

	if repo.APIKey != "" {
		key, err := crypto.Decrypt(repo.APIKey)
		if err != nil {
			return Effective{}, fmt.Errorf("decrypt repository dependency-track API key: %w", err)
		}

		eff.APIKey = key
	}

	if repo.ProjectName != "" {
		eff.ProjectName = repo.ProjectName
	}

	if repo.ProjectVersion != "" {
		eff.ProjectVersion = repo.ProjectVersion
	}

	if repo.AutoPush != nil {
		eff.AutoPush = *repo.AutoPush
	}

	if repo.PushVEX != nil {
		eff.PushVEX = *repo.PushVEX
	}

	return eff, nil
}

// EffectiveFor loads the global settings and the repository's override.
func EffectiveFor(ctx context.Context, db SettingsStore, repoID string) (Effective, error) {
	global, err := LoadGlobal(ctx, db)
	if err != nil {
		return Effective{}, err
	}

	row, err := db.GetRepositoryDependencyTrack(ctx, repoID)
	if err != nil {
		return Effective{}, fmt.Errorf("read repository dependency-track settings: %w", err)
	}

	return Resolve(global, row)
}

// ---- project naming ------------------------------------------------------

// ProjectVars are the values a project name / version template can use.
type ProjectVars struct {
	Repo, Name, Version, ArtifactID, Format string
}

var placeholderRe = regexp.MustCompile(`\{([a-z_]*)\}`)

var errTemplate = errors.New("invalid project template")

// ValidateTemplate checks that tmpl only uses the known placeholders
// ({repo}, {name}, {version}, {artifact_id}, {format}), has no stray braces and
// fits Dependency-Track's 255 character limit with a margin for the values.
func ValidateTemplate(tmpl string) error {
	if len(tmpl) > maxProjectField {
		return fmt.Errorf("%w: longer than %d characters", errTemplate, maxProjectField)
	}

	known := map[string]bool{"repo": true, "name": true, "version": true, "artifact_id": true, "format": true}
	rest := tmpl

	for _, m := range placeholderRe.FindAllStringSubmatch(tmpl, -1) {
		if !known[m[1]] {
			return fmt.Errorf("%w: unknown placeholder {%s}", errTemplate, m[1])
		}

		rest = strings.Replace(rest, m[0], "", 1)
	}

	if strings.ContainsAny(rest, "{}") {
		return fmt.Errorf("%w: unbalanced braces", errTemplate)
	}

	return nil
}

// Render expands tmpl (default def when empty) with vars and bounds the result.
// A "unknown" version renders empty.
func Render(tmpl, def string, v ProjectVars) string {
	if tmpl == "" {
		tmpl = def
	}

	if v.Version == "unknown" {
		v.Version = ""
	}

	r := strings.NewReplacer(
		"{repo}", v.Repo, "{name}", v.Name, "{version}", v.Version,
		"{artifact_id}", v.ArtifactID, "{format}", v.Format,
	)

	out := strings.TrimSpace(r.Replace(tmpl))
	if len(out) > maxProjectField {
		out = out[:maxProjectField]
	}

	return out
}
