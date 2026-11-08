// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/licensepolicy/policytest"
	"github.com/openartifact/openartifact/internal/secrets"
	"github.com/openartifact/openartifact/internal/vuln"
	"github.com/openartifact/openartifact/internal/webhook"
)

// eventsDB lets the vuln handler read the repository a scan belongs to.
type eventsDB struct{ *scanPolicyDB }

func (db eventsDB) GetRepositoryByID(_ context.Context, id string) (*models.Repository, error) {
	return &models.Repository{ID: id, Name: "repo", Format: "npm", RepoType: "hosted"}, nil
}

// eventNames lists the events a publisher saw, in order.
func eventNames(p *recordingPublisher) []string {
	var out []string
	for _, e := range p.all() {
		out = append(out, e.event)
	}

	return out
}

func wantEvents(t *testing.T, what string, p *recordingPublisher, want ...string) {
	t.Helper()

	got := eventNames(p)
	if len(got) != len(want) {
		t.Fatalf("%s: events = %v, want %v", what, got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: events = %v, want %v", what, got, want)
		}
	}
}

func TestVulnScanEmitsScanEvents(t *testing.T) {
	tests := []struct {
		name     string
		backends map[string]vuln.ScannerBackend
		scanners string
		want     []string
		total    int
	}{
		{
			"findings", map[string]vuln.ScannerBackend{"osv": stubScanner{name: "osv", vulns: []models.Vulnerability{
				{ID: "GHSA-1", Severity: "CRITICAL"}, {ID: "GHSA-2", Severity: "HIGH"},
			}}}, "osv", []string{webhook.EventScanCompleted, webhook.EventVulnerabilityFound}, 2,
		},
		{"clean", map[string]vuln.ScannerBackend{"osv": stubScanner{name: "osv"}}, "osv", []string{webhook.EventScanCompleted}, 0},
		{
			"failed scanner", map[string]vuln.ScannerBackend{"osv": stubScanner{name: "osv", err: errScannerBroke}}, "osv",
			[]string{webhook.EventScanCompleted}, 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := eventsDB{newScanPolicyDB()}
			pub := &recordingPublisher{}
			h := newScanPolicyVulnHandler(t, db.scanPolicyDB, tt.backends)
			h.db = db
			h.WithPublisher(pub)

			h.performScan(t.Context(), &models.VulnScanResult{
				ID: testScanID, RepositoryID: testRepoID, ArtifactID: "a@1", Scanner: tt.scanners, Ecosystem: "npm",
				PackageName: "a", PackageVersion: "1",
			}, vuln.Package{Name: "a", Version: "1", Ecosystem: "npm"})

			wantEvents(t, tt.name, pub, tt.want...)

			scan, _ := pub.all()[0].payload["scan"].(map[string]any)
			counts, _ := scan["counts"].(map[string]any)

			if scan["kind"] != string(webhook.ScanKindVulnerability) || scan["id"] != testScanID || counts["total"] != tt.total {
				t.Errorf("scan block = %v", scan)
			}

			if pub.all()[0].repoID != testRepoID {
				t.Errorf("event repository id = %q", pub.all()[0].repoID)
			}
		})
	}
}

func TestLicenseScanEmitsEvents(t *testing.T) {
	block := policytest.Policy("block", nil, []string{"GPL-3.0"})

	tests := []struct {
		name   string
		policy *models.LicensePolicy
		runs   int
		want   []string
	}{
		{"no policy", nil, 1, []string{webhook.EventScanCompleted}},
		{"warn policy", policytest.Policy("warn", nil, []string{"GPL-3.0"}), 1, []string{webhook.EventScanCompleted}},
		{"block quarantines", block, 1, []string{webhook.EventScanCompleted, webhook.EventQuarantineChanged}},
		{
			"a repeat scan does not announce the same quarantine twice", block, 2,
			[]string{webhook.EventScanCompleted, webhook.EventQuarantineChanged, webhook.EventScanCompleted},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newScanPolicyDB()
			db.props["license"] = "GPL-3.0"

			if tt.policy != nil {
				db.policies = []*models.LicensePolicy{tt.policy}
			}

			pub := &recordingPublisher{}
			h := newLicenseHandlerFor(t, db).WithPublisher(pub)

			for range tt.runs {
				runLicenseScan(t, h)
			}

			wantEvents(t, tt.name, pub, tt.want...)
		})
	}
}

func TestLicenseQuarantineReviewEmitsOnlyChanges(t *testing.T) {
	db := newScanPolicyDB()
	db.props["license"] = "GPL-3.0"
	db.policies = []*models.LicensePolicy{policytest.Policy("block", nil, []string{"GPL-3.0"})}

	pub := &recordingPublisher{}
	h := newLicenseHandlerFor(t, db).WithPublisher(pub)
	runLicenseScan(t, h)

	path := "/api/v1/repositories/repo/license/scans/" + quarantineScanID + "/quarantine"

	for _, body := range []string{`{"quarantined":false}`, `{"quarantined":false}`, `{"quarantined":true}`} {
		if rw := serveAs(h.RegisterRoutes, http.MethodPost, path, body); rw.Code != http.StatusOK {
			t.Fatalf("%s: status = %d (%s)", body, rw.Code, rw.Body.String())
		}
	}

	wantEvents(t, "review", pub,
		webhook.EventScanCompleted, webhook.EventQuarantineChanged, // the scan quarantines
		webhook.EventQuarantineChanged, // release
		webhook.EventQuarantineChanged) // quarantined again

	states := []string{}

	for _, e := range pub.all()[1:] {
		q, _ := e.payload["quarantine"].(map[string]any)
		s, _ := q["state"].(string)
		states = append(states, s)

		if e.payload["event"] != webhook.EventQuarantineChanged || e.repoID != testRepoID {
			t.Errorf("event = %+v", e)
		}
	}

	if want := []string{"quarantined", "released", "quarantined"}; !slices.Equal(states, want) {
		t.Errorf("states = %v, want %v", states, want)
	}
}

func TestPropertiesLicenseKeyEmitsEvents(t *testing.T) {
	db := newScanPolicyDB()
	db.policies = []*models.LicensePolicy{policytest.Policy("block", nil, []string{"GPL-3.0"})}
	log, _ := newBundlesScansEnv(t, &bundlesScansMockDB{})
	pub := &recordingPublisher{}
	h := NewPropertiesHandler(log, db, nil).WithPublisher(pub)
	register := func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }
	url := "/api/v1/repositories/repo/artifacts/leftpad@1.0.0/properties"

	if rw := serveAs(register, http.MethodPatch, url, `{"team":"blue"}`); rw.Code != http.StatusOK {
		t.Fatalf("status = %d", rw.Code)
	}

	wantEvents(t, "unrelated property", pub)

	if rw := serveAs(register, http.MethodPatch, url, `{"license":"GPL-3.0"}`); rw.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
	}

	wantEvents(t, "license property", pub, webhook.EventScanCompleted, webhook.EventQuarantineChanged)
}

// secretEventsDB stores the scan a secret scan request creates.
type secretEventsDB struct{ *secretScanStoreDB }

func (db secretEventsDB) CreateSecretScanResult(_ context.Context, r *models.SecretScanResult) error {
	r.ID = secretScanID
	db.scans[r.RepositoryID] = append(db.scans[r.RepositoryID], r)

	return nil
}

func TestSecretsEmitEvents(t *testing.T) {
	db := secretEventsDB{&secretScanStoreDB{scans: map[string][]*models.SecretScanResult{}}}
	log, _ := newBundlesScansEnv(t, &bundlesScansMockDB{})
	pub := &recordingPublisher{}
	h := NewSecretsHandler(db, log, secrets.NewScanner(log)).WithPublisher(pub)
	register := func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }
	base := "/api/v1/repositories/raw/secrets/"

	body := `{"artifact_id":".env","content":"-----BEGIN RSA PRIVATE KEY-----"}`
	if rw := serveAs(register, http.MethodPost, base+"scan", body); rw.Code != http.StatusOK {
		t.Fatalf("scan: status = %d (%s)", rw.Code, rw.Body.String())
	}

	wantEvents(t, "scan", pub, webhook.EventScanCompleted)

	scan, _ := pub.all()[0].payload["scan"].(map[string]any)
	if scan["kind"] != string(webhook.ScanKindSecret) || scan["status"] != "flagged" {
		t.Errorf("scan block = %v", scan)
	}

	for _, q := range []string{`{"quarantined":true}`, `{"quarantined":true}`, `{"quarantined":false}`} {
		if rw := serveAs(register, http.MethodPost, base+"scans/"+secretScanID+"/quarantine", q); rw.Code != http.StatusOK {
			t.Fatalf("%s: status = %d (%s)", q, rw.Code, rw.Body.String())
		}
	}

	wantEvents(t, "review", pub, webhook.EventScanCompleted, webhook.EventQuarantineChanged, webhook.EventQuarantineChanged)
}
