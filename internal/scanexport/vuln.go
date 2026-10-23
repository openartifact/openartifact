// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package scanexport

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/vuln"
)

// VulnItem is a vulnerability scan with the name of its repository.
type VulnItem struct {
	Repo string
	Scan *models.VulnScanResult
}

//nolint:gochecknoglobals // immutable tool metadata
var vulnTools = map[string]toolInfo{
	"osv":   {uri: "https://osv.dev"},
	"trivy": {uri: "https://trivy.dev"},
	"grype": {uri: "https://github.com/anchore/grype"},
}

const unknownTool = "openartifact"

// vulnToolName is the run a scan belongs to: its scanner (osv, trivy or grype).
// A scan that ran several scanners stores one merged finding list that does not
// say which scanner reported what, so it gets its own run named after all of
// them ("osv+trivy"); findings are never duplicated across runs.
func vulnToolName(scanner string) string {
	scanner = strings.ReplaceAll(strings.TrimSpace(scanner), ",", "+")
	if scanner == "" {
		return unknownTool
	}

	return scanner
}

func vulnHelpURI(id string) string {
	return "https://osv.dev/vulnerability/" + url.PathEscape(id)
}

// securityScore picks the GitHub security-severity: the CVSS score when the
// scanner gave one, else a fixed score for the severity.
func securityScore(v models.Vulnerability) float64 {
	if v.CVSS > 0 && v.CVSS <= 10 {
		return v.CVSS
	}

	_, score := levelAndScore(v.Severity)

	return score
}

func vulnRule(v models.Vulnerability, id string) Rule {
	summary := truncate(clean(v.Summary), maxMessageLen)
	if summary == "" {
		summary = id
	}

	help := "See " + vulnHelpURI(v.ID) + " for details."
	if v.FixedIn != "" {
		help = "Fixed in " + clean(v.FixedIn) + ". " + help
	}

	props := map[string]any{
		"security-severity": scoreString(securityScore(v)),
		"tags":              []string{"security", "vulnerability", strings.ToLower(v.Severity)},
	}

	return Rule{
		ID:               id,
		Name:             id,
		ShortDescription: &Text{Text: truncate(summary, 255)},
		FullDescription:  &Text{Text: summary},
		Help:             &Help{Text: help, Markdown: help},
		HelpURI:          vulnHelpURI(v.ID),
		Properties:       props,
	}
}

func vulnMessage(s *models.VulnScanResult, v models.Vulnerability) string {
	msg := fmt.Sprintf("%s@%s is affected by %s", clean(s.PackageName), clean(s.PackageVersion), clean(v.ID))
	if v.Summary != "" {
		msg += ": " + clean(v.Summary)
	}

	if v.FixedIn != "" {
		msg += " Fixed in " + clean(v.FixedIn) + "."
	}

	return truncate(msg, maxMessageLen)
}

// VulnSARIF builds a SARIF log of the scans' findings: one run per scanner,
// one rule per vulnerability id, one result per package and vulnerability,
// located by the artifact path "<repository>/<artifact id>" with the package
// URL as a logical location. Failed and pending scans have no findings and
// contribute none; a partial scan marks its run unsuccessful. truncated is
// true when more than MaxResults findings existed and the least severe were
// dropped.
func VulnSARIF(items []VulnItem) (*Log, bool) {
	var findings []finding

	for _, it := range items {
		s := it.Scan
		tool := vulnToolName(s.Scanner)
		purl := vuln.PackageURL(s.Ecosystem, s.PackageName, s.PackageVersion)
		uri := artifactURI(it.Repo, s.ArtifactID)

		for _, v := range s.Vulnerabilities {
			id := ruleID(v.ID)
			level, _ := levelAndScore(v.Severity)

			findings = append(findings, finding{
				tool:    tool,
				rank:    severityRank(v.Severity),
				rule:    vulnRule(v, id),
				success: s.ScanStatus != "partial",
				result: Result{
					RuleID:  id,
					Level:   level,
					Message: Text{Text: vulnMessage(s, v)},
					Locations: []Location{{
						PhysicalLocation: PhysicalLocation{
							ArtifactLocation: ArtifactLocation{URI: uri},
							Region:           Region{StartLine: 1},
						},
						LogicalLocations: []LogicalLocation{{
							Name:               s.PackageName + "@" + s.PackageVersion,
							FullyQualifiedName: purl,
							Kind:               "package",
						}},
					}},
					PartialFingerprints: map[string]string{
						fingerprintKey: fingerprint(it.Repo, s.ArtifactID, s.PackageName, s.PackageVersion, s.Ecosystem, v.ID),
					},
					Properties: map[string]any{
						"repository": it.Repo,
						"purl":       purl,
						"severity":   strings.ToLower(v.Severity),
						"cvss":       v.CVSS,
						"fixedIn":    v.FixedIn,
						"scanId":     s.ID,
					},
				},
			})
		}
	}

	runs, truncated := buildRuns(findings, vulnTools, unknownTool)

	return &Log{Schema: sarifSchema, Version: sarifVersion, Runs: runs}, truncated
}

// VulnCSVHeader is the first row of VulnCSV.
//
//nolint:gochecknoglobals // immutable column list
var VulnCSVHeader = []string{
	"repository", "artifact_id", "package_name", "package_version", "ecosystem", "scanner",
	"scan_id", "scan_status", "vulnerability_id", "severity", "cvss", "fixed_in", "summary",
	"published_at", "references",
}

// VulnCSVRows returns one row per finding (header excluded), most severe scans
// first as given. A scan without findings yields no row.
func VulnCSVRows(items []VulnItem) [][]string {
	var rows [][]string

	for _, it := range items {
		s := it.Scan
		for _, v := range s.Vulnerabilities {
			cvss := ""
			if v.CVSS > 0 {
				cvss = strconv.FormatFloat(v.CVSS, 'f', -1, 64)
			}

			rows = append(rows, []string{
				it.Repo, s.ArtifactID, s.PackageName, s.PackageVersion, s.Ecosystem, s.Scanner,
				s.ID, s.ScanStatus, v.ID, strings.ToLower(v.Severity), cvss, v.FixedIn, v.Summary,
				v.PublishedAt, strings.Join(v.References, " "),
			})
		}
	}

	return rows
}
