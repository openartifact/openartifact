// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/vuln"
)

const imageDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// imageDB serves one docker repository for the image scan, gate and import tests.
type imageDB struct {
	VulnStore
	auth.EvaluatorStore

	public      bool
	format      string
	targets     []*models.PermissionTarget
	policies    []*models.LicensePolicy
	quarantined bool

	mu    sync.Mutex
	scans []*models.VulnScanResult
}

func (db *imageDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if name == "missing" {
		return nil, nil
	}

	format := db.format
	if format == "" {
		format = "docker"
	}

	return &models.Repository{ID: testRepoID, Name: name, Format: format, Public: db.public}, nil
}

func (db *imageDB) GetPermissionTargetsForPrincipal(context.Context, string, []string) ([]*models.PermissionTarget, error) {
	return db.targets, nil
}

func (db *imageDB) ListProjectGrantsByUser(context.Context, string) ([]*models.ProjectGrant, error) {
	return nil, nil
}

func (db *imageDB) GetVulnScanResult(_ context.Context, _, artifactID string) (*models.VulnScanResult, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	for _, s := range slices.Backward(db.scans) {
		if s.ArtifactID == artifactID {
			return s, nil
		}
	}

	return nil, nil
}

func (db *imageDB) GetVulnScanResultByID(_ context.Context, _, id string) (*models.VulnScanResult, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	for _, s := range db.scans {
		if s.ID == id {
			return s, nil
		}
	}

	return nil, nil
}

func (db *imageDB) CreateVulnScanResult(_ context.Context, s *models.VulnScanResult) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	s.ID = "00000000-0000-4000-8000-00000000000" + string(rune('0'+len(db.scans)+1))
	db.scans = append(db.scans, s)

	return nil
}

func (db *imageDB) UpdateVulnScanResult(_ context.Context, s *models.VulnScanResult) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	for i, old := range db.scans {
		if old.ID == s.ID {
			stored := *s
			db.scans[i] = &stored
		}
	}

	return nil
}

func (db *imageDB) IsArtifactQuarantined(context.Context, string, []string) (bool, error) {
	return db.quarantined, nil
}

func (db *imageDB) ListLicensePolicies(context.Context, string) ([]*models.LicensePolicy, error) {
	return db.policies, nil
}

func (db *imageDB) lastScan() *models.VulnScanResult {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.scans[len(db.scans)-1]
}

// fakeImages serves the components of one image.
type fakeImages struct {
	comps []sbom.Component
}

func (f fakeImages) HasSBOM(context.Context, string, string) (bool, error) {
	return f.comps != nil, nil
}

func (f fakeImages) Components(context.Context, string, string) ([]sbom.Component, error) {
	return f.comps, nil
}

type fakeAttestations struct {
	sum AttestationSummary
}

func (f fakeAttestations) AttestationSummary(context.Context, string, string) (AttestationSummary, error) {
	return f.sum, nil
}

// pkgBackend finds CVE-1 (HIGH) in lodash only.
type pkgBackend struct{}

func (pkgBackend) Name() string { return "osv" }

func (pkgBackend) Scan(_ context.Context, pkg vuln.Package) ([]models.Vulnerability, error) {
	if pkg.Name == "lodash" {
		return []models.Vulnerability{{ID: "CVE-1", Severity: "HIGH"}}, nil
	}

	return nil, nil
}

func newImageHandler(t *testing.T, db *imageDB) *VulnHandler {
	t.Helper()

	log, _ := newBundlesScansEnv(t, &bundlesScansMockDB{})

	h := NewVulnHandler(db, log, auth.NewEvaluator(db, log, false), nil)
	h.newBackend = func(string) vuln.ScannerBackend { return pkgBackend{} }

	return h
}

func serveImage(h *VulnHandler, p *auth.Principal, method, target, body string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p != nil {
				r = r.WithContext(middleware.WithPrincipal(r.Context(), p))
			}

			next.ServeHTTP(w, r)
		})
	})
	h.RegisterRoutes(router, "/api/v1/repositories/{name}")

	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

func tokenWith(actions ...string) (*auth.Principal, []*models.PermissionTarget) {
	uid := "11111111-1111-4111-8111-111111111111"

	return &auth.Principal{Username: "ci", UserID: &uid}, []*models.PermissionTarget{
		{ID: "t1", Actions: actions, RepositoryFilter: "*", PathPattern: "*"},
	}
}

func completedScan(id string, vulns ...models.Vulnerability) *models.VulnScanResult {
	now := time.Now()

	return &models.VulnScanResult{
		ID: id, RepositoryID: testRepoID, ArtifactID: imageDigest, Ecosystem: "docker", Scanner: "osv", ScanStatus: "completed",
		Vulnerabilities: vulns, VulnCount: len(vulns), FailedScanners: []string{}, ScannedAt: &now,
	}
}

const (
	gateURL     = "/api/v1/repositories/img/gate?digest=" + imageDigest
	scanGateURL = "/api/v1/repositories/img/vuln/scans/00000000-0000-4000-8000-000000000001/gate?min_priority=high"
	importURL   = "/api/v1/repositories/img/vuln/scans/import"
)

func decodeImageBody(t *testing.T, rw *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var out map[string]any
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q: %v", rw.Body.String(), err)
	}

	return out
}

func checkResults(t *testing.T, body map[string]any) map[string]bool {
	t.Helper()

	out := map[string]bool{}

	checks, _ := body["checks"].([]any)
	for _, c := range checks {
		m := c.(map[string]any)
		out[m["name"].(string)] = m["pass"].(bool)
	}

	return out
}

func TestDigestGateAggregation(t *testing.T) {
	blockGPL := &models.LicensePolicy{ID: "p1", Name: "no-gpl", Action: "block", Enabled: true, DeniedLicenses: []string{"GPL-3.0-only"}}
	gplImage := fakeImages{comps: []sbom.Component{{Name: "x", Licenses: []string{"GPL-3.0-only"}}}}
	mitImage := fakeImages{comps: []sbom.Component{{Name: "x", Licenses: []string{"MIT"}}}}

	tests := []struct {
		name        string
		scans       []*models.VulnScanResult
		quarantined bool
		policies    []*models.LicensePolicy
		images      ImageSBOMs
		att         *AttestationSummary
		query       string
		wantPass    bool
		wantStatus  string
		wantFailing string
		wantChecks  []string
	}{
		{name: "no scan fails with a reason", wantStatus: "none", wantFailing: "vulnerabilities",
			wantChecks: []string{"vulnerabilities", "license", "quarantine"}},
		{name: "clean scan passes", scans: []*models.VulnScanResult{completedScan("00000000-0000-4000-8000-000000000001")},
			wantPass: true, wantStatus: "completed"},
		{name: "a high finding fails the default criteria", scans: []*models.VulnScanResult{
			completedScan("00000000-0000-4000-8000-000000000001", models.Vulnerability{ID: "CVE-1", Severity: "HIGH"}),
		}, wantStatus: "completed", wantFailing: "vulnerabilities"},
		{name: "a low finding passes the default criteria", scans: []*models.VulnScanResult{
			completedScan("00000000-0000-4000-8000-000000000001", models.Vulnerability{ID: "CVE-1", Severity: "LOW"}),
		}, wantPass: true, wantStatus: "completed"},
		{name: "explicit criteria replace the default", scans: []*models.VulnScanResult{
			completedScan("00000000-0000-4000-8000-000000000001", models.Vulnerability{ID: "CVE-1", Severity: "LOW"}),
		}, query: "&min_priority=low", wantStatus: "completed", wantFailing: "vulnerabilities"},
		{name: "the latest scan decides", scans: []*models.VulnScanResult{
			completedScan("00000000-0000-4000-8000-000000000001", models.Vulnerability{ID: "CVE-1", Severity: "CRITICAL"}),
			completedScan("00000000-0000-4000-8000-000000000002"),
		}, wantPass: true, wantStatus: "completed"},
		{name: "a scan in progress is no pass", scans: []*models.VulnScanResult{
			{ID: "00000000-0000-4000-8000-000000000001", ArtifactID: imageDigest, ScanStatus: "scanning"},
		}, wantStatus: "scanning", wantFailing: "vulnerabilities"},
		{name: "quarantine fails the image", scans: []*models.VulnScanResult{completedScan("00000000-0000-4000-8000-000000000001")},
			quarantined: true, wantStatus: "completed", wantFailing: "quarantine"},
		{
			name:        "a blocking license in the SBOM fails",
			scans:       []*models.VulnScanResult{completedScan("00000000-0000-4000-8000-000000000001")},
			policies:    []*models.LicensePolicy{blockGPL},
			images:      gplImage,
			wantStatus:  "completed",
			wantFailing: "license",
		},
		{
			name:       "a compliant SBOM passes the license check",
			scans:      []*models.VulnScanResult{completedScan("00000000-0000-4000-8000-000000000001")},
			policies:   []*models.LicensePolicy{blockGPL},
			images:     mitImage,
			wantPass:   true,
			wantStatus: "completed",
		},
		{
			name:        "a blocking policy without an SBOM fails",
			scans:       []*models.VulnScanResult{completedScan("00000000-0000-4000-8000-000000000001")},
			policies:    []*models.LicensePolicy{blockGPL},
			wantStatus:  "completed",
			wantFailing: "license",
		},
		{name: "no attestations fails when the lookup is wired", scans: []*models.VulnScanResult{
			completedScan("00000000-0000-4000-8000-000000000001"),
		}, att: &AttestationSummary{}, wantStatus: "completed", wantFailing: "attestations",
			wantChecks: []string{"vulnerabilities", "license", "quarantine", "attestations"}},
		{name: "a failed verification fails", scans: []*models.VulnScanResult{completedScan("00000000-0000-4000-8000-000000000001")},
			att: &AttestationSummary{Total: 2, Verified: 1, Failed: 1}, wantStatus: "completed", wantFailing: "attestations"},
		{name: "verified attestations pass", scans: []*models.VulnScanResult{completedScan("00000000-0000-4000-8000-000000000001")},
			att: &AttestationSummary{Total: 2, Verified: 2}, wantPass: true, wantStatus: "completed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &imageDB{scans: tt.scans, quarantined: tt.quarantined, policies: tt.policies}
			h := newImageHandler(t, db)

			if tt.images != nil {
				h.WithImages(tt.images)
			}

			if tt.att != nil {
				h.WithAttestations(fakeAttestations{sum: *tt.att})
			}

			rw := serveImage(h, adminPrincipal(), http.MethodGet, gateURL+tt.query, "")
			if rw.Code != http.StatusOK {
				t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
			}

			body := decodeImageBody(t, rw)
			if body["pass"] != tt.wantPass || body["scan_status"] != tt.wantStatus {
				t.Fatalf("pass/scan_status = %v/%v, want %v/%s: %s", body["pass"], body["scan_status"], tt.wantPass, tt.wantStatus, rw.Body)
			}

			results := checkResults(t, body)
			for name, pass := range results {
				if pass != (name != tt.wantFailing) {
					t.Errorf("check %s pass = %v (failing = %q)", name, pass, tt.wantFailing)
				}
			}

			if tt.wantFailing != "" && !strings.HasPrefix(body["reason"].(string), tt.wantFailing+": ") {
				t.Errorf("reason = %v", body["reason"])
			}

			for _, name := range tt.wantChecks {
				if _, ok := results[name]; !ok {
					t.Errorf("check %s missing from %v", name, results)
				}
			}
		})
	}
}

func TestDigestGateRejections(t *testing.T) {
	tests := []struct {
		name   string
		target string
		format string
		want   int
	}{
		{"no digest", "/api/v1/repositories/img/gate", "", http.StatusBadRequest},
		{"bad digest", "/api/v1/repositories/img/gate?digest=latest", "", http.StatusBadRequest},
		{"not a docker repository", gateURL, "npm", http.StatusBadRequest},
		{"unknown repository", "/api/v1/repositories/missing/gate?digest=" + imageDigest, "", http.StatusNotFound},
		{"bad criteria", gateURL + "&epss_min=7", "", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newImageHandler(t, &imageDB{format: tt.format})

			if rw := serveImage(h, adminPrincipal(), http.MethodGet, tt.target, ""); rw.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rw.Code, tt.want, rw.Body)
			}
		})
	}
}

func TestGateReadAccess(t *testing.T) {
	reader, readTargets := tokenWith("read")
	writer, writeTargets := tokenWith("write")
	scan := completedScan("00000000-0000-4000-8000-000000000001")

	tests := []struct {
		name      string
		principal *auth.Principal
		targets   []*models.PermissionTarget
		public    bool
		want      int
	}{
		{"read token", reader, readTargets, false, http.StatusOK},
		{"token without read", writer, writeTargets, false, http.StatusForbidden},
		{"anonymous on a private repository", &auth.Principal{IsAnonymous: true}, nil, false, http.StatusForbidden},
		{"anonymous on a public repository", &auth.Principal{IsAnonymous: true}, nil, true, http.StatusOK},
		{"no principal", nil, nil, true, http.StatusForbidden},
	}

	for _, tt := range tests {
		for _, target := range []string{gateURL, scanGateURL} {
			t.Run(tt.name+" "+target, func(t *testing.T) {
				h := newImageHandler(t, &imageDB{targets: tt.targets, public: tt.public, scans: []*models.VulnScanResult{scan}})

				if rw := serveImage(h, tt.principal, http.MethodGet, target, ""); rw.Code != tt.want {
					t.Fatalf("status = %d, want %d (%s)", rw.Code, tt.want, rw.Body)
				}
			})
		}
	}
}

func TestScanGateCarriesChecks(t *testing.T) {
	db := &imageDB{scans: []*models.VulnScanResult{completedScan("00000000-0000-4000-8000-000000000001")}}

	rw := serveImage(newImageHandler(t, db), adminPrincipal(), http.MethodGet, scanGateURL, "")
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rw.Code, rw.Body)
	}

	body := decodeImageBody(t, rw)
	if results := checkResults(t, body); len(results) != 1 || !results["vulnerabilities"] || body["pass"] != true {
		t.Errorf("body = %v", body)
	}
}

const sarifBody = `{"version":"2.1.0","runs":[{"tool":{"driver":{"rules":[{"id":"CVE-2024-1","properties":{"security-severity":"9.1"}}]}},` +
	`"results":[{"ruleId":"CVE-2024-1","level":"error","message":{"text":"m"}}]}]}`

func scanImportBody(format, digest, result string) string {
	return `{"format":"` + format + `","digest":"` + digest + `","result":` + result + `}`
}

func TestImportScan(t *testing.T) {
	writer, writeTargets := tokenWith("write")
	reader, readTargets := tokenWith("read")

	t.Run("a SARIF scan is stored as a completed external scan and gates", func(t *testing.T) {
		db := &imageDB{targets: writeTargets}
		h := newImageHandler(t, db)

		rw := serveImage(h, writer, http.MethodPost, importURL, scanImportBody("sarif", imageDigest, sarifBody))
		if rw.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rw.Code, rw.Body)
		}

		scan := db.lastScan()
		if scan.Scanner != "external:sarif" || scan.ScanStatus != "completed" || scan.ArtifactID != imageDigest ||
			scan.CriticalCount != 1 || scan.VulnCount != 1 {
			t.Errorf("stored scan = %+v", scan)
		}

		gate := serveImage(h, adminPrincipal(), http.MethodGet, gateURL, "")
		if body := decodeImageBody(t, gate); body["pass"] != false || body["scan_status"] != "completed" {
			t.Errorf("gate after import = %v", body)
		}
	})

	t.Run("the scanner label names the source", func(t *testing.T) {
		db := &imageDB{targets: writeTargets}
		body := `{"format":"oa","digest":"` + imageDigest + `","scanner":"clair","result":{"vulnerabilities":[]}}`

		if rw := serveImage(newImageHandler(t, db), writer, http.MethodPost, importURL, body); rw.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rw.Code, rw.Body)
		}

		if got := db.lastScan().Scanner; got != "external:clair" {
			t.Errorf("scanner = %q", got)
		}
	})

	rejections := []struct {
		name      string
		principal *auth.Principal
		targets   []*models.PermissionTarget
		target    string
		body      string
		format    string
		want      int
	}{
		{
			"read access is not enough",
			reader,
			readTargets,
			importURL,
			scanImportBody("sarif", imageDigest, sarifBody),
			"",
			http.StatusForbidden,
		},
		{
			"anonymous",
			&auth.Principal{IsAnonymous: true},
			nil,
			importURL,
			scanImportBody("sarif", imageDigest, sarifBody),
			"",
			http.StatusForbidden,
		},
		{"unknown repository", writer, writeTargets, "/api/v1/repositories/missing/vuln/scans/import",
			scanImportBody("sarif", imageDigest, sarifBody), "", http.StatusNotFound},
		{
			"not a docker repository",
			writer,
			writeTargets,
			importURL,
			scanImportBody("sarif", imageDigest, sarifBody),
			"npm",
			http.StatusBadRequest,
		},
		{"unknown format", writer, writeTargets, importURL, scanImportBody("xml", imageDigest, `{}`), "", http.StatusUnprocessableEntity},
		{"bad digest", writer, writeTargets, importURL, scanImportBody("sarif", "latest", sarifBody), "", http.StatusUnprocessableEntity},
		{
			"unreadable result",
			writer,
			writeTargets,
			importURL,
			scanImportBody("sarif", imageDigest, `{"version":"1.0"}`),
			"",
			http.StatusUnprocessableEntity,
		},
		{"bad label", writer, writeTargets, importURL,
			`{"format":"oa","digest":"` + imageDigest + `","scanner":"Bad Label","result":{}}`, "", http.StatusUnprocessableEntity},
		{
			"no result",
			writer,
			writeTargets,
			importURL,
			`{"format":"oa","digest":"` + imageDigest + `"}`,
			"",
			http.StatusUnprocessableEntity,
		},
		{"not JSON", writer, writeTargets, importURL, `nope`, "", http.StatusBadRequest},
	}

	for _, tt := range rejections {
		t.Run(tt.name, func(t *testing.T) {
			db := &imageDB{targets: tt.targets, format: tt.format}

			rw := serveImage(newImageHandler(t, db), tt.principal, http.MethodPost, tt.target, tt.body)
			if rw.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rw.Code, tt.want, rw.Body)
			}

			if len(db.scans) != 0 {
				t.Errorf("a rejected import stored %d scan(s)", len(db.scans))
			}
		})
	}
}

func TestImageScanFromIngestedComponents(t *testing.T) {
	const scanURL = "/api/v1/repositories/img/vuln/scan"

	body := func(artifact string) string {
		return `{"ecosystem":"docker","artifact_id":"` + artifact + `","scanner":"osv"}`
	}

	t.Run("scans every covered package of the SBOM", func(t *testing.T) {
		db := &imageDB{}
		h := newImageHandler(t, db).WithImages(fakeImages{comps: []sbom.Component{
			{Name: "lodash", PURL: "pkg:npm/lodash@4.17.20"},
			{Name: "express", PURL: "pkg:npm/express@4.0.0"},
			{Name: "musl", PURL: "pkg:apk/alpine/musl@1.2.3"},
		}})

		rw := serveImage(h, adminPrincipal(), http.MethodPost, scanURL, body(imageDigest))
		if rw.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rw.Code, rw.Body)
		}

		if !h.tasks.Shutdown(10 * time.Second) {
			t.Fatal("scan did not finish")
		}

		scan := db.lastScan()
		if scan.ScanStatus != "completed" || scan.VulnCount != 1 || scan.HighCount != 1 || scan.ArtifactID != imageDigest ||
			scan.Ecosystem != "docker" {
			t.Fatalf("scan = %+v", scan)
		}

		if scan.Vulnerabilities[0].Package != "lodash@4.17.20" || !strings.Contains(scan.ScanError, "1 apk") {
			t.Errorf("finding package = %q, scan error = %q", scan.Vulnerabilities[0].Package, scan.ScanError)
		}
	})

	t.Run("an SBOM without a covered package is a failed scan, never a clean one", func(t *testing.T) {
		db := &imageDB{}
		h := newImageHandler(t, db).WithImages(fakeImages{comps: []sbom.Component{{Name: "musl", PURL: "pkg:apk/alpine/musl@1.2.3"}}})

		if rw := serveImage(h, adminPrincipal(), http.MethodPost, scanURL, body(imageDigest)); rw.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rw.Code, rw.Body)
		}

		h.tasks.Shutdown(10 * time.Second)

		if scan := db.lastScan(); scan.ScanStatus != "failed" || scan.ScanError == "" {
			t.Errorf("scan = %+v", scan)
		}
	})

	rejections := []struct {
		name     string
		artifact string
		images   ImageSBOMs
		format   string
		want     int
	}{
		{"no SBOM", imageDigest, fakeImages{}, "", http.StatusUnprocessableEntity},
		{"images not wired", imageDigest, nil, "", http.StatusUnprocessableEntity},
		{"artifact is not a digest", "img:latest", fakeImages{comps: []sbom.Component{{}}}, "", http.StatusBadRequest},
		{"not a docker repository", imageDigest, fakeImages{comps: []sbom.Component{{}}}, "npm", http.StatusBadRequest},
	}

	for _, tt := range rejections {
		t.Run(tt.name, func(t *testing.T) {
			db := &imageDB{format: tt.format}
			h := newImageHandler(t, db)

			if tt.images != nil {
				h.WithImages(tt.images)
			}

			if rw := serveImage(h, adminPrincipal(), http.MethodPost, scanURL, body(tt.artifact)); rw.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rw.Code, tt.want, rw.Body)
			}

			if len(db.scans) != 0 {
				t.Errorf("a refused scan stored a row")
			}
		})
	}
}
