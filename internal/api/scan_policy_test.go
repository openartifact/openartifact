// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/bgtask"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/licensepolicy/policytest"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/vuln"
)

const quarantineScanID = "66666666-6666-6666-6666-666666666666"

var errScannerBroke = errors.New("exec /usr/bin/trivy: permission denied (db token=hunter2)")

// scanPolicyDB is an in-memory database for the vuln scan and license policy
// handler tests.
type scanPolicyDB struct {
	databasetest.BaseMockDB

	mu sync.Mutex

	policies  []*models.LicensePolicy
	props     map[string]string
	licenses  map[string]*models.LicenseScanResult // by artifact ID
	created   []*models.VulnScanResult
	updated   chan *models.VulnScanResult
	setErr    error
	approvals int
}

func newScanPolicyDB() *scanPolicyDB {
	return &scanPolicyDB{
		props:    map[string]string{},
		licenses: map[string]*models.LicenseScanResult{},
		updated:  make(chan *models.VulnScanResult, 4),
	}
}

func (db *scanPolicyDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return &models.Repository{ID: testRepoID, Name: name, Format: "npm"}, nil
}

func (db *scanPolicyDB) ListLicensePolicies(context.Context, string) ([]*models.LicensePolicy, error) {
	return db.policies, nil
}

func (db *scanPolicyDB) ListArtifactProperties(context.Context, string, string) (map[string]string, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	out := make(map[string]string, len(db.props))
	maps.Copy(out, db.props)

	return out, nil
}

func (db *scanPolicyDB) SetArtifactProperties(_ context.Context, _, _ string, props map[string]string, replace bool) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if replace {
		db.props = map[string]string{}
	}

	maps.Copy(db.props, props)

	return nil
}

func (db *scanPolicyDB) GetLicenseScanResult(_ context.Context, _, artifactID string) (*models.LicenseScanResult, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	if r := db.licenses[artifactID]; r != nil {
		cp := *r

		return &cp, nil
	}

	return nil, nil
}

func (db *scanPolicyDB) CreateLicenseScanResult(_ context.Context, r *models.LicenseScanResult) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	cp := *r
	if cp.ID == "" {
		cp.ID = quarantineScanID
	}

	r.ID = cp.ID
	db.licenses[r.ArtifactID] = &cp

	return nil
}

func (db *scanPolicyDB) GetLicenseScanResultByID(_ context.Context, _, id string) (*models.LicenseScanResult, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	for _, r := range db.licenses {
		if r.ID == id {
			cp := *r

			return &cp, nil
		}
	}

	return nil, nil
}

func (db *scanPolicyDB) SetLicenseScanQuarantine(_ context.Context, _, id string, quarantined bool) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.setErr != nil {
		return db.setErr
	}

	for _, r := range db.licenses {
		if r.ID == id {
			r.Quarantined = quarantined
			r.QuarantineApproved = !quarantined
			db.approvals++

			return nil
		}
	}

	return database.ErrLicenseScanNotFound
}

func (db *scanPolicyDB) CreateVulnScanResult(_ context.Context, r *models.VulnScanResult) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	r.ID = testScanID
	cp := *r
	db.created = append(db.created, &cp)

	return nil
}

func (db *scanPolicyDB) UpdateVulnScanResult(_ context.Context, r *models.VulnScanResult) error {
	cp := *r
	db.updated <- &cp

	return nil
}

// stubScanner is a vuln.ScannerBackend with a canned answer.
type stubScanner struct {
	name  string
	vulns []models.Vulnerability
	err   error
}

func (s stubScanner) Name() string { return s.name }

func (s stubScanner) Scan(context.Context, vuln.Package) ([]models.Vulnerability, error) {
	return s.vulns, s.err
}

func newScanPolicyVulnHandler(t *testing.T, db *scanPolicyDB, backends map[string]vuln.ScannerBackend) *VulnHandler {
	t.Helper()

	log, ev := newBundlesScansEnv(t, &bundlesScansMockDB{})
	h := NewVulnHandler(db, log, ev, nil)
	h.newBackend = func(name string) vuln.ScannerBackend { return backends[name] }

	return h
}

func serveWithPrincipal(register func(chi.Router), p *auth.Principal, method, path, body string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), p)))
		})
	})
	register(router)

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body)))

	return rw
}

func waitVulnUpdate(t *testing.T, db *scanPolicyDB) *models.VulnScanResult {
	t.Helper()

	select {
	case r := <-db.updated:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("the background scan never stored its result")

		return nil
	}
}

func TestVulnScan_FailureIsRecordedNotClean(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		backends     map[string]vuln.ScannerBackend
		wantScanner  string
		wantStatus   string
		wantFailed   []string
		wantVulns    int
		wantErrParts []string
	}{
		{
			name:        "only scanner is missing",
			body:        `{"artifact_id":"left-pad@1.0.0","package_name":"left-pad","package_version":"1.0.0","ecosystem":"npm","scanner":"trivy"}`,
			backends:    map[string]vuln.ScannerBackend{"trivy": stubScanner{name: "trivy", err: &vuln.ScanError{Scanner: "trivy", Kind: vuln.FailureUnavailable, Err: errScannerBroke}}},
			wantScanner: "trivy", wantStatus: "failed", wantFailed: []string{"trivy"},
			wantErrParts: []string{"trivy: scanner is not available on the server"},
		},
		{
			name:        "default scanner (osv) cannot reach its database",
			body:        `{"artifact_id":"left-pad@1.0.0","package_name":"left-pad","package_version":"1.0.0","ecosystem":"npm"}`,
			backends:    map[string]vuln.ScannerBackend{"osv": stubScanner{name: "osv", err: errScannerBroke}},
			wantScanner: "osv", wantStatus: "failed", wantFailed: []string{"osv"},
			wantErrParts: []string{"osv: scan failed"},
		},
		{
			name: "two scanners, one fails",
			body: `{"artifact_id":"left-pad@1.0.0","package_name":"left-pad","package_version":"1.0.0","ecosystem":"npm","scanners":["trivy","osv"]}`,
			backends: map[string]vuln.ScannerBackend{
				"osv":   stubScanner{name: "osv", vulns: []models.Vulnerability{{ID: "GHSA-1", Severity: "HIGH"}}},
				"trivy": stubScanner{name: "trivy", err: &vuln.ScanError{Scanner: "trivy", Kind: vuln.FailureTimeout, Err: errScannerBroke}},
			},
			wantScanner: "osv,trivy", wantStatus: "partial", wantFailed: []string{"trivy"}, wantVulns: 1,
			wantErrParts: []string{"trivy: scan timed out"},
		},
		{
			name: "two scanners both fail",
			body: `{"artifact_id":"left-pad@1.0.0","package_name":"left-pad","package_version":"1.0.0","ecosystem":"npm","scanners":["grype","trivy"]}`,
			backends: map[string]vuln.ScannerBackend{
				"grype": stubScanner{name: "grype", err: &vuln.ScanError{Scanner: "grype", Kind: vuln.FailureExecution, Err: errScannerBroke}},
				"trivy": stubScanner{name: "trivy", err: &vuln.ScanError{Scanner: "trivy", Kind: vuln.FailureInvalidOutput, Err: errScannerBroke}},
			},
			wantScanner: "grype,trivy", wantStatus: "failed", wantFailed: []string{"grype", "trivy"},
			wantErrParts: []string{"grype: scanner exited with an error", "trivy: scanner output could not be parsed"},
		},
		{
			name: "every scanner works",
			body: `{"artifact_id":"left-pad@1.0.0","package_name":"left-pad","package_version":"1.0.0","ecosystem":"npm","scanners":["osv","grype"]}`,
			backends: map[string]vuln.ScannerBackend{
				"osv":   stubScanner{name: "osv", vulns: []models.Vulnerability{{ID: "GHSA-1", Severity: "CRITICAL"}}},
				"grype": stubScanner{name: "grype", vulns: []models.Vulnerability{{ID: "GHSA-1", Severity: "CRITICAL"}, {ID: "CVE-2", Severity: "LOW"}}},
			},
			wantScanner: "grype,osv", wantStatus: "completed", wantFailed: []string{}, wantVulns: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newScanPolicyDB()
			h := newScanPolicyVulnHandler(t, db, tt.backends)
			register := func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }

			rw := serveAs(register, http.MethodPost, "/api/v1/repositories/repo/vuln/scan", tt.body)
			if rw.Code != http.StatusOK {
				t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
			}

			var started struct {
				ScanStatus     string   `json:"scan_status"`
				Scanner        string   `json:"scanner"`
				FailedScanners []string `json:"failed_scanners"`
			}
			if err := json.Unmarshal(rw.Body.Bytes(), &started); err != nil {
				t.Fatal(err)
			}

			if started.ScanStatus != "scanning" || started.Scanner != tt.wantScanner || started.FailedScanners == nil {
				t.Errorf("start response = %+v, want scanning/%s with a non-nil failed_scanners", started, tt.wantScanner)
			}

			stored := waitVulnUpdate(t, db)

			if stored.ScanStatus != tt.wantStatus || stored.VulnCount != tt.wantVulns {
				t.Errorf("stored status=%q vulns=%d, want %q/%d", stored.ScanStatus, stored.VulnCount, tt.wantStatus, tt.wantVulns)
			}

			if !slices.Equal(stored.FailedScanners, tt.wantFailed) {
				t.Errorf("FailedScanners = %v, want %v", stored.FailedScanners, tt.wantFailed)
			}

			for _, part := range tt.wantErrParts {
				if !strings.Contains(stored.ScanError, part) {
					t.Errorf("ScanError = %q, want it to contain %q", stored.ScanError, part)
				}
			}

			if tt.wantStatus == "completed" && stored.ScanError != "" {
				t.Errorf("a completed scan has no error, got %q", stored.ScanError)
			}

			// What the API returns for a stored result never carries raw error text.
			out, err := json.Marshal(vulnScanResultToMap(stored))
			if err != nil {
				t.Fatal(err)
			}

			for _, leaked := range []string{"hunter2", "permission denied", "/usr/bin"} {
				if strings.Contains(string(out), leaked) {
					t.Errorf("API response leaks %q: %s", leaked, out)
				}
			}
		})
	}
}

func TestVulnScan_ScannersValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"unknown scanner", `{"artifact_id":"a","package_name":"p","package_version":"1","ecosystem":"npm","scanners":["osv","nessus"]}`, http.StatusBadRequest},
		{"unknown single scanner", `{"artifact_id":"a","package_name":"p","package_version":"1","ecosystem":"npm","scanner":"nessus"}`, http.StatusBadRequest},
		{"empty scanner name", `{"artifact_id":"a","package_name":"p","package_version":"1","ecosystem":"npm","scanners":[""]}`, http.StatusBadRequest},
		{"scanner and scanners merge, duplicates collapse", `{"artifact_id":"a","package_name":"p","package_version":"1","ecosystem":"npm","scanner":"OSV","scanners":["osv","trivy"]}`, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newScanPolicyDB()
			h := newScanPolicyVulnHandler(t, db, map[string]vuln.ScannerBackend{
				"osv": stubScanner{name: "osv"}, "trivy": stubScanner{name: "trivy"},
			})
			register := func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }

			rw := serveAs(register, http.MethodPost, "/api/v1/repositories/repo/vuln/scan", tt.body)
			if rw.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rw.Code, tt.want, rw.Body.String())
			}

			if tt.want == http.StatusOK {
				if got := waitVulnUpdate(t, db); got.Scanner != "osv,trivy" || got.ScanStatus != "completed" {
					t.Errorf("stored scanner=%q status=%q, want osv,trivy/completed", got.Scanner, got.ScanStatus)
				}

				return
			}

			if len(db.created) != 0 {
				t.Error("an invalid request must not create a scan row")
			}
		})
	}
}

func newLicenseHandlerFor(t *testing.T, db *scanPolicyDB) *LicenseHandler {
	t.Helper()

	log, ev := newBundlesScansEnv(t, &bundlesScansMockDB{})

	return NewLicenseHandler(db, log, ev, nil, nil)
}

const licenseScanBody = `{"artifact_id":"leftpad@1.0.0"}`

func runLicenseScan(t *testing.T, h *LicenseHandler) map[string]any {
	t.Helper()

	rw := serveAs(h.RegisterRoutes, http.MethodPost, "/api/v1/repositories/repo/license/scan", licenseScanBody)
	if rw.Code != http.StatusOK {
		t.Fatalf("license scan: status = %d (%s)", rw.Code, rw.Body.String())
	}

	var out map[string]any
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	return out
}

func TestLicenseScan_PolicyModesQuarantine(t *testing.T) {
	tests := []struct {
		name            string
		policy          *models.LicensePolicy
		wantQuarantined bool
		wantViolations  int
		wantAction      string
	}{
		{"no policy", nil, false, 0, ""},
		{"allow", policytest.Policy("allow", nil, []string{"GPL-3.0"}), false, 1, "allow"},
		{"warn", policytest.Policy("warn", nil, []string{"GPL-3.0"}), false, 1, "warn"},
		{"block, denied license", policytest.Policy("block", nil, []string{"GPL-3.0"}), true, 1, "block"},
		{"block, license outside the allow list", policytest.Policy("block", []string{"MIT"}, nil), true, 1, "block"},
		{"block, license allowed", policytest.Policy("block", []string{"GPL-3.0"}, nil), false, 0, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newScanPolicyDB()
			db.props["license"] = "GPL-3.0"

			if tt.policy != nil {
				db.policies = []*models.LicensePolicy{tt.policy}
			}

			out := runLicenseScan(t, newLicenseHandlerFor(t, db))

			if got, _ := out["quarantined"].(bool); got != tt.wantQuarantined {
				t.Errorf("quarantined = %v, want %v (%v)", out["quarantined"], tt.wantQuarantined, out)
			}

			violations, ok := out["policy_violations"].([]any)
			if !ok {
				t.Fatalf("policy_violations must always be an array, got %T", out["policy_violations"])
			}

			if len(violations) != tt.wantViolations {
				t.Fatalf("violations = %v, want %d", violations, tt.wantViolations)
			}

			if tt.wantViolations > 0 {
				if a := violations[0].(map[string]any)["action"]; a != tt.wantAction {
					t.Errorf("violation action = %v, want %s", a, tt.wantAction)
				}
			}

			if stored := db.licenses["leftpad@1.0.0"]; stored == nil || stored.Quarantined != tt.wantQuarantined {
				t.Errorf("stored result = %+v, want quarantined=%v", stored, tt.wantQuarantined)
			}
		})
	}
}

func TestLicenseQuarantine_ApproveAndRequarantine(t *testing.T) {
	db := newScanPolicyDB()
	db.props["license"] = "GPL-3.0"
	db.policies = []*models.LicensePolicy{policytest.Policy("block", nil, []string{"GPL-3.0"})}
	h := newLicenseHandlerFor(t, db)

	scan := runLicenseScan(t, h)
	if scan["quarantined"] != true {
		t.Fatalf("scan must quarantine: %v", scan)
	}

	path := "/api/v1/repositories/repo/license/scans/" + quarantineScanID + "/quarantine"

	// Approve.
	rw := serveAs(h.RegisterRoutes, http.MethodPost, path, `{"quarantined":false}`)
	if rw.Code != http.StatusOK {
		t.Fatalf("approve: status = %d (%s)", rw.Code, rw.Body.String())
	}

	var approved struct {
		Quarantined        bool `json:"quarantined"`
		QuarantineApproved bool `json:"quarantine_approved"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &approved); err != nil {
		t.Fatal(err)
	}

	if approved.Quarantined || !approved.QuarantineApproved {
		t.Errorf("approve response = %+v", approved)
	}

	// A re-scan of the same licenses must not undo the approval.
	if again := runLicenseScan(t, h); again["quarantined"] != false || again["quarantine_approved"] != true {
		t.Errorf("re-scan after approval = quarantined:%v approved:%v", again["quarantined"], again["quarantine_approved"])
	}

	// Quarantine again by hand.
	rw = serveAs(h.RegisterRoutes, http.MethodPost, path, `{"quarantined":true}`)
	if rw.Code != http.StatusOK || !db.licenses["leftpad@1.0.0"].Quarantined {
		t.Errorf("manual quarantine: status = %d, stored = %+v", rw.Code, db.licenses["leftpad@1.0.0"])
	}
}

func TestLicenseQuarantine_Rejections(t *testing.T) {
	tests := []struct {
		name      string
		principal *auth.Principal
		id        string
		body      string
		setErr    error
		want      int
	}{
		{"anonymous cannot approve", &auth.Principal{IsAnonymous: true}, quarantineScanID, `{"quarantined":false}`, nil, http.StatusForbidden},
		{"bad JSON", &auth.Principal{Username: "admin", IsLegacy: true}, quarantineScanID, `{`, nil, http.StatusBadRequest},
		{"id is not a UUID", &auth.Principal{Username: "admin", IsLegacy: true}, "nope", `{"quarantined":false}`, nil, http.StatusNotFound},
		{"unknown scan", &auth.Principal{Username: "admin", IsLegacy: true}, "77777777-7777-7777-7777-777777777777", `{"quarantined":false}`, nil, http.StatusNotFound},
		{"database failure is not leaked", &auth.Principal{Username: "admin", IsLegacy: true}, quarantineScanID, `{"quarantined":false}`, errScannerBroke, http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newScanPolicyDB()
			db.props["license"] = "GPL-3.0"
			db.policies = []*models.LicensePolicy{policytest.Policy("block", nil, []string{"GPL-3.0"})}
			h := newLicenseHandlerFor(t, db)
			runLicenseScan(t, h)

			db.setErr = tt.setErr

			rw := serveWithPrincipal(h.RegisterRoutes, tt.principal, http.MethodPost,
				"/api/v1/repositories/repo/license/scans/"+tt.id+"/quarantine", tt.body)
			if rw.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rw.Code, tt.want, rw.Body.String())
			}

			if strings.Contains(rw.Body.String(), "hunter2") {
				t.Errorf("response leaks the database error: %s", rw.Body.String())
			}

			if !db.licenses["leftpad@1.0.0"].Quarantined {
				t.Error("a rejected request must leave the artifact quarantined")
			}
		})
	}
}

func TestProperties_LicenseKeyQuarantines(t *testing.T) {
	tests := []struct {
		name            string
		method          string
		body            string
		policy          *models.LicensePolicy
		wantQuarantined bool
		wantScanned     bool
	}{
		{"PUT license under block policy", http.MethodPut, `{"properties":{"license":"GPL-3.0"}}`, policytest.Policy("block", nil, []string{"GPL-3.0"}), true, true},
		{"PATCH license under block policy", http.MethodPatch, `{"license":"GPL-3.0-only"}`, policytest.Policy("block", nil, []string{"GPL-3.0"}), true, true},
		{"PATCH license under warn policy", http.MethodPatch, `{"license":"GPL-3.0"}`, policytest.Policy("warn", nil, []string{"GPL-3.0"}), false, true},
		{"PATCH license under allow policy", http.MethodPatch, `{"license":"GPL-3.0"}`, policytest.Policy("allow", nil, []string{"GPL-3.0"}), false, true},
		{"PATCH harmless license under block policy", http.MethodPatch, `{"license":"MIT"}`, policytest.Policy("block", nil, []string{"GPL-3.0"}), false, true},
		{"PATCH unrelated property is not a license event", http.MethodPatch, `{"team":"blue"}`, policytest.Policy("block", nil, []string{"GPL-3.0"}), false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newScanPolicyDB()
			db.policies = []*models.LicensePolicy{tt.policy}
			log, _ := newBundlesScansEnv(t, &bundlesScansMockDB{})
			h := NewPropertiesHandler(log, db, nil)
			register := func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }

			rw := serveAs(register, tt.method, "/api/v1/repositories/repo/artifacts/leftpad@1.0.0/properties", tt.body)
			if rw.Code != http.StatusOK {
				t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
			}

			stored := db.licenses["leftpad@1.0.0"]
			if (stored != nil) != tt.wantScanned {
				t.Fatalf("license result stored = %v, want %v", stored != nil, tt.wantScanned)
			}

			if stored != nil && stored.Quarantined != tt.wantQuarantined {
				t.Errorf("quarantined = %v, want %v", stored.Quarantined, tt.wantQuarantined)
			}
		})
	}
}

func TestProperties_QuarantineSurvivesLicenseEdit(t *testing.T) {
	db := newScanPolicyDB()
	db.policies = []*models.LicensePolicy{policytest.Policy("block", nil, []string{"GPL-3.0"})}
	log, _ := newBundlesScansEnv(t, &bundlesScansMockDB{})
	h := NewPropertiesHandler(log, db, nil)
	register := func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }
	path := "/api/v1/repositories/repo/artifacts/leftpad@1.0.0/properties"

	if rw := serveAs(register, http.MethodPatch, path, `{"license":"GPL-3.0"}`); rw.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
	}

	// Rewriting the license to something harmless must not be a way out.
	if rw := serveAs(register, http.MethodPatch, path, `{"license":"MIT"}`); rw.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
	}

	if !db.licenses["leftpad@1.0.0"].Quarantined {
		t.Fatal("editing the license property released the quarantine; only an admin approval may do that")
	}
}

// ctxScanner blocks until the scan context ends, like a scanner process that
// is killed with it.
type ctxScanner struct {
	started chan struct{}
}

func (ctxScanner) Name() string { return "osv" }

func (s ctxScanner) Scan(ctx context.Context, _ vuln.Package) ([]models.Vulnerability, error) {
	close(s.started)
	<-ctx.Done()

	return nil, ctx.Err()
}

func TestVulnScan_ShutdownCancelsScanAndStoresFailure(t *testing.T) {
	db := newScanPolicyDB()
	started := make(chan struct{})
	h := newScanPolicyVulnHandler(t, db, map[string]vuln.ScannerBackend{"osv": ctxScanner{started: started}})

	tasks := bgtask.New(t.Context())
	h.WithTasks(tasks)

	register := func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }

	rw := serveAs(register, http.MethodPost, "/api/v1/repositories/repo/vuln/scan",
		`{"artifact_id":"left-pad@1.0.0","package_name":"left-pad","package_version":"1.0.0","ecosystem":"npm"}`)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
	}

	<-started

	// Buffered by the stub database: Shutdown must not wait on the test reading it.
	if !tasks.Shutdown(10 * time.Second) {
		t.Fatal("the running scan did not end with the server")
	}

	stored := waitVulnUpdate(t, db)
	if stored.ScanStatus != "failed" {
		t.Errorf("an interrupted scan must be stored as failed, got %q", stored.ScanStatus)
	}
}
