// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func brandingEnvKeys() []string {
	return []string{
		"OPENARTIFACT_BRANDING_PRODUCT_NAME", "OPENARTIFACT_BRANDING_LOGO_LIGHT", "OPENARTIFACT_BRANDING_LOGO_DARK",
		"OPENARTIFACT_BRANDING_FAVICON", "OPENARTIFACT_BRANDING_PRIMARY_COLOR", "OPENARTIFACT_BRANDING_LOGIN_MESSAGE",
		"OPENARTIFACT_BRANDING_SUPPORT_URL", "OPENARTIFACT_BRANDING_SOURCE_URL",
	}
}

func clearBrandingEnv(t *testing.T) {
	t.Helper()

	for _, k := range brandingEnvKeys() {
		t.Setenv(k, "")

		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

func loadYAML(t *testing.T, yaml string) (*Config, error) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	return Load(path)
}

func TestLoad_BrandingDefaultsAreEmpty(t *testing.T) {
	clearBrandingEnv(t)

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}

	if cfg.Branding.ProductName != "" || cfg.Branding.LogoLight != "" || cfg.Branding.PrimaryColor != "" || cfg.Branding.SourceURL != "" {
		t.Errorf("branding must be unset by default: %+v", cfg.Branding)
	}
}

func TestLoad_BrandingYAMLAndEnvOverride(t *testing.T) {
	clearBrandingEnv(t)

	cfg, err := loadYAML(t, `
branding:
  productName: Acme Artifacts
  logoLight: /etc/brand/light.svg
  logoDark: /etc/brand/dark.svg
  favicon: /etc/brand/favicon.png
  primaryColor: "#0f766e"
  loginMessage: Welcome
  supportUrl: https://help.acme.test
  sourceUrl: https://git.acme.test/oa
`)
	if err != nil {
		t.Fatal(err)
	}

	b := cfg.Branding
	if b.ProductName != "Acme Artifacts" || b.LogoLight != "/etc/brand/light.svg" || b.LogoDark != "/etc/brand/dark.svg" ||
		b.Favicon != "/etc/brand/favicon.png" || b.PrimaryColor != "#0f766e" || b.LoginMessage != "Welcome" ||
		b.SupportURL != "https://help.acme.test" || b.SourceURL != "https://git.acme.test/oa" {
		t.Fatalf("yaml branding = %+v", b)
	}

	t.Setenv("OPENARTIFACT_BRANDING_PRODUCT_NAME", "From Env")
	t.Setenv("OPENARTIFACT_BRANDING_LOGO_LIGHT", "/env/light.png")
	t.Setenv("OPENARTIFACT_BRANDING_LOGO_DARK", "/env/dark.png")
	t.Setenv("OPENARTIFACT_BRANDING_FAVICON", "/env/fav.png")
	t.Setenv("OPENARTIFACT_BRANDING_PRIMARY_COLOR", "#112233")
	t.Setenv("OPENARTIFACT_BRANDING_LOGIN_MESSAGE", "Hello")
	t.Setenv("OPENARTIFACT_BRANDING_SUPPORT_URL", "mailto:it@acme.test")
	t.Setenv("OPENARTIFACT_BRANDING_SOURCE_URL", "https://example.test/src")

	cfg, err = loadYAML(t, "branding:\n  productName: Acme Artifacts\n")
	if err != nil {
		t.Fatal(err)
	}

	b = cfg.Branding
	if b.ProductName != "From Env" || b.LogoLight != "/env/light.png" || b.LogoDark != "/env/dark.png" || b.Favicon != "/env/fav.png" ||
		b.PrimaryColor != "#112233" || b.LoginMessage != "Hello" || b.SupportURL != "mailto:it@acme.test" || b.SourceURL != "https://example.test/src" {
		t.Errorf("env must override yaml: %+v", b)
	}
}

func TestLoad_BrandingInvalidIsRejected(t *testing.T) {
	clearBrandingEnv(t)

	for name, yaml := range map[string]string{
		"colour":  "branding:\n  primaryColor: blue\n",
		"support": "branding:\n  supportUrl: javascript:alert(1)\n",
		"source":  "branding:\n  sourceUrl: ftp://x\n",
		"name":    "branding:\n  productName: \"" + strings.Repeat("x", 80) + "\"\n",
	} {
		cfg, err := loadYAML(t, yaml)
		if err != nil {
			t.Fatalf("%s: Load: %v", name, err)
		}

		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "branding") {
			t.Errorf("%s: Validate = %v, want a branding error", name, err)
		}
	}

	t.Setenv("OPENARTIFACT_BRANDING_PRIMARY_COLOR", "#12")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "branding.primaryColor") {
		t.Errorf("env colour: Validate = %v", err)
	}
}
