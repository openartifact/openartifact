// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package scanexport_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/scanexport"
)

func checkScore(t *testing.T, v any) {
	t.Helper()

	var score float64
	if _, err := fmt.Sscanf(v.(string), "%f", &score); err != nil || score < 0 || score > 10 {
		t.Fatalf("security-severity %v", v)
	}
}

// checkGitHubLimits asserts the constraints GitHub code scanning documents for
// uploads: at most 20 runs, 25000 results per run, a non-empty rule id of at
// most 255 characters, a level of error, warning or note, a physical location
// with a relative URI, and a security-severity that is a number in 0..10.
func checkGitHubLimits(t *testing.T, doc *scanexport.Log) {
	t.Helper()

	if len(doc.Runs) == 0 || len(doc.Runs) > 20 {
		t.Fatalf("runs = %d, want 1..20", len(doc.Runs))
	}

	for _, run := range doc.Runs {
		if len(run.Results) > scanexport.MaxResults {
			t.Fatalf("results = %d, over %d", len(run.Results), scanexport.MaxResults)
		}

		for _, r := range run.Tool.Driver.Rules {
			if r.ID == "" || len(r.ID) > 255 {
				t.Fatalf("rule id %q has length %d", r.ID, len(r.ID))
			}

			checkScore(t, r.Properties["security-severity"])
		}

		for _, r := range run.Results {
			switch r.Level {
			case "error", "warning", "note":
			default:
				t.Fatalf("level %q", r.Level)
			}

			if run.Tool.Driver.Rules[r.RuleIndex].ID != r.RuleID {
				t.Fatalf("ruleIndex %d does not point at %s", r.RuleIndex, r.RuleID)
			}

			uri := r.Locations[0].PhysicalLocation.ArtifactLocation.URI
			if uri == "" || strings.HasPrefix(uri, "/") || strings.Contains(uri, "://") || strings.Contains(uri, " ") {
				t.Fatalf("artifact uri %q is not a relative URI", uri)
			}

			if r.PartialFingerprints["openartifact/v1"] == "" {
				t.Fatal("missing partial fingerprint")
			}

			if len(r.Message.Text) > 1024 {
				t.Fatal("message too long")
			}
		}
	}
}

func vulnItems() []scanexport.VulnItem {
	return []scanexport.VulnItem{
		{Repo: "npm-main", Scan: &models.VulnScanResult{
			ID: "s1", ArtifactID: "lodash@4.17.20", PackageName: "lodash", PackageVersion: "4.17.20", Ecosystem: "npm",
			Scanner: "osv", ScanStatus: "completed",
			Vulnerabilities: []models.Vulnerability{
				{ID: "CVE-2021-23337", Summary: "Command injection", Severity: "HIGH", CVSS: 7.2, FixedIn: "4.17.21", References: []string{"https://x.test/a"}},
				{ID: "GHSA-35jh-r3h4-6jhm", Summary: "=HYPERLINK(\"http://evil\")", Severity: "CRITICAL"},
			},
		}},
		{Repo: "npm-main", Scan: &models.VulnScanResult{
			ID: "s2", ArtifactID: "@scope/pkg@1.0.0", PackageName: "@scope/pkg", PackageVersion: "1.0.0", Ecosystem: "npm",
			Scanner: "trivy", ScanStatus: "partial",
			Vulnerabilities: []models.Vulnerability{{ID: "CVE-2021-23337", Severity: "MEDIUM"}, {ID: "", Severity: "weird"}},
		}},
		{Repo: "debs", Scan: &models.VulnScanResult{
			ID: "s3", ArtifactID: "pool/main/c/curl", PackageName: "curl", PackageVersion: "7.0", Ecosystem: "deb",
			Scanner: "grype,osv", ScanStatus: "completed",
			Vulnerabilities: []models.Vulnerability{{ID: strings.Repeat("X", 400), Severity: "LOW"}},
		}},
		{Repo: "npm-main", Scan: &models.VulnScanResult{ID: "s4", ArtifactID: "f", Scanner: "grype", ScanStatus: "failed"}},
	}
}

func TestVulnSARIF_ValidSchemaAndGitHubLimits(t *testing.T) {
	doc, truncated := scanexport.VulnSARIF(vulnItems())
	if truncated {
		t.Fatal("truncated")
	}

	checkGitHubLimits(t, doc)

	var names []string
	for _, r := range doc.Runs {
		names = append(names, r.Tool.Driver.Name)
	}

	if strings.Join(names, ",") != "grype+osv,osv,trivy" {
		t.Fatalf("runs = %v, want one per scanner combination", names)
	}

	osv := doc.Runs[1]
	if len(osv.Tool.Driver.Rules) != 2 || len(osv.Results) != 2 {
		t.Fatalf("osv rules=%d results=%d", len(osv.Tool.Driver.Rules), len(osv.Results))
	}

	// Worst first: the critical GHSA comes before the high CVE.
	if osv.Results[0].RuleID != "GHSA-35jh-r3h4-6jhm" || osv.Results[0].Level != "error" {
		t.Fatalf("first result = %+v", osv.Results[0])
	}

	cve := osv.Tool.Driver.Rules[1]
	if cve.HelpURI != "https://osv.dev/vulnerability/CVE-2021-23337" || cve.Properties["security-severity"] != "7.2" {
		t.Fatalf("rule = %+v", cve)
	}

	if got := osv.Results[1].Locations[0].PhysicalLocation.ArtifactLocation.URI; got != "npm-main/lodash@4.17.20" {
		t.Fatalf("uri = %q", got)
	}

	if got := osv.Results[1].Locations[0].LogicalLocations[0].FullyQualifiedName; got != "pkg:npm/lodash@4.17.20" {
		t.Fatalf("purl = %q", got)
	}

	trivy := doc.Runs[2]
	if trivy.Invocations[0].ExecutionSuccessful {
		t.Fatal("a partial scan must mark its run unsuccessful")
	}

	if trivy.Results[0].Locations[0].PhysicalLocation.ArtifactLocation.URI != "npm-main/@scope/pkg@1.0.0" {
		t.Fatalf("uri = %q", trivy.Results[0].Locations[0].PhysicalLocation.ArtifactLocation.URI)
	}
}

func TestVulnSARIF_FingerprintsAreStableAndDistinct(t *testing.T) {
	a, _ := scanexport.VulnSARIF(vulnItems())
	b, _ := scanexport.VulnSARIF(vulnItems())

	seen := map[string]bool{}

	for i, run := range a.Runs {
		for j, r := range run.Results {
			fp := r.PartialFingerprints["openartifact/v1"]
			if fp != b.Runs[i].Results[j].PartialFingerprints["openartifact/v1"] {
				t.Fatal("fingerprint not stable")
			}

			if seen[fp] {
				t.Fatal("duplicate fingerprint")
			}

			seen[fp] = true
		}
	}
}

func TestVulnSARIF_EmptyIsAValidRun(t *testing.T) {
	doc, _ := scanexport.VulnSARIF(nil)
	if len(doc.Runs) != 1 || len(doc.Runs[0].Results) != 0 {
		t.Fatalf("runs = %+v", doc.Runs)
	}
}

func TestVulnSARIF_TruncatesToGitHubLimitKeepingWorst(t *testing.T) {
	vulns := make([]models.Vulnerability, 0, scanexport.MaxResults+5)
	for i := range scanexport.MaxResults {
		vulns = append(vulns, models.Vulnerability{ID: fmt.Sprintf("CVE-1-%d", i), Severity: "LOW"})
	}

	vulns = append(vulns, models.Vulnerability{ID: "CVE-CRIT", Severity: "CRITICAL"})

	doc, truncated := scanexport.VulnSARIF([]scanexport.VulnItem{{Repo: "r", Scan: &models.VulnScanResult{
		ID: "s", ArtifactID: "a", PackageName: "a", PackageVersion: "1", Ecosystem: "npm", Scanner: "osv", ScanStatus: "completed", Vulnerabilities: vulns,
	}}})
	if !truncated {
		t.Fatal("not truncated")
	}

	run := doc.Runs[0]
	if len(run.Results) != scanexport.MaxResults || run.Results[0].RuleID != "CVE-CRIT" {
		t.Fatalf("results=%d first=%s", len(run.Results), run.Results[0].RuleID)
	}

	if run.Properties["truncated"] != true {
		t.Fatal("run is not marked truncated")
	}

	checkGitHubLimits(t, doc)
}

func TestVulnCSV_FormulaGuard(t *testing.T) {
	var buf bytes.Buffer
	if err := scanexport.WriteCSV(&buf, scanexport.VulnCSVHeader, scanexport.VulnCSVRows(vulnItems())); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if !strings.Contains(out, `'=HYPERLINK`) || strings.Contains(out, ",=HYPERLINK") || strings.Contains(out, `,"=HYPERLINK`) {
		t.Fatalf("formula cell is not neutralized:\n%s", out)
	}

	for _, c := range []string{"=1", "+1", "-1", "@x", "\tx", "\rx"} {
		if got := scanexport.SafeCell(c); got != "'"+c {
			t.Errorf("SafeCell(%q) = %q", c, got)
		}
	}

	if scanexport.SafeCell("lodash") != "lodash" || scanexport.SafeCell("") != "" {
		t.Error("plain cells must not change")
	}
}

func secretItems() []scanexport.SecretItem {
	return []scanexport.SecretItem{{Repo: "raw", Scan: &models.SecretScanResult{
		ID: "x1", ArtifactID: "dir/.env", ScanStatus: "flagged", FindingCount: 3,
		Findings: []models.SecretFinding{
			{Type: "aws_access_key", Description: "AWS Access Key ID", Severity: "critical", RedactedValue: "AKIA****LEAK", LineHint: "key=AKIAIOSFODNN7EXAMPLE"},
			{Type: "aws_access_key", Description: "AWS Access Key ID", Severity: "critical", RedactedValue: "AKIA****LEAK", LineHint: "again"},
			{Type: "jwt", Description: "JWT Token", Severity: "medium", RedactedValue: "eyJ****SIG"},
		},
	}}}
}

func TestSecretsExports_NeverContainValues(t *testing.T) {
	doc, _ := scanexport.SecretsSARIF(secretItems())
	checkGitHubLimits(t, doc)

	var csvBuf bytes.Buffer
	if err := scanexport.WriteCSV(&csvBuf, scanexport.SecretsCSVHeader, scanexport.SecretsCSVRows(secretItems())); err != nil {
		t.Fatal(err)
	}

	sarif, _ := json.Marshal(doc)

	for _, out := range []string{string(sarif), csvBuf.String()} {
		for _, leak := range []string{"AKIA", "LEAK", "eyJ", "SIG", "again", "EXAMPLE"} {
			if strings.Contains(out, leak) {
				t.Errorf("export contains %q: %s", leak, out)
			}
		}
	}

	run := doc.Runs[0]
	if len(run.Tool.Driver.Rules) != 2 || len(run.Results) != 3 {
		t.Fatalf("rules=%d results=%d", len(run.Tool.Driver.Rules), len(run.Results))
	}

	if run.Results[0].PartialFingerprints["openartifact/v1"] == run.Results[1].PartialFingerprints["openartifact/v1"] {
		t.Fatal("two findings of one type share a fingerprint")
	}

	if run.Results[0].Locations[0].PhysicalLocation.ArtifactLocation.URI != "raw/dir/.env" {
		t.Fatalf("uri = %q", run.Results[0].Locations[0].PhysicalLocation.ArtifactLocation.URI)
	}
}
