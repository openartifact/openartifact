// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package secrets

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openartifact/openartifact/pkg/logger"
)

func newTestScanner(t *testing.T) *Scanner {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	return NewScanner(log)
}

func TestScanText_AWSAccessKey(t *testing.T) {
	scanner := newTestScanner(t)

	text := "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE"
	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Error("expected to find AWS access key")
	}

	found := false

	for _, f := range findings {
		if f.Type == "aws_access_key" {
			found = true

			if f.Severity != "critical" {
				t.Errorf("expected severity=critical, got %s", f.Severity)
			}

			break
		}
	}

	if !found {
		t.Error("expected to find aws_access_key type")
	}
}

func TestScanText_AWSSecretKey(t *testing.T) {
	scanner := newTestScanner(t)

	text := `aws_secret_access_key = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"`
	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Error("expected to find AWS secret key")
	}

	found := false

	for _, f := range findings {
		if f.Type == "aws_secret_key" {
			found = true

			if f.Severity != "critical" {
				t.Errorf("expected severity=critical, got %s", f.Severity)
			}

			break
		}
	}

	if !found {
		t.Error("expected to find aws_secret_key type")
	}
}

func TestScanText_GitHubToken(t *testing.T) {
	scanner := newTestScanner(t)

	// GitHub token pattern: ghp_[0-9a-zA-Z]{36}
	text := "token: ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef1234"
	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Error("expected to find GitHub token")
	}

	found := false

	for _, f := range findings {
		if f.Type == "github_token" {
			found = true

			if f.Severity != "high" {
				t.Errorf("expected severity=high, got %s", f.Severity)
			}

			break
		}
	}

	if !found {
		t.Error("expected to find github_token type")
	}
}

func TestScanText_SlackToken(t *testing.T) {
	scanner := newTestScanner(t)

	text := "slack_token=xoxb-123456789-123456789-abcdefghijklmnop"
	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Error("expected to find Slack token")
	}

	found := false

	for _, f := range findings {
		if f.Type == "slack_token" {
			found = true

			if f.Severity != "high" {
				t.Errorf("expected severity=high, got %s", f.Severity)
			}

			break
		}
	}

	if !found {
		t.Error("expected to find slack_token type")
	}
}

func TestScanText_PrivateKey(t *testing.T) {
	scanner := newTestScanner(t)

	text := `-----BEGIN RSA PRIVATE KEY-----`
	text += `
MIIEpAIBAAKCAQEA1234567890...
-----END RSA PRIVATE KEY-----`

	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Error("expected to find private key")
	}

	found := false

	for _, f := range findings {
		if f.Type == "private_key" {
			found = true

			if f.Severity != "critical" {
				t.Errorf("expected severity=critical, got %s", f.Severity)
			}

			break
		}
	}

	if !found {
		t.Error("expected to find private_key type")
	}
}

func TestScanText_GenericPassword(t *testing.T) {
	scanner := newTestScanner(t)

	text := `password = "mysecretpass123"`
	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Error("expected to find generic password")
	}

	found := false

	for _, f := range findings {
		if f.Type == "generic_password" {
			found = true

			if f.Severity != "medium" {
				t.Errorf("expected severity=medium, got %s", f.Severity)
			}

			break
		}
	}

	if !found {
		t.Error("expected to find generic_password type")
	}
}

func TestScanText_JWTToken(t *testing.T) {
	scanner := newTestScanner(t)

	text := "jwt=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"
	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Error("expected to find JWT token")
	}

	found := false

	for _, f := range findings {
		if f.Type == "jwt_token" {
			found = true

			if f.Severity != "medium" {
				t.Errorf("expected severity=medium, got %s", f.Severity)
			}

			break
		}
	}

	if !found {
		t.Error("expected to find jwt_token type")
	}
}

func TestScanText_NoSecrets(t *testing.T) {
	scanner := newTestScanner(t)

	text := "This is just a clean text with no secrets. It contains normal information."
	findings := scanner.ScanText(text)

	if len(findings) > 0 {
		t.Errorf("expected no findings, got %d", len(findings))
	}
}

func TestScanText_MultipleSecrets(t *testing.T) {
	scanner := newTestScanner(t)

	text := ` //nolint:gosec // test fixture for secret scanner
AWS_KEY=AKIAIOSFODNN7EXAMPLE
password = "mysecretpass123"
github_token=ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef12
`

	findings := scanner.ScanText(text)

	if len(findings) < 2 {
		t.Errorf("expected at least 2 findings, got %d", len(findings))
	}
}

func TestScanText_RedactedValue(t *testing.T) {
	scanner := newTestScanner(t)

	text := "AKIAIOSFODNN7EXAMPLE"
	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Fatal("expected to find secret")
	}

	redacted := findings[0].RedactedValue
	if len(redacted) < 4 {
		t.Errorf("expected redacted value to have at least 4 chars, got %d", len(redacted))
	}

	if redacted[len(redacted)-4:] != "****" {
		t.Errorf("expected redacted value to end with ****, got %s", redacted)
	}
}

func TestScanText_RedactedValue_ShortSecret(t *testing.T) {
	scanner := newTestScanner(t)

	// Create a short secret that matches a pattern (password needs at least 8 chars)
	text := "password = \"abcdefgh\""
	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Fatal("expected to find secret")
	}

	redacted := findings[0].RedactedValue
	if len(redacted) < 4 {
		t.Errorf("expected redacted value to have at least 4 chars, got %d", len(redacted))
	}

	if redacted[len(redacted)-4:] != "****" {
		t.Errorf("expected redacted value to end with ****, got %s", redacted)
	}
}

func TestScanMetadata_WithSecrets(t *testing.T) {
	scanner := newTestScanner(t)

	metadata := map[string]any{
		"api_key": "sk_live_1234567890abcdefghij",
		"name":    "my-artifact",
	}

	findings := scanner.ScanMetadata(metadata)

	if len(findings) == 0 {
		t.Error("expected to find secrets in metadata")
	}
}

func TestScanMetadata_Clean(t *testing.T) {
	scanner := newTestScanner(t)

	metadata := map[string]any{
		"name":    "my-artifact",
		"version": "1.0.0",
		"author":  "john@example.com",
	}

	findings := scanner.ScanMetadata(metadata)

	if len(findings) > 0 {
		t.Errorf("expected no findings in clean metadata, got %d", len(findings))
	}
}

func TestScanText_StripeKey(t *testing.T) {
	scanner := newTestScanner(t)

	text := "stripe_key=sk_live_1234567890abcdefghij1234"
	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Error("expected to find Stripe key")
	}

	found := false

	for _, f := range findings {
		if f.Type == "stripe_key" {
			found = true

			if f.Severity != "critical" {
				t.Errorf("expected severity=critical, got %s", f.Severity)
			}

			break
		}
	}

	if !found {
		t.Error("expected to find stripe_key type")
	}
}

func TestScanText_GoogleAPIKey(t *testing.T) {
	scanner := newTestScanner(t)

	// Google API key pattern: AIza[0-9A-Za-z\-_]{35} (4 + 35 = 39 chars total)
	text := "google_api_key=AIzaSyDummyKeyWith35CharactersLongXYZAB"
	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Error("expected to find Google API key")
	}

	found := false

	for _, f := range findings {
		if f.Type == "google_api_key" {
			found = true

			if f.Severity != "high" {
				t.Errorf("expected severity=high, got %s", f.Severity)
			}

			break
		}
	}

	if !found {
		t.Error("expected to find google_api_key type")
	}
}

func TestScanText_NPMToken(t *testing.T) {
	scanner := newTestScanner(t)

	// npm token pattern: npm_[a-zA-Z0-9]{36}
	text := "npm_token=npm_abcdefghijklmnopqrstuvwxyz123456789012"
	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Error("expected to find npm token")
	}

	found := false

	for _, f := range findings {
		if f.Type == "npm_token" {
			found = true

			if f.Severity != "high" {
				t.Errorf("expected severity=high, got %s", f.Severity)
			}

			break
		}
	}

	if !found {
		t.Error("expected to find npm_token type")
	}
}

func TestScanText_DockerHubToken(t *testing.T) {
	scanner := newTestScanner(t)

	// docker_hub_token pattern: dckr_pat_[a-zA-Z0-9_-]{27}
	text := "docker_token=dckr_pat_abcdefghijklmnopqrstuvwxyz123"
	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Error("expected to find Docker Hub token")
	}

	found := false

	for _, f := range findings {
		if f.Type == "docker_hub_token" {
			found = true

			if f.Severity != "high" {
				t.Errorf("expected severity=high, got %s", f.Severity)
			}

			break
		}
	}

	if !found {
		t.Error("expected to find docker_hub_token type")
	}
}

func TestScanText_GenericAPIKey(t *testing.T) {
	scanner := newTestScanner(t)

	text := "api_key = 'abcdefghijklmnopqrst1234567890'"
	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Error("expected to find generic API key")
	}

	found := false

	for _, f := range findings {
		if f.Type == "generic_api_key" {
			found = true

			if f.Severity != "medium" {
				t.Errorf("expected severity=medium, got %s", f.Severity)
			}

			break
		}
	}

	if !found {
		t.Error("expected to find generic_api_key type")
	}
}

func TestScanText_LineHint(t *testing.T) {
	scanner := newTestScanner(t)

	text := "some config password = 'mysecretpass123' more text"
	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Fatal("expected to find secret")
	}

	hint := findings[0].LineHint
	if hint == "" {
		t.Error("expected non-empty line hint")
	}

	if len(hint) > 103 { // 100 chars + "..."
		t.Errorf("expected line hint to be at most 103 chars, got %d", len(hint))
	}
}

func TestScanText_GitHubOAuthToken(t *testing.T) {
	scanner := newTestScanner(t)

	// github_oauth pattern: gho_[0-9a-zA-Z]{36}
	text := "oauth_token=gho_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef1234"
	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Error("expected to find GitHub OAuth token")
	}

	found := false

	for _, f := range findings {
		if f.Type == "github_oauth" {
			found = true

			if f.Severity != "high" {
				t.Errorf("expected severity=high, got %s", f.Severity)
			}

			break
		}
	}

	if !found {
		t.Error("expected to find github_oauth type")
	}
}

func TestScanText_GitHubAppToken(t *testing.T) {
	scanner := newTestScanner(t)

	// github_app_token pattern: (ghu|ghs)_[0-9a-zA-Z]{36}
	text := "app_token=ghu_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef1234"
	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Error("expected to find GitHub App token")
	}

	found := false

	for _, f := range findings {
		if f.Type == "github_app_token" {
			found = true

			if f.Severity != "high" {
				t.Errorf("expected severity=high, got %s", f.Severity)
			}

			break
		}
	}

	if !found {
		t.Error("expected to find github_app_token type")
	}
}

func TestScanText_ECPrivateKey(t *testing.T) {
	scanner := newTestScanner(t)

	text := `-----BEGIN EC PRIVATE KEY-----`
	text += `
MHcCAQEEIIGlVmKjVmKjVmKjVmKjVmKjVmKjVmKjVmKjVmKjoAoGCCqGSM49
-----END EC PRIVATE KEY-----`

	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Error("expected to find EC private key")
	}

	found := false

	for _, f := range findings {
		if f.Type == "private_key" {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected to find private_key type")
	}
}

func TestScanText_OpenSSHPrivateKey(t *testing.T) {
	scanner := newTestScanner(t)

	text := `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUtbm9uZS1ub25lAAAAAAAAAEcAAAAH
-----END OPENSSH PRIVATE KEY-----`

	findings := scanner.ScanText(text)

	if len(findings) == 0 {
		t.Error("expected to find OpenSSH private key")
	}

	found := false

	for _, f := range findings {
		if f.Type == "private_key" {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected to find private_key type")
	}
}

func TestScanText_LineHintMasksNeighbouringSecretsAndStaysUTF8(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")
	s := NewScanner(log)

	akia := "AKIAIOSFODNN7EXAMPLE"
	npm := "npm_" + strings.Repeat("a", 36)
	text := strings.Repeat("é", 30) + akia + " " + npm + strings.Repeat("é", 60)

	findings := s.ScanText(text)
	if len(findings) < 2 {
		t.Fatalf("findings = %d, want >=2", len(findings))
	}

	for _, f := range findings {
		if strings.Contains(f.LineHint, akia) || strings.Contains(f.LineHint, npm) {
			t.Errorf("hint leaks a secret: %q", f.LineHint)
		}

		if !utf8.ValidString(f.LineHint) || !utf8.ValidString(f.RedactedValue) {
			t.Errorf("invalid UTF-8 in finding: %q / %q", f.LineHint, f.RedactedValue)
		}
	}
}
