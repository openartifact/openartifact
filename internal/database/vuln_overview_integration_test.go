// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// vulnSeed inserts one vulnerability scan (artifact ref, job, result).
type vulnSeed struct {
	path, scanner, status       string
	critical, high, medium, low int
	quarantined                 bool
	completedAt                 *time.Time
	createdAt                   time.Time
	id                          string
}

func seedVulnScan(t *testing.T, db *sql.DB, repoID string, s vulnSeed) string {
	t.Helper()

	var refID string
	if err := db.QueryRowContext(context.Background(), `INSERT INTO artifact_refs (repository_id, artifact_path, format)
		VALUES ($1, $2, 'raw') ON CONFLICT (repository_id, artifact_path) DO UPDATE SET artifact_path = EXCLUDED.artifact_path RETURNING id`,
		repoID, s.path).Scan(&refID); err != nil {
		t.Fatalf("insert artifact ref: %v", err)
	}

	var jobID string
	if err := db.QueryRowContext(context.Background(), `INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, completed_at, created_at)
		VALUES ($1, 'vuln', $2, $3, $4, $5) RETURNING id`, refID, s.scanner, s.status, s.completedAt, s.createdAt).Scan(&jobID); err != nil {
		t.Fatalf("insert scan job: %v", err)
	}

	id := s.id
	if id == "" {
		id = mustScan[string](t, db, `SELECT gen_random_uuid()::text`)
	}

	mustExec(t, db, `INSERT INTO vuln_results (id, scan_job_id, vulnerabilities, vuln_count, critical_count, high_count, medium_count, low_count,
		quarantined, package_name, package_version, ecosystem)
		VALUES ($1, $2, '[{"id":"CVE-1","summary":"big payload"}]', $3, $4, $5, $6, $7, $8, $9, '1.0.0', 'npm')`,
		id, jobID, s.critical+s.high+s.medium+s.low, s.critical, s.high, s.medium, s.low, s.quarantined, s.path)

	return id
}

func seedRepo(t *testing.T, db *sql.DB, name, format string) string {
	t.Helper()

	project := mustScan[string](t, db, `INSERT INTO projects (name, slug) VALUES ($1, $1) ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name RETURNING id`, "p-"+name)

	return mustScan[string](t, db, `INSERT INTO repositories (name, format, project_id) VALUES ($1, $2, $3) RETURNING id`, name, format, project)
}

func vulnOverviewIDs(page *models.VulnScanOverviewPage) []string {
	ids := make([]string, 0, len(page.Scans))
	for _, s := range page.Scans {
		ids = append(ids, s.ID)
	}

	return ids
}

// Scan ids are fixed so the id tiebreak is deterministic.
const (
	idCritNewest = "00000000-0000-0000-0000-000000000001"
	idCritOld    = "00000000-0000-0000-0000-000000000002"
	idCrit2      = "00000000-0000-0000-0000-000000000003"
	idHighA      = "00000000-0000-0000-0000-000000000004" // ties with idHighB on every key
	idHighB      = "00000000-0000-0000-0000-000000000005"
	idMedium     = "00000000-0000-0000-0000-000000000006"
	idClean      = "00000000-0000-0000-0000-000000000007"
	idFailed     = "00000000-0000-0000-0000-000000000008"
	idScanning   = "00000000-0000-0000-0000-00000000000a"
	idLow        = "00000000-0000-0000-0000-00000000000b"
)

// overviewFixture is a database with ten scans in two repositories.
type overviewFixture struct {
	pg       *PostgresDB
	pypiRepo string
}

func newOverviewFixture(t *testing.T) *overviewFixture {
	t.Helper()

	pg := newQuotaTestDB(t, 4)
	db := pg.GetDB()

	npmRepo := seedRepo(t, db, "npm-main", "npm")
	pypiRepo := seedRepo(t, db, "pypi-main", "pypi")

	now := time.Now().UTC().Truncate(time.Microsecond)
	done := func(ago time.Duration) *time.Time { v := now.Add(-ago); return &v }
	tied := done(time.Hour)

	seeds := []struct {
		repo string
		s    vulnSeed
	}{
		{npmRepo, vulnSeed{id: idCritNewest, path: "a", scanner: "osv", status: "completed", critical: 2, high: 1, completedAt: done(time.Minute), createdAt: now}},
		{npmRepo, vulnSeed{id: idCritOld, path: "b", scanner: "trivy", status: "completed", critical: 2, high: 1, quarantined: true, completedAt: done(48 * time.Hour), createdAt: now}},
		{pypiRepo, vulnSeed{id: idCrit2, path: "c", scanner: "grype", status: "completed", critical: 2, high: 5, completedAt: done(72 * time.Hour), createdAt: now}},
		{pypiRepo, vulnSeed{id: idHighA, path: "d", scanner: "osv,trivy", status: "completed", high: 3, completedAt: tied, createdAt: now}},
		{pypiRepo, vulnSeed{id: idHighB, path: "e", scanner: "osv", status: "partial", high: 3, completedAt: tied, createdAt: now}},
		{npmRepo, vulnSeed{id: idMedium, path: "f", scanner: "osv", status: "completed", medium: 4, completedAt: done(time.Hour), createdAt: now}},
		{npmRepo, vulnSeed{id: idLow, path: "g", scanner: "osv", status: "completed", low: 1, completedAt: done(time.Hour), createdAt: now}},
		{npmRepo, vulnSeed{id: idClean, path: "h", scanner: "osv", status: "completed", completedAt: done(time.Hour), createdAt: now}},
		{pypiRepo, vulnSeed{id: idFailed, path: "i", scanner: "trivy", status: "failed", createdAt: now.Add(-time.Hour)}},
		{npmRepo, vulnSeed{id: idScanning, path: "j", scanner: "osv", status: "running", createdAt: now}},
	}
	for _, sd := range seeds {
		seedVulnScan(t, db, sd.repo, sd.s)
	}

	return &overviewFixture{pg: pg, pypiRepo: pypiRepo}
}

func (f *overviewFixture) list(t *testing.T, p models.VulnScanListParams) *models.VulnScanOverviewPage {
	t.Helper()

	if p.Limit == 0 {
		p.Limit = 50
	}

	page, err := f.pg.ListVulnScanOverview(context.Background(), p)
	if err != nil {
		t.Fatalf("ListVulnScanOverview(%+v): %v", p, err)
	}

	return page
}

func TestVulnScanOverview_OrderIsWorstFirst(t *testing.T) {
	f := newOverviewFixture(t)

	got := vulnOverviewIDs(f.list(t, models.VulnScanListParams{}))
	// Counts decide first (critical, high, medium, low): the medium scan
	// precedes the low one, which precedes the rows without findings, where
	// the newest comes first (the one still scanning) and an equal time falls
	// back to the id. idHighA and idHighB tie on every key.
	want := []string{idCrit2, idCritNewest, idCritOld, idHighA, idHighB, idMedium, idLow, idScanning, idClean, idFailed}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestVulnScanOverview_RowHasItsRepositoryAndNoFindings(t *testing.T) {
	f := newOverviewFixture(t)

	page := f.list(t, models.VulnScanListParams{Repo: "pypi-main", Severity: "critical"})
	if len(page.Scans) != 1 {
		t.Fatalf("got %d scans, want 1", len(page.Scans))
	}

	s := page.Scans[0]
	if s.ID != idCrit2 || s.RepositoryName != "pypi-main" || s.RepositoryFormat != "pypi" || s.RepositoryID != f.pypiRepo ||
		s.ArtifactID != "c" || s.Scanner != "grype" || s.ScanStatus != "completed" || s.CriticalCount != 2 || s.HighCount != 5 || s.VulnCount != 7 {
		t.Errorf("unexpected row %+v", s)
	}

	if s.Vulnerabilities != nil {
		t.Errorf("the overview selected the findings: %v", s.Vulnerabilities)
	}

	if s.ScannedAt == nil {
		t.Error("a completed scan has no scanned_at")
	}
}

func TestVulnScanOverview_KeysetPagingWalksEveryScanOnce(t *testing.T) {
	f := newOverviewFixture(t)

	want := vulnOverviewIDs(f.list(t, models.VulnScanListParams{}))

	for _, size := range []int{1, 2, 3, 4, 9} {
		var got []string

		var after *models.VulnScanCursor

		for range 20 {
			page := f.list(t, models.VulnScanListParams{Limit: size, After: after})
			got = append(got, vulnOverviewIDs(page)...)

			if page.Next == nil {
				break
			}

			if len(page.Scans) != size {
				t.Fatalf("size %d: a page with more to follow has %d scans", size, len(page.Scans))
			}

			after = page.Next
		}

		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("size %d: paged = %v, want %v", size, got, want)
		}
	}
}

func TestVulnScanOverview_Filters(t *testing.T) {
	f := newOverviewFixture(t)

	tests := []struct {
		name   string
		params models.VulnScanListParams
		want   []string
	}{
		{"repository", models.VulnScanListParams{Repo: "pypi-main"}, []string{idCrit2, idHighA, idHighB, idFailed}},
		{"unknown repository", models.VulnScanListParams{Repo: "nope"}, nil},
		{"severity critical", models.VulnScanListParams{Severity: "critical"}, []string{idCrit2, idCritNewest, idCritOld}},
		{"severity high includes critical", models.VulnScanListParams{Severity: "high"}, []string{idCrit2, idCritNewest, idCritOld, idHighA, idHighB}},
		{"severity medium", models.VulnScanListParams{Severity: "medium"}, []string{idCrit2, idCritNewest, idCritOld, idHighA, idHighB, idMedium}},
		{"severity low", models.VulnScanListParams{Severity: "low"}, []string{idCrit2, idCritNewest, idCritOld, idHighA, idHighB, idMedium, idLow}},
		{"status failed", models.VulnScanListParams{Status: "failed"}, []string{idFailed}},
		{"status partial", models.VulnScanListParams{Status: "partial"}, []string{idHighB}},
		{"status scanning is a running job", models.VulnScanListParams{Status: "scanning"}, []string{idScanning}},
		{"scanner trivy matches a joined list", models.VulnScanListParams{Scanner: "trivy", Status: "completed"}, []string{idCritOld, idHighA}},
		{"scanner grype", models.VulnScanListParams{Scanner: "grype"}, []string{idCrit2}},
		{"quarantined", models.VulnScanListParams{Quarantined: new(true)}, []string{idCritOld}},
		{"not quarantined and critical", models.VulnScanListParams{Quarantined: new(false), Severity: "critical"}, []string{idCrit2, idCritNewest}},
		{"filters combine", models.VulnScanListParams{Repo: "npm-main", Scanner: "osv", Severity: "high"}, []string{idCritNewest}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vulnOverviewIDs(f.list(t, tt.params))
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVulnScanOverview_UnknownFiltersAreRefused(t *testing.T) {
	f := newOverviewFixture(t)

	for _, p := range []models.VulnScanListParams{
		{Severity: "huge", Limit: 5}, {Status: "done", Limit: 5}, {Scanner: "snyk", Limit: 5}, {Limit: -1}, {},
	} {
		if _, err := f.pg.ListVulnScanOverview(context.Background(), p); !errors.Is(err, ErrInvalidVulnScanFilter) {
			t.Errorf("%+v: err = %v, want ErrInvalidVulnScanFilter", p, err)
		}
	}
}

func TestVulnScanOverview_Summary(t *testing.T) {
	f := newOverviewFixture(t)
	ctx := context.Background()

	tests := []struct {
		repo string
		want models.VulnScanSummary
	}{
		{"", models.VulnScanSummary{Total: 10, Completed: 7, Partial: 1, Failed: 1, Scanning: 1, Quarantined: 1, WithCritical: 3, WithHigh: 5, WithMedium: 6, WithLow: 7}},
		{"pypi-main", models.VulnScanSummary{Total: 4, Completed: 2, Partial: 1, Failed: 1, WithCritical: 1, WithHigh: 3, WithMedium: 3, WithLow: 3}},
		{"nope", models.VulnScanSummary{}},
	}

	for _, tt := range tests {
		got, err := f.pg.VulnScanOverviewSummary(ctx, tt.repo)
		if err != nil {
			t.Fatal(err)
		}

		if *got != tt.want {
			t.Errorf("repo %q: summary = %+v, want %+v", tt.repo, *got, tt.want)
		}
	}
}

// TestVulnScanOverviewUsesIndexes guards the filters that must not scan the
// whole result table: every one is answered from an index when it is
// selective. Sequential scans are disabled so the plan shows which index each
// filter can use at all; tables this small otherwise prefer a scan.
func TestVulnScanOverviewUsesIndexes(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	db := pg.GetDB()

	// 40 repositories x 500 artifacts. About 1% of the scans have critical
	// findings, 1% failed, 1% are quarantined.
	project := mustScan[string](t, db, `INSERT INTO projects (name, slug) VALUES ('bulk', 'bulk') RETURNING id`)
	mustExec(t, db, `INSERT INTO repositories (id, name, format, project_id)
		SELECT gen_random_uuid(), 'r' || i, 'raw', $1::uuid FROM generate_series(1, 40) i`, project)
	mustExec(t, db, `INSERT INTO artifact_refs (repository_id, artifact_path, format)
		SELECT r.id, 'p' || g, 'raw' FROM repositories r, generate_series(1, 500) g`)
	mustExec(t, db, `INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, completed_at)
		SELECT id, 'vuln', 'osv', CASE WHEN n % 100 = 0 THEN 'failed' ELSE 'completed' END, now()
		FROM (SELECT id, row_number() OVER () AS n FROM artifact_refs) a`)
	mustExec(t, db, `INSERT INTO vuln_results (scan_job_id, vuln_count, critical_count, high_count, quarantined, package_name, package_version, ecosystem)
		SELECT id, 1, CASE WHEN n % 100 = 1 THEN 1 ELSE 0 END, 1, n % 100 = 2, 'x', '1', 'npm'
		FROM (SELECT id, row_number() OVER () AS n FROM scan_jobs) j`)
	mustExec(t, db, `ANALYZE artifact_refs; ANALYZE scan_jobs; ANALYZE vuln_results; ANALYZE repositories`)

	tests := []struct {
		name   string
		params models.VulnScanListParams
		index  string
	}{
		{"repository", models.VulnScanListParams{Repo: "r7"}, "artifact_refs_repository_id_artifact_path_key"},
		{"critical severity", models.VulnScanListParams{Severity: "critical"}, "idx_vuln_results_critical"},
		{"quarantined", models.VulnScanListParams{Quarantined: new(true)}, "idx_vuln_results_quarantined"},
		{"failed status", models.VulnScanListParams{Status: "failed"}, "idx_scan_jobs_vuln_problems"},
		{"partial status", models.VulnScanListParams{Status: "partial"}, "idx_scan_jobs_vuln_problems"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.params.Limit = 25

			query, args, err := vulnOverviewQuery(tt.params)
			if err != nil {
				t.Fatal(err)
			}

			plan := explainWithoutSeqScan(t, db, query, args...)
			if !strings.Contains(plan, tt.index) {
				t.Errorf("the plan does not use %s:\n%s", tt.index, plan)
			}
		})
	}

	// The page itself is bounded: LIMIT sits on top of the sort, and no
	// count runs.
	query, args, err := vulnOverviewQuery(models.VulnScanListParams{Limit: 25})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(strings.ToLower(query), "count(") {
		t.Errorf("the page query counts: %s", query)
	}

	if plan := explain(t, db, "EXPLAIN "+query, args...); !strings.Contains(plan, "Limit") {
		t.Errorf("the plan has no Limit node:\n%s", plan)
	}
}

// explainWithoutSeqScan runs EXPLAIN with sequential scans discouraged, on one
// connection so the setting applies to the statement.
func explainWithoutSeqScan(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()

	ctx := context.Background()

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}

	rows, err := conn.QueryContext(ctx, "EXPLAIN "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder

	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}

		plan.WriteString(line + "\n")
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return plan.String()
}
