// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package license

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

func newTestLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	return log
}

func TestNormalizeLicense(t *testing.T) {
	d := NewDetector(newTestLogger(t))

	cases := []struct {
		input string
		want  string
	}{
		{"mit", "MIT"},
		{"MIT", "MIT"},
		{"mit license", "MIT"},
		{"apache 2.0", "Apache-2.0"},
		{"apache-2.0", "Apache-2.0"},
		{"gpl-2.0", "GPL-2.0-only"},
		{"gpl-3.0", "GPL-3.0-only"},
		{"lgpl-2.1", "LGPL-2.1-only"},
		{"bsd-2-clause", "BSD-2-Clause"},
		{"unlicense", "Unlicense"},
		{"agpl-3.0", "AGPL-3.0-only"},
		{"eupl-1.2", "EUPL-1.2"},
		// Unknown → return original trimmed
		{"MyCustomLicense", "MyCustomLicense"},
		{"  MIT  ", "MIT"},
		{"", ""},
	}
	for _, c := range cases {
		got := d.NormalizeLicense(c.input)
		if got != c.want {
			t.Errorf("NormalizeLicense(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestDetectFromMetadata(t *testing.T) {
	d := NewDetector(newTestLogger(t))

	t.Run("string value", func(t *testing.T) {
		meta := map[string]any{"license": "mit"}

		got := d.DetectFromMetadata(meta)
		if len(got) != 1 || got[0] != "MIT" {
			t.Errorf("got %v, want [MIT]", got)
		}
	})

	t.Run("slice of interface", func(t *testing.T) {
		meta := map[string]any{"licenses": []any{"mit", "apache 2.0"}}

		got := d.DetectFromMetadata(meta)
		if len(got) != 2 {
			t.Errorf("got %v, want 2 items", got)
		}
	})

	t.Run("slice of string", func(t *testing.T) {
		meta := map[string]any{"licenses": []string{"MIT", "ISC"}}

		got := d.DetectFromMetadata(meta)
		if len(got) != 2 {
			t.Errorf("got %v, want 2 items", got)
		}
	})

	t.Run("missing key returns empty", func(t *testing.T) {
		got := d.DetectFromMetadata(map[string]any{})
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})

	t.Run("deduplication across keys", func(t *testing.T) {
		meta := map[string]any{
			"license":  "mit",
			"licenses": []any{"mit"},
		}

		got := d.DetectFromMetadata(meta)
		if len(got) != 1 {
			t.Errorf("got %v, want 1 (deduplicated)", got)
		}
	})

	t.Run("spdx_id key", func(t *testing.T) {
		meta := map[string]any{"spdx_id": "Apache-2.0"}

		got := d.DetectFromMetadata(meta)
		if len(got) != 1 || got[0] != "Apache-2.0" {
			t.Errorf("got %v, want [Apache-2.0]", got)
		}
	})

	t.Run("empty string ignored", func(t *testing.T) {
		meta := map[string]any{"license": ""}

		got := d.DetectFromMetadata(meta)
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})
}

func TestEvaluatePolicy(t *testing.T) {
	d := NewDetector(newTestLogger(t))
	licenses := []string{"MIT", "Apache-2.0"}

	t.Run("nil policy is compliant", func(t *testing.T) {
		ok, violations := d.EvaluatePolicy(licenses, nil)
		if !ok || len(violations) != 0 {
			t.Errorf("nil policy should be compliant, got ok=%v violations=%v", ok, violations)
		}
	})

	t.Run("disabled policy is compliant", func(t *testing.T) {
		policy := &models.LicensePolicy{Enabled: false, DeniedLicenses: []string{"MIT"}}

		ok, violations := d.EvaluatePolicy(licenses, policy)
		if !ok || len(violations) != 0 {
			t.Errorf("disabled policy should be compliant, got ok=%v violations=%v", ok, violations)
		}
	})

	t.Run("denied license causes violation", func(t *testing.T) {
		policy := &models.LicensePolicy{Enabled: true, DeniedLicenses: []string{"MIT"}}

		ok, violations := d.EvaluatePolicy(licenses, policy)
		if ok || len(violations) == 0 {
			t.Errorf("MIT should be denied, got ok=%v violations=%v", ok, violations)
		}
	})

	t.Run("allowed list with matching license", func(t *testing.T) {
		policy := &models.LicensePolicy{Enabled: true, AllowedLicenses: []string{"MIT"}}

		ok, violations := d.EvaluatePolicy(licenses, policy)
		if !ok || len(violations) != 0 {
			t.Errorf("MIT is in allowed list, should be compliant, got ok=%v violations=%v", ok, violations)
		}
	})

	t.Run("allowed list with no matching license", func(t *testing.T) {
		policy := &models.LicensePolicy{Enabled: true, AllowedLicenses: []string{"GPL-3.0-only"}}

		ok, violations := d.EvaluatePolicy([]string{"MIT"}, policy)
		if ok || len(violations) == 0 {
			t.Errorf("MIT not in allowed list, should be violation, got ok=%v violations=%v", ok, violations)
		}
	})

	t.Run("empty licenses with allowed list is not violation", func(t *testing.T) {
		policy := &models.LicensePolicy{Enabled: true, AllowedLicenses: []string{"MIT"}}
		ok, violations := d.EvaluatePolicy([]string{}, policy)
		// no licenses, allowed check skipped (len(licenses)==0)
		if !ok || len(violations) != 0 {
			t.Errorf("empty licenses should not trigger allowed-list violation, got ok=%v violations=%v", ok, violations)
		}
	})
}

func TestEvaluatePolicy_CompoundExpressionDenied(t *testing.T) {
	d := NewDetector(nil)
	ok, v := d.EvaluatePolicy([]string{"GPL-3.0-only AND MIT"}, &models.LicensePolicy{Enabled: true, DeniedLicenses: []string{"gpl-3.0-only"}})
	if ok || len(v) == 0 {
		t.Fatalf("compound expression must be denied, ok=%v v=%v", ok, v)
	}
}
