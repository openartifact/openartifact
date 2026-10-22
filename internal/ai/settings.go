// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ai

import (
	"context"
	"strconv"
)

// System setting keys written by the admin API.
const (
	SettingEnabled      = "ai.enabled"
	SettingAutoAnalyze  = "ai.auto_analyze"
	SettingProvider     = "ai.provider"
	SettingBaseURL      = "ai.base_url"
	SettingAPIKey       = "ai.api_key"
	SettingModel        = "ai.model"
	SettingMaxTokens    = "ai.max_tokens"
	SettingTimeoutSecs  = "ai.timeout_secs"
	SettingSystemPrompt = "ai.system_prompt"
)

// SettingReader reads one system setting; an unset key yields "".
type SettingReader interface {
	GetSystemSetting(ctx context.Context, key string) (string, error)
}

// Settings is the effective AI configuration: whether analysis runs, whether
// uploads are analysed unattended, and how to reach the model.
type Settings struct {
	Enabled     bool
	AutoAnalyze bool
	Config      Config
}

// LoadSettings merges the stored system settings over env (the env/YAML
// values). Once ai.enabled has been saved the database decides Enabled and
// every other field uses the stored value when non-empty, falling back to env
// otherwise; without a saved ai.enabled the env values apply as they are.
// ai.auto_analyze is independent: the stored value wins whenever it is set.
func LoadSettings(ctx context.Context, r SettingReader, env Settings) Settings {
	get := func(key string) string {
		v, _ := r.GetSystemSetting(ctx, key)
		return v
	}

	out := env

	if v := get(SettingAutoAnalyze); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			out.AutoAnalyze = b
		}
	}

	rawEnabled := get(SettingEnabled)
	if rawEnabled == "" {
		return out
	}

	if b, err := strconv.ParseBool(rawEnabled); err == nil {
		out.Enabled = b
	} else {
		out.Enabled = false
	}

	cfg := &out.Config

	setString := func(dst *string, key string) {
		if v := get(key); v != "" {
			*dst = v
		}
	}

	setInt := func(dst *int, key string) {
		if v := get(key); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}

	setString(&cfg.Provider, SettingProvider)
	setString(&cfg.BaseURL, SettingBaseURL)
	setString(&cfg.APIKey, SettingAPIKey)
	setString(&cfg.Model, SettingModel)
	setInt(&cfg.MaxTokens, SettingMaxTokens)
	setInt(&cfg.TimeoutSecs, SettingTimeoutSecs)
	setString(&cfg.SystemPrompt, SettingSystemPrompt)

	return out
}

// NewClient returns a client for the settings, or nil when AI is disabled.
func (s Settings) NewClient() *Client {
	if !s.Enabled {
		return nil
	}

	return NewClient(s.Config)
}
