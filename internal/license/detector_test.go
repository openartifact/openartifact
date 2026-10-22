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
		// no licenses declared: policies match licenses, they do not require one
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

// A policy written with one SPDX spelling must cover the others: the deny list
// says "GPL-3.0" while the detector normalizes artifacts to "GPL-3.0-only".
func TestEvaluatePolicy_SpellingsOfOneLicenseMatch(t *testing.T) {
	d := NewDetector(nil)

	spellings := []string{
		"GPL-3.0", "GPL-3.0-only", "GPL-3.0+", "GPL-3.0-or-later", "gpl-3.0", "GPLv3", "GPLv3+",
	}
	if got := d.NormalizeLicense("GPLv3"); got != "GPL-3.0-only" {
		t.Fatalf("NormalizeLicense(GPLv3) = %q, want GPL-3.0-only", got)
	}

	for _, inPolicy := range spellings {
		for _, onArtifact := range spellings {
			t.Run(inPolicy+" vs "+onArtifact, func(t *testing.T) {
				deny := &models.LicensePolicy{Enabled: true, DeniedLicenses: []string{inPolicy}}
				if ok, _ := d.EvaluatePolicy([]string{d.NormalizeLicense(onArtifact)}, deny); ok {
					t.Errorf("deny %q must match artifact %q", inPolicy, onArtifact)
				}

				allow := &models.LicensePolicy{Enabled: true, AllowedLicenses: []string{inPolicy}}
				if ok, v := d.EvaluatePolicy([]string{d.NormalizeLicense(onArtifact)}, allow); !ok {
					t.Errorf("allow %q must admit artifact %q: %v", inPolicy, onArtifact, v)
				}
			})
		}
	}

	t.Run("different license of another family is untouched", func(t *testing.T) {
		deny := &models.LicensePolicy{Enabled: true, DeniedLicenses: []string{"GPL-3.0"}}
		for _, other := range []string{"GPL-2.0-only", "LGPL-3.0-only", "AGPL-3.0-only", "MIT"} {
			if ok, v := d.EvaluatePolicy([]string{other}, deny); !ok {
				t.Errorf("%s must not match deny GPL-3.0: %v", other, v)
			}
		}
	})
}

func TestNormalizeLicense_PyPIClassifier(t *testing.T) {
	d := NewDetector(nil)

	cases := map[string]string{
		"License :: OSI Approved :: MIT License":                                             "MIT",
		"License :: OSI Approved :: GNU General Public License v3 (GPLv3)":                   "GPL-3.0-only",
		"License :: OSI Approved :: GNU General Public License v3 or later (GPLv3+)":         "GPL-3.0-or-later",
		"License :: OSI Approved :: Apache Software License":                                 "Apache-2.0",
		"License :: OSI Approved :: GNU Affero General Public License v3 or later (AGPLv3+)": "AGPL-3.0-or-later",
	}
	for in, want := range cases {
		if got := d.NormalizeLicense(in); got != want {
			t.Errorf("NormalizeLicense(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectFromMetadata_MapShapes(t *testing.T) {
	d := NewDetector(nil)

	got := d.DetectFromMetadata(map[string]any{"license": map[string]any{"type": "GPL-3.0"}})
	if len(got) != 1 || got[0] != "GPL-3.0-only" {
		t.Fatalf("map license = %v", got)
	}

	got = d.DetectFromMetadata(map[string]any{"licenses": []any{map[string]any{"type": "MIT"}, map[string]any{"name": "Apache-2.0"}}})
	if len(got) != 2 {
		t.Fatalf("list of maps = %v", got)
	}
}
