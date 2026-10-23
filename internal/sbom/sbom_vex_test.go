// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

const testSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeDB struct {
	database.BaseMockDB

	lic   *models.LicenseScanResult
	vuln  *models.VulnScanResult
	vulns []*models.VulnScanResult
	stmts []*models.VEXStatement
	comps []*models.Component
}

func (f *fakeDB) GetLicenseScanResult(context.Context, string, string) (*models.LicenseScanResult, error) {
	return f.lic, nil
}

func (f *fakeDB) GetVulnScanResult(context.Context, string, string) (*models.VulnScanResult, error) {
	return f.vuln, nil
}

func (f *fakeDB) ListVulnScanResults(context.Context, string, bool) ([]*models.VulnScanResult, error) {
	return f.vulns, nil
}

func (f *fakeDB) ListVEXStatements(_ context.Context, _, artifactID string) ([]*models.VEXStatement, error) {
	var out []*models.VEXStatement

	for _, s := range f.stmts {
		if artifactID == "" || s.ArtifactID == artifactID {
			out = append(out, s)
		}
	}

	return out, nil
}

func (f *fakeDB) SearchComponents(context.Context, string, database.ComponentListOptions) ([]*models.Component, int, error) {
	return f.comps, len(f.comps), nil
}

func scanned(vulns ...models.Vulnerability) *models.VulnScanResult {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	return &models.VulnScanResult{
		ArtifactID: "lib@1.0.0", ScanStatus: "completed", Scanner: "osv",
		Vulnerabilities: vulns, ScannedAt: &now,
	}
}

//nolint:gochecknoglobals // read-only fixture copied by value
var testVuln = models.Vulnerability{
	ID: "CVE-2024-0001", Summary: "bad", Severity: "HIGH", CVSS: 7.5, FixedIn: "1.0.5",
	References: []string{"https://example.com/a", "javascript:alert(1)"}, CWEs: []string{"CWE-79"},
	PublishedAt: "2024-01-01T00:00:00Z",
}

func newFakeGen(db *fakeDB) *Generator {
	return NewGenerator(db, nil).WithToolVersion("9.9.9")
}

//nolint:gocyclo,cyclop // one scenario asserting every part of the document
func TestCycloneDXFullDocument(t *testing.T) {
	db := &fakeDB{
		lic:   &models.LicenseScanResult{ScanStatus: "completed", SPDXExpressions: []string{"mit or apache-2.0"}},
		vuln:  scanned(testVuln),
		comps: []*models.Component{{Name: "lib", Version: "1.0.0", SHA256: testSHA}},
	}

	meta := map[string]any{"dependencies": []any{
		map[string]any{"name": "left-pad", "version": "1.3.0", "license": "MIT"},
		map[string]any{"name": "left-pad", "version": "1.3.0"},
	}}

	res, err := newFakeGen(db).GenerateCycloneDX(context.Background(), "r", "lib@1.0.0", "npm", meta, "1.6")
	if err != nil {
		t.Fatal(err)
	}

	var bom cdxBOM
	if err := json.Unmarshal(res.Body, &bom); err != nil {
		t.Fatal(err)
	}

	root := bom.Metadata.Component
	if bom.SpecVersion != "1.6" || root.PURL != "pkg:npm/lib@1.0.0" {
		t.Fatalf("spec=%s purl=%s", bom.SpecVersion, root.PURL)
	}

	if len(root.Hashes) != 1 || root.Hashes[0].Content != testSHA {
		t.Errorf("hashes = %+v", root.Hashes)
	}

	if len(root.Licenses) != 1 || root.Licenses[0].Expression != "MIT OR Apache-2.0" {
		t.Errorf("licenses = %+v", root.Licenses)
	}

	if len(bom.Components) != 1 || res.Components != 1 {
		t.Errorf("duplicate dependency not collapsed: %d components", len(bom.Components))
	}

	if len(bom.Dependencies) != 2 || bom.Dependencies[0].DependsOn[0] != "pkg:npm/left-pad@1.3.0" {
		t.Errorf("dependencies = %+v", bom.Dependencies)
	}

	if len(bom.Vulnerabilities) != 1 {
		t.Fatalf("vulnerabilities = %d", len(bom.Vulnerabilities))
	}

	v := bom.Vulnerabilities[0]
	if v.Ratings[0].Severity != "high" || *v.Ratings[0].Score != 7.5 || len(v.CWEs) != 1 || v.CWEs[0] != 79 {
		t.Errorf("vuln = %+v", v)
	}

	if len(v.Advisories) != 1 || v.Affects[0].Ref != root.BOMRef || v.Analysis.State != "in_triage" {
		t.Errorf("advisories=%+v affects=%+v analysis=%+v", v.Advisories, v.Affects, v.Analysis)
	}
}

func TestCycloneDXRejectsUnknownSpec(t *testing.T) {
	if _, err := NewGenerator(nil, nil).GenerateCycloneDX(context.Background(), "r", "a@1", "npm", nil, "1.3"); err == nil {
		t.Fatal("want an error for CycloneDX 1.3")
	}
}

func TestSPDXJSONAndTagValueFillLicensesChecksumsAndPurl(t *testing.T) {
	db := &fakeDB{
		lic:   &models.LicenseScanResult{ScanStatus: "completed", Licenses: []string{"Apache License, 2.0", "Weird Custom EULA"}},
		vuln:  scanned(testVuln),
		comps: []*models.Component{{Name: "lib", Version: "1.0.0", SHA256: testSHA}},
	}
	g := newFakeGen(db)

	res, err := g.GenerateSPDXJSON(context.Background(), "r", "lib@1.0.0", "npm", nil)
	if err != nil {
		t.Fatal(err)
	}

	var doc spdxDoc
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		t.Fatal(err)
	}

	p := doc.Packages[0]
	if p.LicenseConcluded != "Apache-2.0 AND LicenseRef-Weird-Custom-EULA" || p.LicenseDeclared == noAssertion {
		t.Errorf("licenses = %q / %q", p.LicenseConcluded, p.LicenseDeclared)
	}

	if len(doc.ExtractedLicenses) != 1 || doc.SPDXVersion != "SPDX-2.3" {
		t.Errorf("extracted = %+v version=%s", doc.ExtractedLicenses, doc.SPDXVersion)
	}

	if p.Checksums[0].Algorithm != "SHA256" || p.ExternalRefs[0].Locator != "pkg:npm/lib@1.0.0" || p.ExternalRefs[0].Type != "purl" {
		t.Errorf("package = %+v", p)
	}

	tv, err := g.GenerateSPDX(context.Background(), "r", "lib@1.0.0", "npm", nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"PackageChecksum: SHA256: " + testSHA,
		"PackageLicenseConcluded: Apache-2.0 AND LicenseRef-Weird-Custom-EULA",
		"ExternalRef: PACKAGE-MANAGER purl pkg:npm/lib@1.0.0",
		"Relationship: SPDXRef-DOCUMENT DESCRIBES SPDXRef-RootPackage",
		"LicenseID: LicenseRef-Weird-Custom-EULA",
	} {
		if !strings.Contains(tv.Text, want) {
			t.Errorf("tag-value missing %q:\n%s", want, tv.Text)
		}
	}
}

func TestSPDXLicensesWithoutScanFallBackToNoAssertion(t *testing.T) {
	res, err := NewGenerator(nil, nil).GenerateSPDX(context.Background(), "r", "lib@1", "npm", nil)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(res.Text, "PackageLicenseDeclared: NOASSERTION") {
		t.Errorf("unknown license must stay NOASSERTION:\n%s", res.Text)
	}
}

func TestCanonicalSPDXExpression(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"mit", "MIT", true},
		{"(MIT or Apache-2.0) and bsd-3-clause", "(MIT OR Apache-2.0) AND BSD-3-Clause", true},
		{"GPL-2.0-or-later WITH classpath-exception-2.0", "GPL-2.0-or-later WITH Classpath-exception-2.0", true},
		{"Apache-2.0+", "Apache-2.0+", true},
		{"Custom License", "", false},
		{"MIT AND", "", false},
		{"(MIT", "", false},
		{"", "", false},
	}

	for _, c := range cases {
		got, ok := canonicalSPDXExpression(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("canonicalSPDXExpression(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestFindingsStatusRules(t *testing.T) {
	decision := func(status, just string) *models.VEXStatement {
		return &models.VEXStatement{VulnID: "CVE-2024-0001", Status: status, Justification: just, ImpactStatement: "not reachable", UpdatedAt: time.Now()}
	}
	fixed := testVuln
	fixed.ID = "CVE-2024-0002"
	fixed.FixedIn = "1.0.0"

	cases := []struct {
		name        string
		in          *Input
		want        string
		wantAction  bool
		wantJustify string
	}{
		{"default is under_investigation", &Input{Version: "1.0.0", Scan: scanned(testVuln), Decisions: map[string]*models.VEXStatement{}}, "under_investigation", false, ""},
		{"quarantine means affected", &Input{Version: "1.0.0", Quarantined: true, Scan: scanned(testVuln), Decisions: map[string]*models.VEXStatement{}}, "affected", true, ""},
		{"fixed_in <= version means fixed", &Input{Version: "1.0.0", Scan: scanned(fixed), Decisions: map[string]*models.VEXStatement{}}, "fixed", false, ""},
		{"decision wins over quarantine", &Input{Version: "1.0.0", Quarantined: true, Scan: scanned(testVuln), Decisions: map[string]*models.VEXStatement{"CVE-2024-0001": decision("not_affected", "vulnerable_code_not_present")}}, "not_affected", false, "vulnerable_code_not_present"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := c.in.Findings()
			if len(f) != 1 || f[0].Status != c.want || (f[0].Action != "") != c.wantAction || f[0].Justification != c.wantJustify {
				t.Fatalf("findings = %+v", f)
			}
		})
	}
}

func TestOpenVEXDocument(t *testing.T) {
	db := &fakeDB{
		vulns: []*models.VulnScanResult{scanned(testVuln)},
		stmts: []*models.VEXStatement{
			{ArtifactID: "other@2", VulnID: "CVE-2023-9", Status: "not_affected", Justification: "component_not_present", ImpactStatement: "n/a", UpdatedAt: time.Now()},
		},
		comps: []*models.Component{{Version: "1.0.0", SHA256: testSHA}},
	}
	g := newFakeGen(db)

	inputs, err := g.VEXInputs(context.Background(), "r", "npm")
	if err != nil || len(inputs) != 2 {
		t.Fatalf("inputs = %d, err = %v", len(inputs), err)
	}

	res, err := g.GenerateOpenVEX(inputs)
	if err != nil {
		t.Fatal(err)
	}

	var doc openVEXDoc
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		t.Fatal(err)
	}

	if doc.Context != "https://openvex.dev/ns/v0.2.0" || len(doc.Statements) != 2 {
		t.Fatalf("doc = %+v", doc)
	}

	byVuln := map[string]openVEXStmt{}
	for _, s := range doc.Statements {
		byVuln[s.Vulnerability.Name] = s
	}

	if s := byVuln["CVE-2024-0001"]; s.Status != "under_investigation" || s.Products[0].Identifiers["purl"] != "pkg:npm/lib@1.0.0" || s.Products[0].Hashes["sha-256"] != testSHA {
		t.Errorf("scan statement = %+v", s)
	}

	if s := byVuln["CVE-2023-9"]; s.Status != "not_affected" || s.Justification != "component_not_present" {
		t.Errorf("decision statement = %+v", s)
	}
}

func TestCycloneDXVEXMapsAnalysis(t *testing.T) {
	in := &Input{
		Format: "npm", ArtifactID: "lib@1.0.0", Name: "lib", Version: "1.0.0", PURL: "pkg:npm/lib@1.0.0",
		Scan: scanned(testVuln),
		Decisions: map[string]*models.VEXStatement{"CVE-2024-0001": {
			VulnID: "CVE-2024-0001", Status: "not_affected", Justification: "vulnerable_code_not_in_execute_path", ImpactStatement: "never called",
		}},
	}

	res, err := NewGenerator(nil, nil).GenerateCycloneDXVEX([]*Input{in}, "")
	if err != nil {
		t.Fatal(err)
	}

	var bom cdxBOM
	if err := json.Unmarshal(res.Body, &bom); err != nil {
		t.Fatal(err)
	}

	a := bom.Vulnerabilities[0].Analysis
	if a.State != "not_affected" || a.Justification != "code_not_reachable" || a.Detail != "never called" {
		t.Errorf("analysis = %+v", a)
	}
}

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		format, v, fixed string
		ge, ok           bool
	}{
		{"npm", "1.0.5", "1.0.5", true, true},
		{"npm", "1.0.4", "1.0.5", false, true},
		{"npm", "1.10.0", "1.9.9", true, true},
		{"npm", "1.0.0-rc1", "1.0.0", false, true},
		{"npm", "2.0.0", "1.0.0-rc1", true, true},
		{"npm", "unknown", "1.0.0", false, false},
		{"maven", "1.2.3", "1.2.3", true, true},
		{"npm", "abc", "1.0.0", false, false},
	}

	for _, c := range cases {
		ge, ok := versionAtLeast(c.format, c.v, c.fixed)
		if ge != c.ge || ok != c.ok {
			t.Errorf("versionAtLeast(%s, %q, %q) = %v,%v want %v,%v", c.format, c.v, c.fixed, ge, ok, c.ge, c.ok)
		}
	}

	if !fixedByAny("npm", "2.1.0", "1.2.9, 2.0.4") || fixedByAny("npm", "2.0.1", "1.2.9, 2.0.4") {
		t.Error("multi-version fixed_in must be matched per major line")
	}
}

func TestGeneratePURL(t *testing.T) {
	cases := map[[3]string]string{
		{"npm", "@babel/core", "7.0.0"}:    "pkg:npm/%40babel/core@7.0.0",
		{"maven", "org.x:lib", "1.0+b"}:    "pkg:maven/org.x/lib@1.0%2Bb",
		{"pypi", "My_Pkg", "1.0"}:          "pkg:pypi/my-pkg@1.0",
		{"go", "github.com/a/b", "v1.2.3"}: "pkg:golang/github.com/a/b@v1.2.3",
		{"raw", "file.bin", "unknown"}:     "pkg:generic/file.bin",
	}

	for in, want := range cases {
		if got := generatePURL(in[0], in[1], in[2]); got != want {
			t.Errorf("generatePURL%v = %q, want %q", in, got, want)
		}
	}
}
