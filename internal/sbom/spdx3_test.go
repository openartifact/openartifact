// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// spdx3Fixture is a document with licenses (SPDX and custom), hashes, dependencies,
// a scanned vulnerability and a not_affected decision.
func spdx3Fixture(t *testing.T) *SBOMResult {
	t.Helper()

	db := &fakeDB{
		lic:  &models.LicenseScanResult{ScanStatus: "completed", Licenses: []string{"Apache License, 2.0", "Weird Custom EULA"}},
		vuln: scanned(testVuln, models.Vulnerability{ID: "GHSA-aaaa-bbbb-cccc", Summary: "other"}),
		stmts: []*models.VEXStatement{{
			ArtifactID: "lib@1.0.0", VulnID: "GHSA-aaaa-bbbb-cccc", Status: models.VEXNotAffected,
			Justification: models.VEXVulnerableCodeNotInExec, ImpactStatement: "never reached",
		}},
		comps: []*models.Component{{Name: "lib", Version: "1.0.0", SHA256: testSHA}},
	}
	db.vulns = []*models.VulnScanResult{db.vuln}

	meta := map[string]any{"dependencies": []any{
		map[string]any{"name": "left-pad", "version": "1.3.0", "license": "MIT", "sha256": testSHA},
		map[string]any{"name": "util", "version": "2.0.0"},
	}}

	res, err := newFakeGen(db).GenerateSPDX3JSON(context.Background(), "r", "lib@1.0.0", "npm", meta)
	if err != nil {
		t.Fatal(err)
	}

	return res
}

func graphByType(t *testing.T, res *SBOMResult) map[string][]map[string]any {
	t.Helper()

	var doc struct {
		Context string           `json:"@context"`
		Graph   []map[string]any `json:"@graph"`
	}

	if err := json.Unmarshal(res.Body, &doc); err != nil {
		t.Fatal(err)
	}

	if doc.Context != spdx3Context {
		t.Errorf("@context = %q", doc.Context)
	}

	out := map[string][]map[string]any{}

	for _, el := range doc.Graph {
		typ, _ := el["type"].(string)
		out[typ] = append(out[typ], el)
	}

	return out
}

func TestSPDX3JSONStructure(t *testing.T) {
	res := spdx3Fixture(t)

	if res.Format != "spdx3-json" || res.SpecVersion != SPDX30 || res.ContentType != "application/ld+json" || res.Components != 2 {
		t.Fatalf("result = %s %s %s %d", res.Format, res.SpecVersion, res.ContentType, res.Components)
	}

	g := graphByType(t, res)

	if len(g["software_Package"]) != 3 || len(g["SpdxDocument"]) != 1 || len(g["software_Sbom"]) != 1 || len(g["CreationInfo"]) != 1 {
		t.Fatalf("graph counts: pkg=%d doc=%d sbom=%d ci=%d", len(g["software_Package"]), len(g["SpdxDocument"]), len(g["software_Sbom"]), len(g["CreationInfo"]))
	}

	root := g["software_Package"][0]
	if root["software_packageUrl"] != "pkg:npm/lib@1.0.0" || root["software_packageVersion"] != "1.0.0" {
		t.Errorf("root = %v", root)
	}

	hashes, _ := root["verifiedUsing"].([]any)
	if len(hashes) != 1 || hashes[0].(map[string]any)["hashValue"] != testSHA {
		t.Errorf("hashes = %v", root["verifiedUsing"])
	}

	if len(g["expandedlicensing_CustomLicense"]) != 1 {
		t.Errorf("custom licenses = %d", len(g["expandedlicensing_CustomLicense"]))
	}

	vex := g["security_VexNotAffectedVulnAssessmentRelationship"]
	if len(vex) != 1 || vex[0]["security_justificationType"] != "vulnerableCodeNotInExecutePath" || vex[0]["relationshipType"] != "doesNotAffect" {
		t.Errorf("not-affected assessments = %v", vex)
	}

	if len(g["security_VexUnderInvestigationVulnAssessmentRelationship"]) != 1 {
		t.Errorf("under-investigation assessments = %d", len(g["security_VexUnderInvestigationVulnAssessmentRelationship"]))
	}
}

func TestSPDX3NoDatabase(t *testing.T) {
	res, err := NewGenerator(nil, nil).GenerateSPDX3JSON(context.Background(), "r", "lib@1", "npm", nil)
	if err != nil {
		t.Fatal(err)
	}

	g := graphByType(t, res)
	if len(g["software_Package"]) != 1 || len(g["Relationship"]) != 0 {
		t.Errorf("minimal graph = %v", g)
	}
}

// TestSPDX3ValidatesWithOfficialValidator runs the SPDX project's spdx3-validate
// (https://github.com/spdx/spdx3-validate) on the generated document. It needs
// the tool (SPDX3_VALIDATE=/path/to/spdx3-validate or on PATH) and network access
// for the schemas, so it is skipped when unavailable.
func TestSPDX3ValidatesWithOfficialValidator(t *testing.T) {
	bin := os.Getenv("SPDX3_VALIDATE")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("spdx3-validate"); err != nil {
			t.Skip("spdx3-validate not installed")
		}
	}

	path := filepath.Join(t.TempDir(), "sbom.spdx3.json")
	if err := os.WriteFile(path, spdx3Fixture(t).Body, 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := exec.CommandContext(context.Background(), bin, "--json", path, "--spdx-version", SPDX30).CombinedOutput()
	if err != nil {
		t.Fatalf("spdx3-validate rejected the document: %v\n%s", err, out)
	}
}
