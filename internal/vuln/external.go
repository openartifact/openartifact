// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
)

// The formats of an externally produced scan result.
const (
	// ExternalSARIF is SARIF 2.1.0 (trivy, grype, clair-action and Konflux's
	// scanners can write it).
	ExternalSARIF = "sarif"
	// ExternalOSVScanner is the JSON output of osv-scanner.
	ExternalOSVScanner = "osv-scanner"
	// ExternalOA is OpenArtifact's own minimal schema: {"vulnerabilities":[...]}
	// with the findings as the API returns them.
	ExternalOA = "oa"
)

const (
	// MaxExternalFindings bounds the findings of one imported scan.
	MaxExternalFindings = 50_000

	maxExternalText = 2048
	maxSARIFRuns    = 16
)

// ErrExternalInvalid is returned for a scan result that cannot be read in the
// format it was declared in.
var ErrExternalInvalid = errors.New("scan result is not valid")

// ExternalFormats lists the accepted formats.
func ExternalFormats() []string { return []string{ExternalSARIF, ExternalOSVScanner, ExternalOA} }

// ParseExternal reads the findings of a scan result produced by another tool.
// Every text is bounded and severities are normalized to CRITICAL, HIGH,
// MEDIUM, LOW or UNKNOWN, so what is stored never depends on the producer.
func ParseExternal(format string, data []byte) ([]models.Vulnerability, error) {
	var (
		vulns []models.Vulnerability
		err   error
	)

	switch format {
	case ExternalSARIF:
		vulns, err = parseSARIF(data)
	case ExternalOSVScanner:
		vulns, err = parseOSVScanner(data)
	case ExternalOA:
		vulns, err = parseOA(data)
	default:
		return nil, fmt.Errorf("%w: unknown format %q", ErrExternalInvalid, format)
	}

	if err != nil {
		return nil, err
	}

	if len(vulns) > MaxExternalFindings {
		return nil, fmt.Errorf("%w: more than %d findings", ErrExternalInvalid, MaxExternalFindings)
	}

	out := make([]models.Vulnerability, 0, len(vulns))

	for _, v := range vulns {
		if v = normalizeFinding(v); v.ID != "" {
			out = append(out, v)
		}
	}

	return out, nil
}

// normalizeFinding bounds the texts of a finding and fixes its severity.
func normalizeFinding(v models.Vulnerability) models.Vulnerability {
	v.ID = clip(strings.TrimSpace(v.ID), 256)
	v.Summary = clip(v.Summary, maxExternalText)
	v.FixedIn = clip(v.FixedIn, 256)
	v.Package = clip(v.Package, 512)
	v.PublishedAt = clip(v.PublishedAt, 64)

	v.Severity = strings.ToUpper(strings.TrimSpace(v.Severity))
	switch v.Severity {
	case "CRITICAL", "HIGH", "MEDIUM", "LOW":
	case "MODERATE":
		v.Severity = "MEDIUM"
	default:
		v.Severity = "UNKNOWN"
	}

	if v.CVSS < 0 || v.CVSS > 10 || v.CVSS != v.CVSS {
		v.CVSS = 0
	}

	v.References = clipList(v.References, 32, 1024)
	v.Aliases = clipList(cleanAliases(v.ID, v.Aliases), maxAliases, 256)
	v.CWEs = clipList(v.CWEs, 32, 64)

	return v
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return strings.ToValidUTF8(s[:n], "")
}

func clipList(in []string, maxLen, maxItem int) []string {
	if len(in) > maxLen {
		in = in[:maxLen]
	}

	out := make([]string, len(in))
	for i, s := range in {
		out[i] = clip(s, maxItem)
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

func parseOA(data []byte) ([]models.Vulnerability, error) {
	var doc struct {
		Vulnerabilities []models.Vulnerability `json:"vulnerabilities"`
	}

	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExternalInvalid, err)
	}

	return doc.Vulnerabilities, nil
}

func parseOSVScanner(data []byte) ([]models.Vulnerability, error) {
	var doc struct {
		Results []struct {
			Packages []struct {
				Package struct {
					Name      string `json:"name"`
					Version   string `json:"version"`
					Ecosystem string `json:"ecosystem"`
				} `json:"package"`
				Vulnerabilities []osvVulnerability `json:"vulnerabilities"`
			} `json:"packages"`
		} `json:"results"`
	}

	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExternalInvalid, err)
	}

	var out []models.Vulnerability

	for _, res := range doc.Results {
		for _, p := range res.Packages {
			for _, v := range p.Vulnerabilities {
				f := convertOSV(v, p.Package.Ecosystem, p.Package.Name, p.Package.Version)
				f.Package = p.Package.Name + "@" + p.Package.Version
				out = append(out, f)

				if len(out) > MaxExternalFindings {
					return out, nil
				}
			}
		}
	}

	return out, nil
}

// sarifFixed finds the fixed version trivy and grype write into a rule's help
// text or a result's message.
var sarifFixed = regexp.MustCompile(`(?i)fixed\s+version:?\s*([^\s|,;]+)`)

// The sarif types use lower-case tags: Go matches JSON keys case-insensitively,
// so they read the camelCase keys of SARIF 2.1.
type sarifRule struct {
	ID               string `json:"id"`
	ShortDescription struct {
		Text string `json:"text"`
	} `json:"shortdescription"`
	FullDescription struct {
		Text string `json:"text"`
	} `json:"fulldescription"`
	Help struct {
		Text string `json:"text"`
	} `json:"help"`
	HelpURI    string         `json:"helpuri"`
	Properties map[string]any `json:"properties"`
}

type sarifResult struct {
	RuleID    string `json:"ruleid"`
	RuleIndex int    `json:"ruleindex"`
	Level     string `json:"level"`
	Message   struct {
		Text string `json:"text"`
	} `json:"message"`
	Locations []struct {
		PhysicalLocation struct {
			ArtifactLocation struct {
				URI string `json:"uri"`
			} `json:"artifactlocation"`
		} `json:"physicallocation"`
		LogicalLocations []struct {
			Name string `json:"name"`
		} `json:"logicallocations"`
	} `json:"locations"`
	Properties map[string]any `json:"properties"`
}

type sarifRun struct {
	Tool struct {
		Driver struct {
			Rules []sarifRule `json:"rules"`
		} `json:"driver"`
	} `json:"tool"`
	Results []sarifResult `json:"results"`
}

func parseSARIF(data []byte) ([]models.Vulnerability, error) {
	var doc struct {
		Version string     `json:"version"`
		Runs    []sarifRun `json:"runs"`
	}

	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExternalInvalid, err)
	}

	if !strings.HasPrefix(doc.Version, "2.") {
		return nil, fmt.Errorf("%w: SARIF version %q is not 2.x", ErrExternalInvalid, doc.Version)
	}

	if len(doc.Runs) > maxSARIFRuns {
		return nil, fmt.Errorf("%w: more than %d runs", ErrExternalInvalid, maxSARIFRuns)
	}

	var out []models.Vulnerability

	for i := range doc.Runs {
		rules := doc.Runs[i].Tool.Driver.Rules

		for j := range doc.Runs[i].Results {
			res := &doc.Runs[i].Results[j]
			out = append(out, sarifFinding(res, sarifRuleOf(rules, res)))

			if len(out) > MaxExternalFindings {
				return out, nil
			}
		}
	}

	return out, nil
}

// sarifRuleOf returns the rule a result names, by id or by index; nil for none.
func sarifRuleOf(rules []sarifRule, res *sarifResult) *sarifRule {
	for i := range rules {
		if res.RuleID != "" && rules[i].ID == res.RuleID {
			return &rules[i]
		}
	}

	if res.RuleID == "" && res.RuleIndex >= 0 && res.RuleIndex < len(rules) {
		return &rules[res.RuleIndex]
	}

	return nil
}

func sarifFinding(res *sarifResult, rule *sarifRule) models.Vulnerability {
	v := models.Vulnerability{ID: res.RuleID, Summary: res.Message.Text}

	if rule != nil {
		if v.ID == "" {
			v.ID = rule.ID
		}

		if t := firstNonEmpty(rule.ShortDescription.Text, rule.FullDescription.Text); t != "" {
			v.Summary = t
		}

		if rule.HelpURI != "" {
			v.References = []string{rule.HelpURI}
		}

		v.Severity, v.CVSS = sarifSeverity(res, rule)
		v.FixedIn = fixedFrom(rule.Help.Text, res.Message.Text)
	} else {
		v.Severity, v.CVSS = sarifSeverity(res, nil)
		v.FixedIn = fixedFrom(res.Message.Text)
	}

	for _, loc := range res.Locations {
		if len(loc.LogicalLocations) > 0 && loc.LogicalLocations[0].Name != "" {
			v.Package = loc.LogicalLocations[0].Name
			break
		}

		if uri := loc.PhysicalLocation.ArtifactLocation.URI; uri != "" {
			v.Package = uri
			break
		}
	}

	return v
}

// sarifSeverity reads the severity of a result: the GitHub "security-severity"
// score (a CVSS number) of the result or its rule, then a severity word among
// the rule's tags (trivy), then the SARIF level.
func sarifSeverity(res *sarifResult, rule *sarifRule) (string, float64) {
	props := []map[string]any{res.Properties}
	if rule != nil {
		props = append(props, rule.Properties)
	}

	for _, p := range props {
		if score, ok := numberProp(p["security-severity"]); ok && score > 0 {
			return cvssScoreToSeverity(score), score
		}
	}

	if rule != nil {
		if tags, ok := rule.Properties["tags"].([]any); ok {
			for _, t := range tags {
				s, _ := t.(string)
				switch strings.ToUpper(s) {
				case "CRITICAL", "HIGH", "MEDIUM", "LOW":
					return strings.ToUpper(s), 0
				}
			}
		}
	}

	switch strings.ToLower(res.Level) {
	case "error":
		return "HIGH", 0
	case "warning":
		return "MEDIUM", 0
	case "note":
		return "LOW", 0
	}

	return "UNKNOWN", 0
}

func numberProp(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}

	return 0, false
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}

	return ""
}

// fixedFrom returns the fixed version named in the first of texts that names one.
func fixedFrom(texts ...string) string {
	for _, t := range texts {
		if m := sarifFixed.FindStringSubmatch(t); m != nil {
			return m[1]
		}
	}

	return ""
}
