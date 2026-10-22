// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"fmt"
	"testing"

	"github.com/openartifact/openartifact/pkg/logger"
)

func newTestScanner(t *testing.T) *OSVScanner {
	t.Helper()

	log, _ := logger.New("error", "json", "stderr")

	return NewOSVScanner(log)
}

// Test ecosystem mapping
func TestMapEcosystem(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"npm", "npm"},
		{"pypi", "PyPI"},
		{"go", "Go"},
		{"cargo", "crates.io"},
		{"rubygems", "RubyGems"},
		{"maven", "Maven"},
		{"nuget", "NuGet"},
		{"deb", "Debian"},
		{"rpm", "Red Hat"},
		{"helm", "Helm"},
		{"unknown", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := mapEcosystem(tt.input)
			if got != tt.expected {
				t.Errorf("mapEcosystem(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// Test CVSS score to severity conversion
func TestCvssScoreToSeverity(t *testing.T) {
	tests := []struct {
		score    float64
		expected string
	}{
		{9.5, "CRITICAL"},
		{9.0, "CRITICAL"},
		{7.5, "HIGH"},
		{7.0, "HIGH"},
		{5.0, "MEDIUM"},
		{4.0, "MEDIUM"},
		{2.0, "LOW"},
		{0.5, "LOW"},
		{0.0, "UNKNOWN"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%.1f", tt.score), func(t *testing.T) {
			got := cvssScoreToSeverity(tt.score)
			if got != tt.expected {
				t.Errorf("cvssScoreToSeverity(%.1f) = %q, want %q", tt.score, got, tt.expected)
			}
		})
	}
}

// Test scanner construction
func TestNewScanner_NotNil(t *testing.T) {
	s := newTestScanner(t)
	if s == nil {
		t.Fatal("expected non-nil scanner")
	}

	if s.osvBaseURL == "" {
		t.Error("expected non-empty OSV base URL")
	}
}

// Test OSV base URL is set correctly
func TestNewScanner_OSVBaseURL(t *testing.T) {
	s := newTestScanner(t)
	if s.osvBaseURL != "https://api.osv.dev/v1" {
		t.Errorf("expected OSV base URL to be https://api.osv.dev/v1, got %q", s.osvBaseURL)
	}
}

// Test HTTP client is initialized
func TestNewScanner_HTTPClient(t *testing.T) {
	s := newTestScanner(t)
	if s.httpClient == nil {
		t.Error("expected non-nil HTTP client")
	}
}

// Test CVSS score extraction
func TestExtractCVSSScore(t *testing.T) {
	tests := []struct {
		name     string
		vector   string
		expected float64
	}{
		{
			name:     "CVSS with numeric score",
			vector:   "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H",
			expected: 3.1,
		},
		{
			name:     "CVSS with 3 high components",
			vector:   "CVSS:3.0/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H",
			expected: 3.0,
		},
		{
			name:     "CVSS with 2 high components",
			vector:   "CVSS:3.0/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:L",
			expected: 3.0,
		},
		{
			name:     "CVSS with 1 high component",
			vector:   "CVSS:3.0/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:L/A:L",
			expected: 3.0,
		},
		{
			name:     "CVSS with no high components",
			vector:   "CVSS:3.0/AV:N/AC:L/PR:N/UI:N/S:U/C:L/I:L/A:L",
			expected: 3.0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCVSSScore(tt.vector)
			if got != tt.expected {
				t.Errorf("extractCVSSScore(%q) = %f, want %f", tt.vector, got, tt.expected)
			}
		})
	}
}

// Test ecosystem mapping case insensitivity
func TestMapEcosystem_CaseInsensitive(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"NPM", "npm"},
		{"PyPI", "PyPI"},
		{"GO", "Go"},
		{"Cargo", "crates.io"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := mapEcosystem(tt.input)
			if got != tt.expected {
				t.Errorf("mapEcosystem(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// Test severity boundary conditions
func TestCvssScoreToSeverity_Boundaries(t *testing.T) {
	tests := []struct {
		score    float64
		expected string
	}{
		{8.99, "HIGH"},
		{9.0, "CRITICAL"},
		{6.99, "MEDIUM"},
		{7.0, "HIGH"},
		{3.99, "LOW"},
		{4.0, "MEDIUM"},
		{0.09, "UNKNOWN"},
		{0.1, "LOW"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%.2f", tt.score), func(t *testing.T) {
			got := cvssScoreToSeverity(tt.score)
			if got != tt.expected {
				t.Errorf("cvssScoreToSeverity(%.2f) = %q, want %q", tt.score, got, tt.expected)
			}
		})
	}
}
