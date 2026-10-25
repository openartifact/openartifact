// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/scanexport"
)

const exportScanID = "55555555-5555-5555-5555-555555555555"

type scanExportMockDB struct {
	databasetest.BaseMockDB

	// pages is served in order, one per ListVulnScanOverview call.
	pages     []*models.VulnScanOverviewPage
	listCalls int
	params    []models.VulnScanListParams
	full      map[string]*models.VulnScanResult
	secrets   []*models.SecretScanResult
}

func (db *scanExportMockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if name == "missing" {
		return nil, nil
	}

	return &models.Repository{ID: testRepoID, Name: name, Format: "npm"}, nil
}

func (db *scanExportMockDB) GetVulnScanResultByID(_ context.Context, _, id string) (*models.VulnScanResult, error) {
	return db.full[id], nil
}

func (db *scanExportMockDB) ListVulnScanOverview(_ context.Context, p models.VulnScanListParams) (*models.VulnScanOverviewPage, error) {
	db.params = append(db.params, p)
	page := db.pages[db.listCalls]
	db.listCalls++

	return page, nil
}

func (db *scanExportMockDB) ListSecretScanResults(_ context.Context, _ string, flaggedOnly bool) ([]*models.SecretScanResult, error) {
	var out []*models.SecretScanResult

	for _, s := range db.secrets {
		if !flaggedOnly || s.ScanStatus == "flagged" {
			out = append(out, s)
		}
	}

	return out, nil
}

func exportServe(t *testing.T, db *scanExportMockDB, target string) (int, http.Header, string) {
	t.Helper()

	log, ev := newBundlesScansEnv(t, &bundlesScansMockDB{})
	vh := NewVulnHandler(db, log, ev, nil)
	sh := NewSecretsHandler(db, log, nil)

	rw := serveAs(func(r chi.Router) {
		vh.RegisterRoutes(r, "/api/v1/repositories/{name}")
		vh.RegisterOverviewRoutes(r)
		sh.RegisterRoutes(r, "/api/v1/repositories/{name}")
	}, http.MethodGet, target, "")

	return rw.Code, rw.Header(), rw.Body.String()
}

func exportScan(id string, vulns ...models.Vulnerability) *models.VulnScanResult {
	return &models.VulnScanResult{
		ID: id, ArtifactID: "lodash@4.17.20", PackageName: "lodash", PackageVersion: "4.17.20", Ecosystem: "npm",
		Scanner: "osv", ScanStatus: "completed", Vulnerabilities: vulns, VulnCount: len(vulns),
	}
}

func TestExportVulnScan_Formats(t *testing.T) {
	db := &scanExportMockDB{full: map[string]*models.VulnScanResult{
		exportScanID: exportScan(exportScanID, models.Vulnerability{ID: "CVE-1", Severity: "HIGH", Summary: "=cmd|' /C calc'!A0"}),
	}}
	base := "/api/v1/repositories/npm-main/vuln/scans/" + exportScanID + "/export"

	code, h, body := exportServe(t, db, base+"?format=sarif")
	if code != http.StatusOK || h.Get("Content-Type") != "application/sarif+json" ||
		!strings.Contains(h.Get("Content-Disposition"), `filename="vuln-npm-main-`+exportScanID+`.sarif"`) {
		t.Fatalf("sarif: %d %v", code, h)
	}

	var doc scanexport.Log
	if err := json.Unmarshal([]byte(body), &doc); err != nil || doc.Version != "2.1.0" || doc.Runs[0].Results[0].RuleID != "CVE-1" {
		t.Fatalf("sarif body: %v %s", err, body)
	}

	code, h, body = exportServe(t, db, base+"?format=csv")
	if code != http.StatusOK || h.Get("Content-Type") != "text/csv; charset=utf-8" || !strings.Contains(body, "'=cmd") {
		t.Fatalf("csv: %d %v %s", code, h, body)
	}

	code, h, body = exportServe(t, db, base)
	if code != http.StatusOK || h.Get("Content-Type") != "application/json" || !strings.Contains(body, `"vulnerabilities"`) {
		t.Fatalf("json: %d %v %s", code, h, body)
	}

	if code, _, _ = exportServe(t, db, base+"?format=xml"); code != http.StatusBadRequest {
		t.Fatalf("bad format = %d", code)
	}

	for _, target := range []string{
		"/api/v1/repositories/missing/vuln/scans/" + exportScanID + "/export",
		"/api/v1/repositories/npm-main/vuln/scans/not-a-uuid/export",
		"/api/v1/repositories/npm-main/vuln/scans/66666666-6666-6666-6666-666666666666/export",
	} {
		if code, _, _ = exportServe(t, db, target); code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", target, code)
		}
	}
}

func TestExportVulnOverview_PagesThroughAllFiltersAndSkipsEmpty(t *testing.T) {
	row := func(id, repo string, n int) *models.VulnScanOverviewRow {
		s := exportScan(id)
		s.VulnCount = n

		return &models.VulnScanOverviewRow{VulnScanResult: *s, RepositoryName: repo}
	}

	db := &scanExportMockDB{
		pages: []*models.VulnScanOverviewPage{
			{Scans: []*models.VulnScanOverviewRow{row("a", "r1", 1), row("clean", "r1", 0)}, Next: &models.VulnScanCursor{ID: "clean"}},
			{Scans: []*models.VulnScanOverviewRow{row("b", "r2", 1)}},
		},
		full: map[string]*models.VulnScanResult{
			"a": exportScan("a", models.Vulnerability{ID: "CVE-A", Severity: "CRITICAL"}),
			"b": exportScan("b", models.Vulnerability{ID: "CVE-B", Severity: "LOW"}),
		},
	}

	code, h, body := exportServe(t, db, "/api/v1/vuln/scans/export?format=csv&repo=r1&severity=low&scanner=osv&limit=1")
	if code != http.StatusOK {
		t.Fatalf("status %d %s", code, body)
	}

	if db.listCalls != 2 || db.params[0].Repo != "r1" || db.params[0].Severity != "low" || db.params[0].Scanner != "osv" ||
		db.params[0].Limit != vulnOverviewMaxLimit || db.params[0].After != nil || db.params[1].After == nil {
		t.Fatalf("overview queries = %+v", db.params)
	}

	if !strings.Contains(body, "CVE-A") || !strings.Contains(body, "CVE-B") || h.Get(exportTruncatedHeader) != "" {
		t.Fatalf("body %s headers %v", body, h)
	}

	// The JSON form lists every scan, with its repository name.
	db.listCalls = 0

	_, _, body = exportServe(t, db, "/api/v1/vuln/scans/export")
	if !strings.Contains(body, `"repository_name":"r2"`) || strings.Count(body, `"artifact_id"`) != 3 {
		t.Fatalf("json body %s", body)
	}

	if code, _, _ = exportServe(t, db, "/api/v1/vuln/scans/export?severity=bogus"); code != http.StatusBadRequest {
		t.Fatalf("bad filter = %d", code)
	}
}

func secretScans() []*models.SecretScanResult {
	return []*models.SecretScanResult{
		{ID: "ss1", ArtifactID: ".env", ScanStatus: "flagged", FindingCount: 1, Findings: []models.SecretFinding{
			{Type: "aws_access_key", Description: "AWS Access Key ID", Severity: "critical", RedactedValue: "AKIA****LEAK", LineHint: "hint-LEAK"},
		}},
		{ID: "ss2", ArtifactID: "ok.txt", ScanStatus: "clean"},
	}
}

func TestExportSecretScans_NeverLeakValues(t *testing.T) {
	db := &scanExportMockDB{secrets: secretScans()}

	for _, format := range []string{"sarif", "csv", "json"} {
		for _, target := range []string{
			"/api/v1/repositories/raw/secrets/scans/export?format=" + format,
			"/api/v1/repositories/raw/secrets/scans/ss1/export?format=" + format,
		} {
			code, _, body := exportServe(t, db, target)
			if code != http.StatusOK || !strings.Contains(body, "aws_access_key") {
				t.Fatalf("%s: %d %s", target, code, body)
			}

			if strings.Contains(body, "LEAK") || strings.Contains(body, "AKIA") || strings.Contains(body, "redacted") || strings.Contains(body, "line_hint") {
				t.Fatalf("%s leaks a secret value: %s", target, body)
			}
		}
	}

	_, _, body := exportServe(t, db, "/api/v1/repositories/raw/secrets/scans/export?flagged=true")
	if strings.Contains(body, "ok.txt") {
		t.Fatalf("flagged filter ignored: %s", body)
	}

	for target, want := range map[string]int{
		"/api/v1/repositories/raw/secrets/scans/nope/export":      http.StatusNotFound,
		"/api/v1/repositories/missing/secrets/scans/export":       http.StatusNotFound,
		"/api/v1/repositories/raw/secrets/scans/export?format=xx": http.StatusBadRequest,
	} {
		if code, _, _ := exportServe(t, db, target); code != want {
			t.Errorf("%s = %d, want %d", target, code, want)
		}
	}
}

func TestExportOverview_AdminOnly(t *testing.T) {
	rw := serveOverview(t, &vulnOverviewMockDB{}, nil, "/api/v1/vuln/scans/export")
	if rw.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rw.Code)
	}
}
