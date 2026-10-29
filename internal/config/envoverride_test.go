// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsUnparsableEnv(t *testing.T) {
	tests := []struct{ key, val string }{
		{"SERVER_PORT", "80x80-secret"},
		{"SERVER_TLS_ENABLED", "maybe-secret"},
		{"DB_CONNECT_TIMEOUT", "soon-secret"},
		{"SERVER_MAX_BODY_BYTES", "1.5-secret"},
		{"DB_OPTIONS", "novalue-secret"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Setenv(tt.key, tt.val)

			_, err := Load("")
			if !errors.Is(err, errInvalidEnv) {
				t.Fatalf("Load() err = %v, want errInvalidEnv", err)
			}

			if !strings.Contains(err.Error(), tt.key) {
				t.Errorf("error %q does not name %s", err, tt.key)
			}

			if strings.Contains(err.Error(), "secret") {
				t.Errorf("error %q leaks the value", err)
			}
		})
	}
}

func TestLoadBlankScalarEnvIsUnset(t *testing.T) {
	t.Setenv("SERVER_PORT", " ")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Server.Port != 8080 {
		t.Errorf("port = %d, want default 8080", cfg.Server.Port)
	}
}

func TestEmptyListEnvClearsYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(
		path,
		[]byte("server:\n  corsOrigins: [\"https://a\"]\n  trustedProxies: [\"10.0.0.0/8\"]\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil || len(cfg.Server.CORSOrigins) != 1 {
		t.Fatalf("baseline: %v %v", err, cfg.Server.CORSOrigins)
	}

	t.Setenv("CORS_ORIGINS", "")
	t.Setenv("OPENARTIFACT_TRUSTED_PROXIES", " , ")

	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Server.CORSOrigins) != 0 || len(cfg.Server.TrustedProxies) != 0 {
		t.Errorf("lists not cleared: %v %v", cfg.Server.CORSOrigins, cfg.Server.TrustedProxies)
	}
}

func TestServerLimitsDefaultsAndEnv(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Server.IdleTimeout != 120 || cfg.Server.MaxHeaderBytes != 1<<20 {
		t.Errorf("defaults = %d/%d", cfg.Server.IdleTimeout, cfg.Server.MaxHeaderBytes)
	}

	t.Setenv("SERVER_IDLE_TIMEOUT", "30")
	t.Setenv("SERVER_MAX_HEADER_BYTES", "65536")

	cfg, err = Load("")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Server.IdleTimeout != 30 || cfg.Server.MaxHeaderBytes != 65536 {
		t.Errorf("env = %d/%d", cfg.Server.IdleTimeout, cfg.Server.MaxHeaderBytes)
	}
}

func TestServerLimitsValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ServerConfig)
		bad    bool
	}{
		{"defaults", func(*ServerConfig) {}, false},
		{"zero timeouts", func(s *ServerConfig) { s.IdleTimeout, s.ReadTimeout = 0, 0 }, false},
		{"negative read", func(s *ServerConfig) { s.ReadTimeout = -1 }, true},
		{"negative write", func(s *ServerConfig) { s.WriteTimeout = -1 }, true},
		{"negative header", func(s *ServerConfig) { s.ReadHeaderTimeout = -5 }, true},
		{"negative idle", func(s *ServerConfig) { s.IdleTimeout = -1 }, true},
		{"negative shutdown", func(s *ServerConfig) { s.ShutdownTimeout = -1 }, true},
		{"negative delay", func(s *ServerConfig) { s.ShutdownDelay = -1 }, true},
		{"huge idle", func(s *ServerConfig) { s.IdleTimeout = maxServerSeconds + 1 }, true},
		{"negative header bytes", func(s *ServerConfig) { s.MaxHeaderBytes = -1 }, true},
		{"tiny header bytes", func(s *ServerConfig) { s.MaxHeaderBytes = 10 }, true},
		{"huge header bytes", func(s *ServerConfig) { s.MaxHeaderBytes = maxMaxHeaderBytes + 1 }, true},
		{"zero header bytes", func(s *ServerConfig) { s.MaxHeaderBytes = 0 }, false},
		{"negative body", func(s *ServerConfig) { s.MaxBodyBytes = -1 }, true},
		{"negative rps", func(s *ServerConfig) { s.RateLimitRPS = -1 }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Defaults()
			tt.mutate(&cfg.Server)

			err := cfg.Validate()
			if tt.bad != errors.Is(err, errInvalidServerLimit) {
				t.Fatalf("Validate() = %v, bad=%v", err, tt.bad)
			}
		})
	}
}
