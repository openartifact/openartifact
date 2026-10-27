// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/netutil"
)

type mapSettings map[string]string

func (m mapSettings) GetSystemSetting(_ context.Context, key string) (string, error) {
	return m[key], nil
}

func TestLoadSettings(t *testing.T) {
	t.Parallel()

	env := Settings{
		Enabled:     true,
		AutoAnalyze: true,
		Config:      Config{Provider: "openai", Model: "env-model", MaxTokens: 100, TimeoutSecs: 5, APIKey: "env-key"},
	}

	tests := []struct {
		name  string
		db    mapSettings
		check func(Settings) bool
	}{
		{"nothing stored keeps env", mapSettings{}, func(s Settings) bool { return s == env }},
		{"stored enabled=false disables even if env enables", mapSettings{SettingEnabled: "false"},
			func(s Settings) bool { return !s.Enabled && s.Config.Model == "env-model" && s.AutoAnalyze }},
		{
			"stored fields win, empty ones fall back",
			mapSettings{
				SettingEnabled:   "true",
				SettingModel:     "db-model",
				SettingProvider:  "custom",
				SettingBaseURL:   "http://x",
				SettingMaxTokens: "7",
			},
			func(s Settings) bool {
				return s.Enabled && s.Config.Model == "db-model" && s.Config.Provider == "custom" &&
					s.Config.BaseURL == "http://x" && s.Config.MaxTokens == 7 && s.Config.TimeoutSecs == 5 && s.Config.APIKey == "env-key"
			},
		},
		{"stored fields are ignored until ai.enabled is saved", mapSettings{SettingModel: "db-model"},
			func(s Settings) bool { return s.Config.Model == "env-model" }},
		{"auto_analyze is independent of ai.enabled", mapSettings{SettingAutoAnalyze: "false"},
			func(s Settings) bool { return !s.AutoAnalyze && s.Enabled }},
		{"garbage enabled means off", mapSettings{SettingEnabled: "maybe"}, func(s Settings) bool { return !s.Enabled }},
		{"garbage auto_analyze is ignored", mapSettings{SettingAutoAnalyze: "maybe"}, func(s Settings) bool { return s.AutoAnalyze }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := LoadSettings(context.Background(), tc.db, env); !tc.check(got) {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestSettingsNewClientNilWhenDisabled(t *testing.T) {
	t.Parallel()

	if (Settings{Config: Config{Model: "m"}}).NewClient() != nil {
		t.Error("a disabled configuration must not produce a client")
	}

	if (Settings{Enabled: true, Config: Config{Model: "m"}}).NewClient() == nil {
		t.Error("an enabled configuration must produce a client")
	}
}

func chatServer(t *testing.T, reportedModel string, hits *[]string) *httptest.Server {
	t.Helper()

	t.Setenv(netutil.EnvAllowPrivate, "true")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits = append(*hits, r.URL.Path)

		out := map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": `{"artifact_summary":"s","license_status":"safe"}`}}},
		}
		if reportedModel != "" {
			out["model"] = reportedModel
		}

		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestCustomProviderUsesItsBaseURLAndReportsModel(t *testing.T) {
	var hits []string

	srv := chatServer(t, "provider-snapshot-2026", &hits)

	c := NewClient(Config{Provider: "custom", BaseURL: srv.URL + "/v9/", Model: "configured"})

	_, usage, err := c.Analyze(context.Background(), "manifest")
	if err != nil {
		t.Fatal(err)
	}

	if len(hits) != 1 || hits[0] != "/v9/chat/completions" {
		t.Errorf("hits = %v", hits)
	}

	if usage.Model != "provider-snapshot-2026" || c.Model() != "configured" {
		t.Errorf("usage.Model=%q Model()=%q", usage.Model, c.Model())
	}
}

func TestModelFallsBackToConfiguredWhenProviderReportsNone(t *testing.T) {
	var hits []string

	srv := chatServer(t, "", &hits)

	_, usage, err := NewClient(Config{Provider: "ollama", BaseURL: srv.URL, Model: "llama3"}).Analyze(context.Background(), "m")
	if err != nil || usage.Model != "llama3" {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
}

func TestCustomProviderWithoutBaseURLNeverFallsBackToOpenAI(t *testing.T) {
	t.Parallel()

	if got := NewClient(Config{Provider: "custom", Model: "m", APIKey: "k"}).baseURL; got != "" {
		t.Errorf("baseURL = %q, want empty so the key cannot be sent to api.openai.com", got)
	}
}
