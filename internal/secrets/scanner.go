// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package secrets

import (
	"cmp"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// SecretPattern represents a pattern for detecting secrets.
type SecretPattern struct {
	Name        string
	Description string
	Severity    string
	Regex       *regexp.Regexp
}

// Scanner scans text for secrets.
type Scanner struct {
	log      *logger.Logger
	patterns []SecretPattern
}

// NewScanner creates a new secret scanner.
func NewScanner(log *logger.Logger) *Scanner {
	return &Scanner{
		log:      log,
		patterns: defaultPatterns,
	}
}

//nolint:gochecknoglobals // immutable default secret detection patterns
var defaultPatterns = []SecretPattern{
	{
		Name:        "aws_access_key",
		Description: "AWS Access Key ID",
		Severity:    "critical",
		Regex:       regexp.MustCompile(`(?i)AKIA[0-9A-Z]{16}`),
	},
	{
		Name:        "aws_secret_key",
		Description: "AWS Secret Access Key",
		Severity:    "critical",
		Regex:       regexp.MustCompile(`(?i)aws.{0,20}secret.{0,20}['\"][0-9a-zA-Z/+]{40}['\"]`),
	},
	{
		Name:        "github_token",
		Description: "GitHub Personal Access Token",
		Severity:    "high",
		Regex:       regexp.MustCompile(`ghp_[0-9a-zA-Z]{36}`),
	},
	{
		Name:        "github_oauth",
		Description: "GitHub OAuth Token",
		Severity:    "high",
		Regex:       regexp.MustCompile(`gho_[0-9a-zA-Z]{36}`),
	},
	{
		Name:        "github_app_token",
		Description: "GitHub App Token",
		Severity:    "high",
		Regex:       regexp.MustCompile(`(ghu|ghs)_[0-9a-zA-Z]{36}`),
	},
	{
		Name:        "slack_token",
		Description: "Slack API Token",
		Severity:    "high",
		Regex:       regexp.MustCompile(`xox[baprs]-([0-9a-zA-Z]{10,48})?`),
	},
	{
		Name:        "stripe_key",
		Description: "Stripe API Key",
		Severity:    "critical",
		Regex:       regexp.MustCompile(`(?:r|s)k_live_[0-9a-zA-Z]{24}`),
	},
	{
		Name:        "private_key",
		Description: "Private Key",
		Severity:    "critical",
		Regex:       regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----`),
	},
	{
		Name:        "generic_api_key",
		Description: "Generic API Key",
		Severity:    "medium",
		Regex:       regexp.MustCompile(`(?i)(?:api[_-]?key|apikey|api[_-]?secret)['\"\s]*[:=]['\"\s]*([a-zA-Z0-9_\-]{20,})`),
	},
	{
		Name:        "generic_password",
		Description: "Generic Password",
		Severity:    "medium",
		Regex:       regexp.MustCompile(`(?i)(?:password|passwd|pwd)['\"\s]*[:=]['\"\s]*([^\s'\"]{8,})`),
	},
	{
		Name:        "jwt_token",
		Description: "JWT Token",
		Severity:    "medium",
		Regex:       regexp.MustCompile(`eyJ[a-zA-Z0-9_-]{10,}\.[a-zA-Z0-9_-]{10,}\.[a-zA-Z0-9_-]{10,}`),
	},
	{
		Name:        "google_api_key",
		Description: "Google API Key",
		Severity:    "high",
		Regex:       regexp.MustCompile(`AIza[0-9A-Za-z\-_]{35}`),
	},
	{
		Name:        "npm_token",
		Description: "npm Access Token",
		Severity:    "high",
		Regex:       regexp.MustCompile(`npm_[a-zA-Z0-9]{36}`),
	},
	{
		Name:        "docker_hub_token",
		Description: "Docker Hub Token",
		Severity:    "high",
		Regex:       regexp.MustCompile(`dckr_pat_[a-zA-Z0-9_-]{27}`),
	},
}

// span is a half-open byte range [start,end) of a detected secret.
type span struct{ start, end int }

// ScanText scans a text string for secrets.
func (s *Scanner) ScanText(text string) []models.SecretFinding {
	type hit struct {
		pattern SecretPattern
		sp      span
	}

	var hits []hit

	// Collect every match first so each line hint can mask all neighbouring
	// secrets, not only its own.
	spans := make([]span, 0, 4)

	for _, pattern := range s.patterns {
		for _, match := range pattern.Regex.FindAllStringIndex(text, -1) {
			sp := span{match[0], match[1]}
			hits = append(hits, hit{pattern, sp})
			spans = append(spans, sp)
		}
	}

	var findings []models.SecretFinding

	for _, h := range hits {
		findings = append(findings, models.SecretFinding{
			Type:          h.pattern.Name,
			Description:   h.pattern.Description,
			Severity:      h.pattern.Severity,
			RedactedValue: redactValue(text[h.sp.start:h.sp.end]),
			LineHint:      getLineHint(text, h.sp.start, h.sp.end, spans),
		})
	}

	return findings
}

// ScanMetadata scans artifact metadata map for secrets.
func (s *Scanner) ScanMetadata(metadata map[string]any) []models.SecretFinding {
	// Convert metadata to JSON string and scan
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		s.log.Warnw("failed to marshal metadata for secret scanning", "error", err)
		return nil
	}

	return s.ScanText(string(metadataJSON))
}

// redactValue masks a secret, keeping at most an eighth of it (max 4 bytes,
// cut on a rune boundary) so a finding never reveals a useful prefix.
func redactValue(value string) string {
	n := min(len(value)/8, 4)
	for n > 0 && !utf8.RuneStart(value[n]) {
		n--
	}

	return value[:n] + "****"
}

// getLineHint returns up to 100 chars around the match with every detected
// secret (spans, which includes the match itself) replaced by "****".
func getLineHint(text string, start, end int, spans []span) string {
	const contextSize = 50

	// Find line boundaries
	lineStart := start
	for lineStart > 0 && text[lineStart-1] != '\n' {
		lineStart--
	}

	lineEnd := end
	for lineEnd < len(text) && text[lineEnd] != '\n' {
		lineEnd++
	}

	contextStart := max(start-contextSize, lineStart)
	contextEnd := min(end+contextSize, lineEnd)

	// Window edges may land inside a multibyte rune; widen them to boundaries.
	for contextStart > lineStart && !utf8.RuneStart(text[contextStart]) {
		contextStart--
	}

	for contextEnd < lineEnd && !utf8.RuneStart(text[contextEnd]) {
		contextEnd++
	}

	var b strings.Builder

	pos := contextStart

	for _, sp := range sortedSpans(spans) {
		if sp.end <= pos || sp.start >= contextEnd {
			continue
		}

		if sp.start > pos {
			b.WriteString(text[pos:sp.start])
		}

		b.WriteString("****")

		pos = max(pos, sp.end)
	}

	if pos < contextEnd {
		b.WriteString(text[pos:contextEnd])
	}

	hint := strings.TrimSpace(b.String())

	// Limit to 100 bytes without splitting a rune.
	if len(hint) > 100 {
		cut := 100
		for cut > 0 && !utf8.RuneStart(hint[cut]) {
			cut--
		}

		hint = hint[:cut] + "..."
	}

	return strings.ToValidUTF8(hint, "")
}

func sortedSpans(spans []span) []span {
	out := slices.Clone(spans)
	slices.SortFunc(out, func(a, b span) int { return cmp.Compare(a.start, b.start) })

	return out
}
