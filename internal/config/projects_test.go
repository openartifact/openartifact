// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"testing"
)

const projectsSelfCreateEnv = "OPENARTIFACT_PROJECTS_SELF_CREATE"

func loadProjectsConfig(t *testing.T, yaml, env string, envSet bool) *Config {
	t.Helper()

	t.Setenv(projectsSelfCreateEnv, "")

	if envSet {
		t.Setenv(projectsSelfCreateEnv, env)
	} else if err := os.Unsetenv(projectsSelfCreateEnv); err != nil {
		t.Fatal(err)
	}

	path := ""

	if yaml != "" {
		path = filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	return cfg
}

func TestLoad_ProjectsSelfCreate(t *testing.T) {
	tests := []struct {
		name   string
		yaml   string
		env    string
		envSet bool
		want   bool
	}{
		{"default off", "", "", false, false},
		{"yaml on", "projects:\n  selfCreate: true\n", "", false, true},
		{"yaml off", "projects:\n  selfCreate: false\n", "", false, false},
		{"env on", "", "true", true, true},
		{"env overrides yaml on", "projects:\n  selfCreate: true\n", "false", true, false},
		{"env overrides yaml off", "projects:\n  selfCreate: false\n", "1", true, true},
		{"malformed env keeps yaml", "projects:\n  selfCreate: true\n", "maybe", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loadProjectsConfig(t, tt.yaml, tt.env, tt.envSet)
			if cfg.Projects.SelfCreate != tt.want {
				t.Fatalf("Projects.SelfCreate = %v, want %v", cfg.Projects.SelfCreate, tt.want)
			}
		})
	}
}
