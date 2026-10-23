// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/licensepolicy"
)

const scanPolicyOrgID = "00000000-0000-0000-0000-000000000001"

func scanPolicyRepo(t *testing.T, pg *database.PostgresDB, name string) *models.Repository {
	t.Helper()

	orgID := scanPolicyOrgID
	repo := &models.Repository{Name: name, Format: "npm", RepoType: "hosted", ProjectID: &orgID}

	if err := pg.CreateRepository(context.Background(), repo); err != nil {
		t.Fatalf("CreateRepository(%s): %v", name, err)
	}

	return repo
}

func scanPolicyBlock(t *testing.T, pg *database.PostgresDB, repoID, action, denied string) {
	t.Helper()

	// AllowedLicenses stays nil on purpose: a policy that only lists denied
	// licenses (the usual request body) must be accepted by the database layer.
	policy := &models.LicensePolicy{
		RepositoryID: &repoID, Name: action + " " + denied, Action: action,
		DeniedLicenses: []string{denied}, Enabled: true,
	}
	if err := pg.CreateLicensePolicy(context.Background(), policy); err != nil {
		t.Fatalf("CreateLicensePolicy(%s %s): %v", action, denied, err)
	}

	policy.AllowedLicenses = nil

	if err := pg.UpdateLicensePolicy(context.Background(), policy); err != nil {
		t.Fatalf("UpdateLicensePolicy(%s %s): %v", action, denied, err)
	}
}

func assertQuarantined(t *testing.T, pg *database.PostgresDB, repoID string, want bool, keys ...string) {
	t.Helper()

	got, err := pg.IsArtifactQuarantined(context.Background(), repoID, keys)
	if err != nil {
		t.Fatalf("IsArtifactQuarantined: %v", err)
	}

	if got != want {
		t.Errorf("IsArtifactQuarantined(%v) = %v, want %v", keys, got, want)
	}
}

// assertVulnScanReadBack reads the scan through every read path and compares
// what was stored.
func assertVulnScanReadBack(t *testing.T, pg *database.PostgresDB, repoID, scanID, artifactID, status, scanError string, failed []string) {
	t.Helper()

	ctx := context.Background()

	byID, err := pg.GetVulnScanResultByID(ctx, repoID, scanID)
	if err != nil || byID == nil {
		t.Fatalf("GetVulnScanResultByID: %v %v", byID, err)
	}

	byArtifact, err := pg.GetVulnScanResult(ctx, repoID, artifactID)
	if err != nil || byArtifact == nil {
		t.Fatalf("GetVulnScanResult: %v %v", byArtifact, err)
	}

	listed, err := pg.ListVulnScanResults(ctx, repoID, false)
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListVulnScanResults: %d rows, err %v", len(listed), err)
	}

	for label, r := range map[string]*models.VulnScanResult{"by id": byID, "by artifact": byArtifact, "listed": listed[0]} {
		if r.ScanStatus != status || r.ScanError != scanError {
			t.Errorf("%s: status=%q error=%q, want %q/%q", label, r.ScanStatus, r.ScanError, status, scanError)
		}

		if r.FailedScanners == nil || !slices.Equal(r.FailedScanners, failed) {
			t.Errorf("%s: failed scanners = %#v, want %#v", label, r.FailedScanners, failed)
		}
	}
}

// A scan that failed or only partly ran must survive the database round trip
// with its failed scanners, and the status vocabulary must accept 'partial'.
func TestVulnScanFailureStatusesRoundTrip(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repo := scanPolicyRepo(t, pg, "scan-fail-repo")

	const artifactID = "left-pad@1.0.0"

	scan := &models.VulnScanResult{
		RepositoryID: repo.ID, ArtifactID: artifactID, PackageName: "left-pad", PackageVersion: "1.0.0",
		Ecosystem: "npm", Scanner: "osv,trivy", ScanStatus: "scanning", Vulnerabilities: []models.Vulnerability{},
	}
	if err := pg.CreateVulnScanResult(ctx, scan); err != nil {
		t.Fatalf("CreateVulnScanResult: %v", err)
	}

	assertVulnScanReadBack(t, pg, repo.ID, scan.ID, artifactID, "scanning", "", []string{})

	tests := []struct {
		name       string
		status     string
		scanError  string
		failed     []string
		wantFailed []string
	}{
		{"partial", "partial", "trivy: scan timed out", []string{"trivy"}, []string{"trivy"}},
		{"failed", "failed", "osv: scan failed; trivy: scan timed out", []string{"osv", "trivy"}, []string{"osv", "trivy"}},
		{"completed again", "completed", "", nil, []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			scan.ScanStatus, scan.ScanError, scan.FailedScanners, scan.ScannedAt = tt.status, tt.scanError, tt.failed, &now

			if err := pg.UpdateVulnScanResult(ctx, scan); err != nil {
				t.Fatalf("UpdateVulnScanResult(%s): %v", tt.status, err)
			}

			assertVulnScanReadBack(t, pg, repo.ID, scan.ID, artifactID, tt.status, tt.scanError, tt.wantFailed)
		})
	}

	scan.ScanStatus = "exploded"
	if err := pg.UpdateVulnScanResult(ctx, scan); err == nil {
		t.Error("a status outside the vocabulary must be rejected by the database")
	}
}

// Migration 014 on a database that already holds failed scans: the raw scanner
// error stored by older versions is replaced, and the new columns default so
// existing rows keep working.
func TestMigration014SanitizesLegacyFailures(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	db := pg.GetDB()
	repo := scanPolicyRepo(t, pg, "legacy-repo")

	mustExec := func(q string, args ...any) {
		t.Helper()

		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}

	// Put the schema back to its state before migration 014.
	mustExec(`ALTER TABLE vuln_results DROP COLUMN failed_scanners`)
	mustExec(`DROP INDEX idx_license_results_quarantined`)
	mustExec(`ALTER TABLE license_results DROP COLUMN quarantined, DROP COLUMN quarantine_approved`)
	mustExec(`ALTER TABLE scan_jobs DROP CONSTRAINT scan_jobs_status_check`)
	mustExec(`ALTER TABLE scan_jobs ADD CONSTRAINT scan_jobs_status_check CHECK (status IN ('pending', 'running', 'completed', 'failed'))`)

	res, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version LIKE '014%'`)
	if err != nil {
		t.Fatalf("forget migration 014: %v", err)
	}

	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("expected exactly one tracked 014 migration, removed %d", n)
	}

	const rawError = `run trivy: exec: "trivy": executable file not found in $PATH (stderr: /srv/oa/.cache/token=abc)`

	insertJob := func(path, scanType, status, message string) string {
		t.Helper()

		var refID, jobID string

		if err := db.QueryRowContext(ctx,
			`INSERT INTO artifact_refs (repository_id, artifact_path, format) VALUES ($1, $2, 'npm') RETURNING id`,
			repo.ID, path).Scan(&refID); err != nil {
			t.Fatal(err)
		}

		if err := db.QueryRowContext(ctx,
			`INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, error_message) VALUES ($1, $2, 'trivy', $3, NULLIF($4, '')) RETURNING id`,
			refID, scanType, status, message).Scan(&jobID); err != nil {
			t.Fatal(err)
		}

		return jobID
	}

	failedVuln := insertJob("a@1", "vuln", "failed", rawError)
	okVuln := insertJob("b@1", "vuln", "completed", "")
	failedSecret := insertJob("c@1", "secret", "failed", "secret scanner message")

	mustExec(`INSERT INTO vuln_results (scan_job_id) VALUES ($1)`, failedVuln)
	mustExec(`INSERT INTO vuln_results (scan_job_id) VALUES ($1)`, okVuln)

	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("re-apply pending migration 014: %v", err)
	}

	message := func(jobID string) *string {
		t.Helper()

		var m *string
		if err := db.QueryRowContext(ctx, `SELECT error_message FROM scan_jobs WHERE id = $1`, jobID).Scan(&m); err != nil {
			t.Fatal(err)
		}

		return m
	}

	if m := message(failedVuln); m == nil || *m != "scan failed" {
		t.Errorf("legacy failed vuln scan error = %v, want the neutral %q", m, "scan failed")
	}

	if m := message(okVuln); m != nil {
		t.Errorf("a completed scan has no error, got %q", *m)
	}

	if m := message(failedSecret); m == nil || *m != "secret scanner message" {
		t.Errorf("only vuln scans are rewritten, secret scan message = %v", m)
	}

	var failedScanners int
	if err := db.QueryRowContext(ctx, `SELECT cardinality(failed_scanners) FROM vuln_results WHERE scan_job_id = $1`, failedVuln).Scan(&failedScanners); err != nil {
		t.Fatalf("failed_scanners must exist after the migration: %v", err)
	}

	if failedScanners != 0 {
		t.Errorf("legacy rows default to no failed scanners, got %d", failedScanners)
	}

	if _, err := db.ExecContext(ctx, `UPDATE scan_jobs SET status = 'partial' WHERE id = $1`, okVuln); err != nil {
		t.Errorf("status partial must be accepted after the migration: %v", err)
	}
}

// assertLicenseSourceRoundTrip checks that the license source and the
// unresolved flag survive the database.
func assertLicenseSourceRoundTrip(t *testing.T, pg *database.PostgresDB, ctx context.Context, repoID string) {
	t.Helper()

	log := newTestLogger(t)

	if _, _, err := licensepolicy.Record(ctx, pg, log, repoID, "decl-pkg@1.0.0", []string{"MIT"}, models.LicenseSourceClientDeclared); err != nil {
		t.Fatalf("Record MIT: %v", err)
	}

	declared, err := pg.GetLicenseScanResult(ctx, repoID, "decl-pkg@1.0.0")
	if err != nil || declared == nil || declared.LicenseSource != models.LicenseSourceClientDeclared {
		t.Fatalf("stored result = %+v (%v), want license_source client_declared", declared, err)
	}

	if _, _, err = licensepolicy.Record(ctx, pg, log, repoID, "bsd-pkg@1.0.0", []string{"BSD"}, models.LicenseSourceArtifact); err != nil {
		t.Fatalf("Record BSD: %v", err)
	}

	stored, err := pg.GetLicenseScanResult(ctx, repoID, "bsd-pkg@1.0.0")
	if err != nil || stored == nil || stored.LicenseSource != models.LicenseSourceArtifact || len(stored.UnresolvedLicenses) != 1 || stored.UnresolvedLicenses[0] != "BSD" {
		t.Fatalf("stored result = %+v (%v), want license_source artifact and BSD unresolved", stored, err)
	}
}

// A license result quarantines the artifact for downloads exactly like a
// vulnerability or secret result; approval releases it and is remembered.
func TestLicenseQuarantineLifecycle(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repo := scanPolicyRepo(t, pg, "lic-repo")
	other := scanPolicyRepo(t, pg, "lic-other")
	log := newTestLogger(t)

	scanPolicyBlock(t, pg, repo.ID, "block", "GPL-3.0")

	// Detected after upload: the policy blocks it, so the artifact is quarantined.
	res, violations, err := licensepolicy.Record(ctx, pg, log, repo.ID, "leftpad@1.0.0", []string{"GPL-3.0"}, models.LicenseSourceClientDeclared)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	if !res.Quarantined || len(licensepolicy.Blocked(violations)) != 1 {
		t.Fatalf("result = %+v violations = %v, want quarantined by the block policy", res, violations)
	}

	assertLicenseSourceRoundTrip(t, pg, ctx, repo.ID)

	assertQuarantined(t, pg, repo.ID, true, "leftpad@1.0.0")
	assertQuarantined(t, pg, repo.ID, true, "unrelated", "leftpad@1.0.0")
	assertQuarantined(t, pg, repo.ID, false, "leftpad@2.0.0")
	assertQuarantined(t, pg, other.ID, false, "leftpad@1.0.0") // per repository

	// A re-scan does not release it.
	if res, _, err = licensepolicy.Record(ctx, pg, log, repo.ID, "leftpad@1.0.0", []string{"GPL-3.0-or-later"}, models.LicenseSourceClientDeclared); err != nil || !res.Quarantined {
		t.Fatalf("re-scan: %+v %v", res, err)
	}

	// An admin approves it.
	if err := pg.SetLicenseScanQuarantine(ctx, repo.ID, res.ID, false); err != nil {
		t.Fatalf("approve: %v", err)
	}

	assertQuarantined(t, pg, repo.ID, false, "leftpad@1.0.0")

	stored, err := pg.GetLicenseScanResult(ctx, repo.ID, "leftpad@1.0.0")
	if err != nil || stored == nil || stored.Quarantined || !stored.QuarantineApproved {
		t.Fatalf("after approval: %+v %v", stored, err)
	}

	// The approval holds for a re-scan of the same licenses ...
	if res, _, err = licensepolicy.Record(ctx, pg, log, repo.ID, "leftpad@1.0.0", []string{"GPL-3.0-or-later"}, models.LicenseSourceClientDeclared); err != nil || res.Quarantined || !res.QuarantineApproved {
		t.Fatalf("re-scan after approval: %+v %v", res, err)
	}

	assertQuarantined(t, pg, repo.ID, false, "leftpad@1.0.0")

	// ... not for different licenses ...
	if res, _, err = licensepolicy.Record(ctx, pg, log, repo.ID, "leftpad@1.0.0", []string{"GPL-3.0-only"}, models.LicenseSourceClientDeclared); err != nil || !res.Quarantined || res.QuarantineApproved {
		t.Fatalf("other licenses after approval: %+v %v", res, err)
	}

	assertQuarantined(t, pg, repo.ID, true, "leftpad@1.0.0")

	// ... and the admin can quarantine by hand again.
	if err := pg.SetLicenseScanQuarantine(ctx, repo.ID, res.ID, false); err != nil {
		t.Fatalf("approve again: %v", err)
	}

	if err := pg.SetLicenseScanQuarantine(ctx, repo.ID, res.ID, true); err != nil {
		t.Fatalf("quarantine by hand: %v", err)
	}

	assertQuarantined(t, pg, repo.ID, true, "leftpad@1.0.0")
}

// An admin of one repository cannot touch another repository's scan, and an
// unknown id is reported as such.
func TestLicenseQuarantineIsScopedToRepository(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repo := scanPolicyRepo(t, pg, "scope-repo")
	other := scanPolicyRepo(t, pg, "scope-other")

	scanPolicyBlock(t, pg, repo.ID, "block", "GPL-3.0")

	res, _, err := licensepolicy.Record(ctx, pg, newTestLogger(t), repo.ID, "leftpad@1.0.0", []string{"GPL-3.0"}, models.LicenseSourceClientDeclared)
	if err != nil || !res.Quarantined {
		t.Fatalf("Record: %+v %v", res, err)
	}

	if err := pg.SetLicenseScanQuarantine(ctx, other.ID, res.ID, false); !errors.Is(err, database.ErrLicenseScanNotFound) {
		t.Errorf("approve through another repository: err = %v, want ErrLicenseScanNotFound", err)
	}

	if err := pg.SetLicenseScanQuarantine(ctx, repo.ID, "77777777-7777-7777-7777-777777777777", false); !errors.Is(err, database.ErrLicenseScanNotFound) {
		t.Errorf("unknown scan: err = %v, want ErrLicenseScanNotFound", err)
	}

	assertQuarantined(t, pg, repo.ID, true, "leftpad@1.0.0")
}

// Warn and allow policies report violations but never quarantine.
func TestLicenseWarnAndAllowNeverQuarantine(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repo := scanPolicyRepo(t, pg, "warn-repo")

	scanPolicyBlock(t, pg, repo.ID, "warn", "AGPL-3.0")
	scanPolicyBlock(t, pg, repo.ID, "allow", "AGPL-3.0")

	res, violations, err := licensepolicy.Record(ctx, pg, newTestLogger(t), repo.ID, "agpl-pkg@1.0.0", []string{"AGPL-3.0"}, models.LicenseSourceClientDeclared)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	if res.Quarantined || len(violations) == 0 || len(licensepolicy.Blocked(violations)) != 0 {
		t.Fatalf("warn/allow must report but not quarantine: %+v %v", res, violations)
	}

	assertQuarantined(t, pg, repo.ID, false, "agpl-pkg@1.0.0")
}
