// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"errors"
	"testing"
)

func TestServerRoleDefaultsToAll(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.Server.Role != RoleAll {
		t.Errorf("default role = %q, want %q", cfg.Server.Role, RoleAll)
	}

	if !cfg.Server.ServesAPI() || !cfg.Server.RunsWorkers() {
		t.Error("role all must serve the API and run the workers")
	}
}

func TestServerRoleEnvOverride(t *testing.T) {
	t.Setenv("OPENARTIFACT_ROLE", RoleWorker)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.Server.Role != RoleWorker {
		t.Errorf("role = %q, want %q", cfg.Server.Role, RoleWorker)
	}
}

func TestServerRoleCapabilities(t *testing.T) {
	tests := []struct {
		role    string
		api     bool
		workers bool
	}{
		{role: RoleAll, api: true, workers: true},
		{role: "", api: true, workers: true},
		{role: RoleWeb, api: true, workers: false},
		{role: RoleWorker, api: false, workers: true},
	}

	for _, tc := range tests {
		s := ServerConfig{Role: tc.role}

		if s.ServesAPI() != tc.api || s.RunsWorkers() != tc.workers {
			t.Errorf("role %q: ServesAPI=%v RunsWorkers=%v, want %v %v", tc.role, s.ServesAPI(), s.RunsWorkers(), tc.api, tc.workers)
		}
	}
}

func TestValidateRejectsUnknownRole(t *testing.T) {
	for _, role := range []string{"Web", "api", "scheduler", " worker"} {
		cfg := defaultConfig()
		cfg.Server.Role = role

		if err := cfg.Validate(); !errors.Is(err, errInvalidRole) {
			t.Errorf("Validate() with role %q = %v, want errInvalidRole", role, err)
		}
	}

	for _, role := range []string{RoleAll, RoleWeb, RoleWorker, ""} {
		cfg := defaultConfig()
		cfg.Server.Role = role
		cfg.Database.Password = "secret"

		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() with role %q = %v, want nil", role, err)
		}
	}
}
