// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestLoad_OSVMirrorDefaultsOffAndValid(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	m := cfg.OSVMirror
	if m.Enabled || m.Interval != 24*time.Hour || m.BaseURL == "" || len(m.Ecosystems) == 0 || m.MaxArchiveBytes <= 0 {
		t.Fatalf("defaults = %+v", m)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
}

func TestLoad_OSVMirrorYAMLAndEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yamlText := "osvMirror:\n  enabled: true\n  baseUrl: http://osv.internal:8080/mirror\n  ecosystems: [npm, PyPI]\n  interval: 12h\n"

	if err := os.WriteFile(path, []byte(yamlText), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	m := cfg.OSVMirror
	if !m.Enabled || m.BaseURL != "http://osv.internal:8080/mirror" || !slices.Equal(m.Ecosystems, []string{"npm", "PyPI"}) ||
		m.Interval != 12*time.Hour {
		t.Fatalf("yaml: %+v", m)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("a private air-gapped base URL must validate: %v", err)
	}

	t.Setenv("OPENARTIFACT_OSV_MIRROR_INTERVAL", "48h")
	t.Setenv("OPENARTIFACT_OSV_MIRROR_ECOSYSTEMS", "Go,crates.io")
	t.Setenv("OPENARTIFACT_OSV_MIRROR_BASE_URL", "https://osv.example/data")

	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("Load with env: %v", err)
	}

	m = cfg.OSVMirror
	if m.Interval != 48*time.Hour || !slices.Equal(m.Ecosystems, []string{"Go", "crates.io"}) || m.BaseURL != "https://osv.example/data" {
		t.Fatalf("env must override yaml: %+v", m)
	}
}

func TestValidate_OSVMirror(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*OSVMirrorConfig)
	}{
		{"interval too short", func(m *OSVMirrorConfig) { m.Interval = time.Minute }},
		{"unsupported ecosystem", func(m *OSVMirrorConfig) { m.Ecosystems = []string{"Debian:11"} }},
		{"no ecosystem", func(m *OSVMirrorConfig) { m.Ecosystems = nil }},
		{"credentials in url", func(m *OSVMirrorConfig) { m.BaseURL = "https://user:pw@osv.example" }},
		{"not http", func(m *OSVMirrorConfig) { m.BaseURL = "ftp://osv.example" }},
		{"relative temp dir", func(m *OSVMirrorConfig) { m.TempDir = "tmp" }},
		{"no size cap", func(m *OSVMirrorConfig) { m.MaxArchiveBytes = 0 }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load("")
			if err != nil {
				t.Fatal(err)
			}

			cfg.OSVMirror.Enabled = true
			tc.mutate(&cfg.OSVMirror)

			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate accepted an invalid osvMirror")
			}

			cfg.OSVMirror.Enabled = false

			if err := cfg.Validate(); err != nil {
				t.Fatalf("a disabled mirror must not be validated: %v", err)
			}
		})
	}
}
