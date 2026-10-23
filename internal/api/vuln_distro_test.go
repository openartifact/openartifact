// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/vuln"
)

const (
	distroDebID     = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	distroRPMID     = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	distroDistID    = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	distroOtherRepo = "99999999-9999-9999-9999-999999999999"
)

var errDistroLookup = errors.New("connection refused (password=hunter2)")

// distroScanDB adds the deb and rpm lookups the scan handler makes to the scan
// handler test database, and records them.
type distroScanDB struct {
	*scanPolicyDB

	debPackages map[string]*models.DebPackage
	distros     []models.DebianDistribution
	components  map[string]*models.Component
	err         error

	// lookups is only touched by the request goroutine.
	lookups []string
}

func (db *distroScanDB) GetDebPackageByID(_ context.Context, id string) (*models.DebPackage, error) {
	db.lookups = append(db.lookups, "deb package")

	if db.err != nil {
		return nil, db.err
	}

	return db.debPackages[id], nil
}

func (db *distroScanDB) GetDebianDistributionsByRepo(context.Context, string) ([]models.DebianDistribution, error) {
	db.lookups = append(db.lookups, "deb distributions")

	return db.distros, db.err
}

func (db *distroScanDB) GetComponentByID(_ context.Context, id string) (*models.Component, error) {
	db.lookups = append(db.lookups, "rpm component")

	if db.err != nil {
		return nil, db.err
	}

	return db.components[id], nil
}

// scanRecorder records the package each scanner was asked to scan.
type scanRecorder struct {
	mu    sync.Mutex
	calls map[string]vuln.Package
}

func (r *scanRecorder) backend(name string) vuln.ScannerBackend {
	return recordingBackend{name: name, rec: r}
}

func (r *scanRecorder) ran() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	names := make([]string, 0, len(r.calls))
	for name := range r.calls {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}

type recordingBackend struct {
	name string
	rec  *scanRecorder
}

func (b recordingBackend) Name() string { return b.name }

func (b recordingBackend) Scan(_ context.Context, pkg vuln.Package) ([]models.Vulnerability, error) {
	b.rec.mu.Lock()
	defer b.rec.mu.Unlock()

	b.rec.calls[b.name] = pkg

	return nil, nil
}

func newDistroScanDB() *distroScanDB {
	return &distroScanDB{
		scanPolicyDB: newScanPolicyDB(),
		debPackages: map[string]*models.DebPackage{
			distroDebID: {ID: distroDebID, RepositoryID: testRepoID, DistributionID: distroDistID, Name: "openssl", Version: "3.0.11-1"},
		},
		distros: []models.DebianDistribution{
			{ID: "dddddddd-dddd-dddd-dddd-dddddddddddd", Name: "stable"},
			{ID: distroDistID, Name: "bookworm"},
		},
		components: map[string]*models.Component{
			distroRPMID: {ID: distroRPMID, RepositoryID: testRepoID, Format: "rpm", Name: "openssl", Version: "3.0.7", Release: "24.el9", Epoch: 1},
		},
	}
}

func scanBody(artifactID, name, version, ecosystem, scanners string) string {
	return `{"artifact_id":"` + artifactID + `","package_name":"` + name + `","package_version":"` + version +
		`","ecosystem":"` + ecosystem + `","scanners":` + scanners + `}`
}

// A deb or rpm is scanned by grype with the distribution it is found to belong
// to; when none is found grype is replaced by osv, and the scanner field of the
// scan says which scanner ran. Nothing is recorded as unsupported.
func TestVulnScan_DebRPMDistro(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(db *distroScanDB)
		body        string
		wantScanner string
		wantRan     []string
		wantPkg     vuln.Package // as the scanner that ran first alphabetically received it
		wantLookups []string
	}{
		{
			name:        "deb in a release-named distribution",
			body:        scanBody(distroDebID, "openssl", "3.0.11-1", "deb", `["grype"]`),
			wantScanner: "grype", wantRan: []string{"grype"},
			wantPkg:     vuln.Package{Name: "openssl", Version: "3.0.11-1", Ecosystem: "deb", Distro: "debian-12"},
			wantLookups: []string{"deb package", "deb distributions"},
		},
		{
			name:        "ubuntu codename distribution",
			mutate:      func(db *distroScanDB) { db.distros[1].Name = "jammy-security" },
			body:        scanBody(distroDebID, "openssl", "3.0.11-1", "deb", `["grype"]`),
			wantScanner: "grype", wantRan: []string{"grype"},
			wantPkg:     vuln.Package{Name: "openssl", Version: "3.0.11-1", Ecosystem: "deb", Distro: "ubuntu-22.04"},
			wantLookups: []string{"deb package", "deb distributions"},
		},
		{
			name: "distribution name unknown, version carries the release",
			mutate: func(db *distroScanDB) {
				db.debPackages[distroDebID].Version = "3.0.11-1~deb12u2"
				db.debPackages[distroDebID].DistributionID = db.distros[0].ID
			},
			body:        scanBody(distroDebID, "openssl", "3.0.11-1~deb12u2", "deb", `["grype"]`),
			wantScanner: "grype", wantRan: []string{"grype"},
			wantPkg:     vuln.Package{Name: "openssl", Version: "3.0.11-1~deb12u2", Ecosystem: "deb", Distro: "debian-12"},
			wantLookups: []string{"deb package", "deb distributions"},
		},
		{
			name:        "deb in an operator-named distribution goes to osv",
			mutate:      func(db *distroScanDB) { db.debPackages[distroDebID].DistributionID = db.distros[0].ID },
			body:        scanBody(distroDebID, "openssl", "3.0.11-1", "deb", `["grype"]`),
			wantScanner: "osv", wantRan: []string{"osv"},
			wantPkg:     vuln.Package{Name: "openssl", Version: "3.0.11-1", Ecosystem: "deb"},
			wantLookups: []string{"deb package", "deb distributions"},
		},
		{
			name:        "grype and osv requested, distribution unknown: one osv scan",
			mutate:      func(db *distroScanDB) { db.debPackages[distroDebID].DistributionID = db.distros[0].ID },
			body:        scanBody(distroDebID, "openssl", "3.0.11-1", "deb", `["grype","osv"]`),
			wantScanner: "osv", wantRan: []string{"osv"},
			wantPkg:     vuln.Package{Name: "openssl", Version: "3.0.11-1", Ecosystem: "deb"},
			wantLookups: []string{"deb package", "deb distributions"},
		},
		{
			name:        "free-form artifact id: version tag still decides, no lookup",
			body:        scanBody("openssl_3.0.11-1~deb11u1_amd64.deb", "openssl", "3.0.11-1~deb11u1", "deb", `["grype"]`),
			wantScanner: "grype", wantRan: []string{"grype"},
			wantPkg: vuln.Package{Name: "openssl", Version: "3.0.11-1~deb11u1", Ecosystem: "deb", Distro: "debian-11"},
		},
		{
			name:        "free-form artifact id and no release tag goes to osv without a lookup",
			body:        scanBody("openssl_3.0.11-1_amd64.deb", "openssl", "3.0.11-1", "deb", `["grype"]`),
			wantScanner: "osv", wantRan: []string{"osv"},
			wantPkg: vuln.Package{Name: "openssl", Version: "3.0.11-1", Ecosystem: "deb"},
		},
		{
			name:        "stored package with another name is not trusted",
			body:        scanBody(distroDebID, "curl", "3.0.11-1", "deb", `["grype"]`),
			wantScanner: "osv", wantRan: []string{"osv"},
			wantPkg:     vuln.Package{Name: "curl", Version: "3.0.11-1", Ecosystem: "deb"},
			wantLookups: []string{"deb package"},
		},
		{
			name:        "stored package of another repository is not trusted",
			mutate:      func(db *distroScanDB) { db.debPackages[distroDebID].RepositoryID = distroOtherRepo },
			body:        scanBody(distroDebID, "openssl", "3.0.11-1", "deb", `["grype"]`),
			wantScanner: "osv", wantRan: []string{"osv"},
			wantPkg:     vuln.Package{Name: "openssl", Version: "3.0.11-1", Ecosystem: "deb"},
			wantLookups: []string{"deb package"},
		},
		{
			name:        "unknown stored package id goes to osv",
			body:        scanBody("dddddddd-0000-0000-0000-000000000000", "openssl", "3.0.11-1", "deb", `["grype"]`),
			wantScanner: "osv", wantRan: []string{"osv"},
			wantPkg:     vuln.Package{Name: "openssl", Version: "3.0.11-1", Ecosystem: "deb"},
			wantLookups: []string{"deb package"},
		},
		{
			name:        "rpm takes epoch and release from the stored row",
			body:        scanBody(distroRPMID, "openssl", "3.0.7", "rpm", `["grype"]`),
			wantScanner: "grype", wantRan: []string{"grype"},
			wantPkg:     vuln.Package{Name: "openssl", Version: "3.0.7", Release: "24.el9", Epoch: 1, Ecosystem: "rpm", Distro: "rhel-9"},
			wantLookups: []string{"rpm component"},
		},
		{
			name:        "fedora rpm",
			mutate:      func(db *distroScanDB) { db.components[distroRPMID].Release = "3.fc39" },
			body:        scanBody(distroRPMID, "openssl", "3.0.7", "rpm", `["grype"]`),
			wantScanner: "grype", wantRan: []string{"grype"},
			wantPkg:     vuln.Package{Name: "openssl", Version: "3.0.7", Release: "3.fc39", Epoch: 1, Ecosystem: "rpm", Distro: "fedora-39"},
			wantLookups: []string{"rpm component"},
		},
		{
			name:        "rpm release without a distribution tag goes to osv",
			mutate:      func(db *distroScanDB) { db.components[distroRPMID].Release = "1" },
			body:        scanBody(distroRPMID, "openssl", "3.0.7", "rpm", `["grype","trivy"]`),
			wantScanner: "osv", wantRan: []string{"osv"},
			wantPkg:     vuln.Package{Name: "openssl", Version: "3.0.7", Release: "1", Epoch: 1, Ecosystem: "rpm"},
			wantLookups: []string{"rpm component"},
		},
		{
			name:        "component that is not an rpm is not trusted",
			mutate:      func(db *distroScanDB) { db.components[distroRPMID].Format = "deb" },
			body:        scanBody(distroRPMID, "openssl", "3.0.7", "rpm", `["grype"]`),
			wantScanner: "osv", wantRan: []string{"osv"},
			wantPkg:     vuln.Package{Name: "openssl", Version: "3.0.7", Ecosystem: "rpm"},
			wantLookups: []string{"rpm component"},
		},
		{
			name:        "free-form artifact id with a full rpm version",
			body:        scanBody("openssl-3.0.7-24.el9.x86_64.rpm", "openssl", "1:3.0.7-24.el9", "rpm", `["grype"]`),
			wantScanner: "grype", wantRan: []string{"grype"},
			wantPkg: vuln.Package{Name: "openssl", Version: "1:3.0.7-24.el9", Ecosystem: "rpm", Distro: "rhel-9"},
		},
		{
			name:        "osv alone never needs a distribution",
			body:        scanBody(distroDebID, "openssl", "3.0.11-1", "deb", `["osv"]`),
			wantScanner: "osv", wantRan: []string{"osv"},
			wantPkg: vuln.Package{Name: "openssl", Version: "3.0.11-1", Ecosystem: "deb"},
		},
		{
			name:        "other ecosystems are not looked up",
			body:        scanBody(distroDebID, "left-pad", "1.0.0", "npm", `["grype"]`),
			wantScanner: "grype", wantRan: []string{"grype"},
			wantPkg: vuln.Package{Name: "left-pad", Version: "1.0.0", Ecosystem: "npm"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newDistroScanDB()
			if tt.mutate != nil {
				tt.mutate(db)
			}

			rec := &scanRecorder{calls: map[string]vuln.Package{}}
			backends := map[string]vuln.ScannerBackend{}

			for _, n := range []string{"osv", "trivy", "grype"} {
				backends[n] = rec.backend(n)
			}

			h := newScanPolicyVulnHandler(t, db.scanPolicyDB, backends)
			h.db = db

			register := func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }

			rw := serveAs(register, http.MethodPost, "/api/v1/repositories/repo/vuln/scan", tt.body)
			if rw.Code != http.StatusOK {
				t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
			}

			var started struct {
				Scanner string `json:"scanner"`
			}
			if err := json.Unmarshal(rw.Body.Bytes(), &started); err != nil {
				t.Fatal(err)
			}

			if started.Scanner != tt.wantScanner {
				t.Errorf("response scanner = %q, want %q", started.Scanner, tt.wantScanner)
			}

			stored := waitVulnUpdate(t, db.scanPolicyDB)

			// The scan completed: it was neither refused nor recorded as unsupported.
			if stored.ScanStatus != "completed" || stored.ScanError != "" || len(stored.FailedScanners) != 0 {
				t.Errorf("stored status=%q error=%q failed=%v, want a completed scan", stored.ScanStatus, stored.ScanError, stored.FailedScanners)
			}

			if stored.Scanner != tt.wantScanner {
				t.Errorf("stored scanner = %q, want %q", stored.Scanner, tt.wantScanner)
			}

			// What the caller sent is what is stored; the distribution is only
			// used for the scan.
			if len(db.created) != 1 || db.created[0].PackageVersion != jsonField(t, tt.body, "package_version") {
				t.Errorf("stored version differs from the request: %+v", db.created)
			}

			if got := rec.ran(); !slices.Equal(got, tt.wantRan) {
				t.Errorf("scanners that ran = %v, want %v", got, tt.wantRan)
			}

			if got := rec.calls[tt.wantRan[0]]; got != tt.wantPkg {
				t.Errorf("%s scanned %+v, want %+v", tt.wantRan[0], got, tt.wantPkg)
			}

			if !slices.Equal(db.lookups, tt.wantLookups) {
				t.Errorf("database lookups = %v, want %v", db.lookups, tt.wantLookups)
			}
		})
	}
}

// A lookup failure is a server error, not a scan that quietly falls back and
// not a row stuck in "scanning"; the error text stays in the log.
func TestVulnScan_DistroLookupFailure(t *testing.T) {
	for _, tt := range []struct {
		name, id, ecosystem string
	}{
		{"deb", distroDebID, "deb"},
		{"rpm", distroRPMID, "rpm"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := newDistroScanDB()
			db.err = errDistroLookup

			rec := &scanRecorder{calls: map[string]vuln.Package{}}
			h := newScanPolicyVulnHandler(t, db.scanPolicyDB, map[string]vuln.ScannerBackend{"grype": rec.backend("grype")})
			h.db = db

			register := func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }

			rw := serveAs(register, http.MethodPost, "/api/v1/repositories/repo/vuln/scan",
				scanBody(tt.id, "openssl", "3.0.7", tt.ecosystem, `["grype"]`))

			if rw.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d (%s), want 500", rw.Code, rw.Body.String())
			}

			if strings.Contains(rw.Body.String(), "hunter2") || strings.Contains(rw.Body.String(), "connection refused") {
				t.Errorf("response leaks the database error: %s", rw.Body.String())
			}

			if len(db.created) != 0 || len(rec.ran()) != 0 {
				t.Errorf("a failed lookup created %d scans and ran %v", len(db.created), rec.ran())
			}
		})
	}
}

func jsonField(t *testing.T, body, key string) string {
	t.Helper()

	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}

	s, _ := m[key].(string)

	return s
}
