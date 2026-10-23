// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package scanexport renders stored vulnerability and secret scan results as
// SARIF 2.1.0 (for GitHub code scanning, DefectDojo and other SARIF consumers)
// and as CSV. Secret exports never carry a secret value, not even a redacted
// one.
package scanexport

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	sarifSchema  = "https://json.schemastore.org/sarif-2.1.0.json"
	sarifVersion = "2.1.0"

	// MaxResults is the most results GitHub code scanning accepts in one SARIF
	// file; an export holds back the least severe findings beyond it.
	MaxResults = 25000

	// maxRuleIDLen is the longest rule id GitHub code scanning accepts.
	maxRuleIDLen = 255

	// maxMessageLen is the longest message GitHub shows; it truncates beyond it.
	maxMessageLen = 1024

	fingerprintKey = "openartifact/v1"
)

// Log is a SARIF log: the top-level object of a .sarif file.
type Log struct {
	Schema  string `json:"$schema"`
	Version string `json:"version"`
	Runs    []Run  `json:"runs"`
}

// Run is the output of one tool.
type Run struct {
	Tool              Tool               `json:"tool"`
	AutomationDetails *AutomationDetails `json:"automationDetails,omitempty"`
	Invocations       []Invocation       `json:"invocations,omitempty"`
	Results           []Result           `json:"results"`
	Properties        map[string]any     `json:"properties,omitempty"`
}

// Tool names the program that produced a run.
type Tool struct {
	Driver Driver `json:"driver"`
}

// Driver is the tool component with its rules.
type Driver struct {
	Name           string `json:"name"`
	InformationURI string `json:"informationUri,omitempty"`
	Rules          []Rule `json:"rules"`
}

// AutomationDetails distinguishes runs of the same tool in one upload.
type AutomationDetails struct {
	ID string `json:"id"`
}

// Invocation records whether the tool ran to completion.
type Invocation struct {
	ExecutionSuccessful bool `json:"executionSuccessful"`
}

// Rule describes one kind of finding (a vulnerability id or a secret type).
type Rule struct {
	ID               string         `json:"id"`
	Name             string         `json:"name,omitempty"`
	ShortDescription *Text          `json:"shortDescription,omitempty"`
	FullDescription  *Text          `json:"fullDescription,omitempty"`
	Help             *Help          `json:"help,omitempty"`
	HelpURI          string         `json:"helpUri,omitempty"`
	Properties       map[string]any `json:"properties,omitempty"`
}

// Text is a SARIF message string.
type Text struct {
	Text string `json:"text"`
}

// Help is the rule's help text.
type Help struct {
	Text     string `json:"text"`
	Markdown string `json:"markdown,omitempty"`
}

// Result is one finding.
type Result struct {
	RuleID              string            `json:"ruleId"`
	RuleIndex           int               `json:"ruleIndex"`
	Level               string            `json:"level"`
	Message             Text              `json:"message"`
	Locations           []Location        `json:"locations"`
	PartialFingerprints map[string]string `json:"partialFingerprints"`
	Properties          map[string]any    `json:"properties,omitempty"`
}

// Location points at the artifact a finding is about.
type Location struct {
	PhysicalLocation PhysicalLocation  `json:"physicalLocation"`
	LogicalLocations []LogicalLocation `json:"logicalLocations,omitempty"`
}

// PhysicalLocation is a file-like place.
type PhysicalLocation struct {
	ArtifactLocation ArtifactLocation `json:"artifactLocation"`
	Region           Region           `json:"region"`
}

// ArtifactLocation is the artifact's relative URI.
type ArtifactLocation struct {
	URI string `json:"uri"`
}

// Region is the (whole-artifact) region of a finding.
type Region struct {
	StartLine int `json:"startLine"`
}

// LogicalLocation names the package a finding is about.
type LogicalLocation struct {
	Name               string `json:"name"`
	FullyQualifiedName string `json:"fullyQualifiedName,omitempty"`
	Kind               string `json:"kind,omitempty"`
}

// severityRank orders severities, worst first (lower is worse).
func severityRank(level string) int {
	switch strings.ToLower(level) {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium", "moderate":
		return 2
	case "low":
		return 3
	}

	return 4
}

// levelAndScore maps a severity to the SARIF level and the GitHub
// "security-severity" score (0.0 to 10.0; GitHub shows >=9 critical, >=7 high,
// >=4 medium, below that low).
func levelAndScore(severity string) (string, float64) {
	switch severityRank(severity) {
	case 0:
		return "error", 9.5
	case 1:
		return "error", 8.0
	case 2:
		return "warning", 5.5
	case 3:
		return "note", 3.0
	}

	return "note", 0.1
}

// scoreString renders a security-severity score the way GitHub expects it: a
// string holding a number.
func scoreString(score float64) string {
	return strconv.FormatFloat(score, 'f', 1, 64)
}

// truncate shortens s to at most n bytes without splitting a rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}

	return s
}

// clean drops control characters so a message cannot break a consumer's line
// handling.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}

		return r
	}, s)
}

// ruleID returns id as a rule id GitHub accepts: non-empty and at most 255
// bytes; an over-long id keeps a prefix plus a hash so it stays unique.
func ruleID(id string) string {
	id = strings.TrimSpace(clean(id))
	if id == "" {
		return "OA-UNKNOWN"
	}

	if len(id) <= maxRuleIDLen {
		return id
	}

	sum := sha256.Sum256([]byte(id))

	return truncate(id, maxRuleIDLen-17) + "-" + hex.EncodeToString(sum[:8])
}

// artifactURI is the relative artifact location "<repository>/<artifact id>",
// every path segment percent-encoded.
func artifactURI(repo, artifactID string) string {
	parts := append([]string{repo}, strings.Split(artifactID, "/")...)
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}

	return strings.Join(parts, "/")
}

// fingerprint is a stable identity for a finding, so a consumer can match the
// same finding across uploads: the hash of its parts.
func fingerprint(parts ...string) string {
	h := sha256.New()

	for _, p := range parts {
		h.Write([]byte(strconv.Itoa(len(p)) + ":" + p))
	}

	return hex.EncodeToString(h.Sum(nil))
}

// finding is one result before grouping into runs.
type finding struct {
	tool    string
	rank    int
	rule    Rule
	result  Result
	success bool
}

// buildRuns sorts the findings worst first, keeps the first MaxResults, and
// groups them into one run per tool, with each run's rules deduplicated and
// results pointing at them. emptyTool names the run emitted when there are no
// findings at all (a SARIF upload with no run is rejected by some consumers).
// truncated reports whether findings were held back.
func buildRuns(findings []finding, tools map[string]toolInfo, emptyTool string) ([]Run, bool) {
	slices.SortStableFunc(findings, func(a, b finding) int { return a.rank - b.rank })

	truncated := len(findings) > MaxResults
	if truncated {
		findings = findings[:MaxResults]
	}

	order := []string{}
	byTool := map[string]*Run{}
	ruleIdx := map[string]map[string]int{}

	for _, f := range findings {
		run, ok := byTool[f.tool]
		if !ok {
			run = newRun(f.tool, tools)
			byTool[f.tool] = run
			ruleIdx[f.tool] = map[string]int{}
			order = append(order, f.tool)
		}

		idx, seen := ruleIdx[f.tool][f.rule.ID]
		if !seen {
			idx = len(run.Tool.Driver.Rules)
			ruleIdx[f.tool][f.rule.ID] = idx
			run.Tool.Driver.Rules = append(run.Tool.Driver.Rules, f.rule)
		}

		f.result.RuleIndex = idx
		run.Results = append(run.Results, f.result)

		if !f.success {
			run.Invocations[0].ExecutionSuccessful = false
		}
	}

	if len(order) == 0 {
		return []Run{*newRun(emptyTool, tools)}, false
	}

	slices.Sort(order)

	runs := make([]Run, 0, len(order))
	for _, t := range order {
		run := byTool[t]
		if truncated {
			run.Properties = map[string]any{"truncated": true, "maxResults": MaxResults}
		}

		runs = append(runs, *run)
	}

	return runs, truncated
}

// toolInfo is the descriptive data of a tool.
type toolInfo struct{ uri string }

func newRun(tool string, tools map[string]toolInfo) *Run {
	return &Run{
		Tool: Tool{Driver: Driver{
			Name:           tool,
			InformationURI: tools[tool].uri,
			Rules:          []Rule{},
		}},
		AutomationDetails: &AutomationDetails{ID: "openartifact/" + tool + "/"},
		Invocations:       []Invocation{{ExecutionSuccessful: true}},
		Results:           []Result{},
	}
}
