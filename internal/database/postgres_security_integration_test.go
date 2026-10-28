// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// seedScanJob inserts an artifact ref and a scan job of the given type and
// returns the job id.
func seedScanJob(t *testing.T, db *sql.DB, repoID, path, scanType, scanner, status string, completedAt time.Time) string {
	t.Helper()

	refID := mustScan[string](t, db, `INSERT INTO artifact_refs (repository_id, artifact_path, format)
		VALUES ($1, $2, 'raw') ON CONFLICT (repository_id, artifact_path) DO UPDATE SET artifact_path = EXCLUDED.artifact_path RETURNING id`,
		repoID, path)

	return mustScan[string](t, db, `INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, completed_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $5) RETURNING id`, refID, scanType, scanner, status, completedAt)
}

func seedVulnResult(t *testing.T, db *sql.DB, jobID, vulns string, crit, high, medium, low int, quarantined bool) string {
	t.Helper()

	return mustScan[string](
		t,
		db,
		`INSERT INTO vuln_results (scan_job_id, vulnerabilities, vuln_count, critical_count, high_count, medium_count, low_count,
		quarantined, package_name, package_version, ecosystem)
		VALUES ($1, $2::jsonb, $3, $4, $5, $6, $7, $8, '', '', 'npm') RETURNING id`,
		jobID,
		vulns,
		crit+high+medium+low,
		crit,
		high,
		medium,
		low,
		quarantined,
	)
}

// securityFixture is a database with findings of every type in two projects.
type securityFixture struct {
	pg                 *PostgresDB
	db                 *sql.DB
	repoA, repoB       string
	projectA, projectB string
	ids                map[string]string // label -> result id
	now                time.Time
}

func newSecurityFixture(t *testing.T) *securityFixture {
	t.Helper()

	pg := newQuotaTestDB(t, 4)
	db := pg.GetDB()
	f := &securityFixture{pg: pg, db: db, ids: map[string]string{}, now: time.Now().UTC().Truncate(time.Microsecond)}

	f.repoA = seedRepo(t, db, "npm-main", "npm")
	f.repoB = seedRepo(t, db, "pypi-main", "pypi")
	f.projectA = mustScan[string](t, db, `SELECT project_id FROM repositories WHERE id = $1`, f.repoA)
	f.projectB = mustScan[string](t, db, `SELECT project_id FROM repositories WHERE id = $1`, f.repoB)

	done := func(ago time.Duration) time.Time { return f.now.Add(-ago) }

	// repo A: a critical+high vulnerability result, a clean one, a quarantined
	// license, a secret result and an AI quarantine recommendation.
	job := seedScanJob(t, db, f.repoA, "lodash@4.0.0", "vuln", "osv", "completed", done(time.Hour))
	f.ids["lodash"] = seedVulnResult(t, db, job,
		`[{"id":"CVE-8","severity":"HIGH","cvss":7.5},{"id":"CVE-9","severity":"CRITICAL","cvss":9.8}]`, 1, 1, 0, 0, false)

	job = seedScanJob(t, db, f.repoA, "clean@1.0.0", "vuln", "osv", "completed", done(time.Minute))
	f.ids["clean"] = seedVulnResult(t, db, job, `[]`, 0, 0, 0, 0, false)

	job = seedScanJob(t, db, f.repoA, "gpl-lib@1.0.0", "license", "license", "completed", done(2*time.Hour))
	f.ids["license"] = mustScan[string](t, db, `INSERT INTO license_results (scan_job_id, licenses, quarantined)
		VALUES ($1, '{GPL-3.0-only}', true) RETURNING id`, job)

	job = seedScanJob(t, db, f.repoA, "leaky@1.0.0", "secret", "secrets", "completed", done(3*time.Hour))
	f.ids["secret"] = mustScan[string](t, db, `INSERT INTO secret_results (scan_job_id, findings, finding_count)
		VALUES ($1, '[{"type":"aws-key","description":"AWS access key","severity":"high"},{"type":"x","severity":"weird"}]', 2) RETURNING id`, job)

	job = seedScanJob(t, db, f.repoA, "evil@2.0.0", "ai", "ai", "completed", done(4*time.Hour))
	f.ids["ai"] = mustScan[string](
		t,
		db,
		`INSERT INTO ai_results (scan_job_id, summary, security_notes, quarantine_recommended, artifact_name, artifact_version)
		VALUES ($1, 'looks fine', E'Downloads a payload at install time.\nSecond line', true, 'evil', '2.0.0') RETURNING id`,
		job,
	)

	// repo B: a high vulnerability fully resolved by a VEX decision.
	job = seedScanJob(t, db, f.repoB, "requests@2.0", "vuln", "osv", "completed", done(5*time.Hour))
	f.ids["requests"] = seedVulnResult(t, db, job, `[{"id":"CVE-5","severity":"HIGH"}]`, 0, 1, 0, 0, false)
	mustExec(t, db, `INSERT INTO vex_statements (repository_id, artifact_id, vuln_id, status, justification)
		VALUES ($1, 'requests@2.0', 'cve-5', 'not_affected', 'component_not_present')`, f.repoB)

	// a failed scan and a repository nobody scanned
	seedScanJob(t, db, f.repoB, "broken@1", "vuln", "osv", "failed", done(time.Hour))
	seedRepo(t, db, "raw-empty", "raw")

	return f
}

func findingIDs(page *models.FindingPage) []string {
	ids := make([]string, 0, len(page.Findings))
	for _, f := range page.Findings {
		ids = append(ids, f.ID)
	}

	return ids
}

func listAll(t *testing.T, f *securityFixture, p models.FindingListParams) *models.FindingPage {
	t.Helper()

	if p.Types == nil {
		p.Types = models.FindingTypes()
	}

	if p.Limit == 0 {
		p.Limit = 50
	}

	if !p.Scope.All && p.Scope.ProjectIDs == nil {
		p.Scope.All = true
	}

	page, err := f.pg.ListSecurityFindings(context.Background(), p)
	if err != nil {
		t.Fatalf("ListSecurityFindings: %v", err)
	}

	return page
}

func TestSecurityFindings_ListOrderAndContent(t *testing.T) {
	f := newSecurityFixture(t)
	page := listAll(t, f, models.FindingListParams{})

	// critical vuln, high secret, high AI, high license (quarantined) and the suppressed
	// high vuln: severity rank DESC, then newest scan.
	want := []string{f.ids["lodash"], f.ids["license"], f.ids["secret"], f.ids["ai"], f.ids["requests"]}
	if got := findingIDs(page); !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}

	if page.Next != nil {
		t.Fatalf("Next = %+v, want nil on the last page", page.Next)
	}

	byID := map[string]*models.Finding{}
	for _, fd := range page.Findings {
		byID[fd.ID] = fd
	}

	checkFindingDetails(t, f, byID)
}

func checkFindingDetails(t *testing.T, f *securityFixture, byID map[string]*models.Finding) {
	t.Helper()

	lodash := byID[f.ids["lodash"]]
	if lodash.Type != "vuln" || lodash.Repository != "npm-main" || lodash.Format != "npm" ||
		lodash.PackageName != "lodash" || lodash.PackageVersion != "4.0.0" || lodash.Severity() != "critical" ||
		lodash.Critical != 1 || lodash.High != 1 || lodash.Status != "open" || lodash.Title != "CVE-9" || lodash.Scanner != "osv" {
		t.Fatalf("lodash finding = %+v", lodash)
	}

	checkOtherFindingDetails(t, f, byID)
}

func checkOtherFindingDetails(t *testing.T, f *securityFixture, byID map[string]*models.Finding) {
	t.Helper()

	if lic := byID[f.ids["license"]]; lic.Status != "quarantined" || !lic.Quarantined || lic.Title != "GPL-3.0-only" ||
		lic.Severity() != "high" {
		t.Fatalf("license finding = %+v", lic)
	}

	// the "weird" severity counts as low
	if sec := byID[f.ids["secret"]]; sec.High != 1 || sec.Low != 1 || sec.Title != "AWS access key" {
		t.Fatalf("secret finding = %+v", sec)
	}

	if ai := byID[f.ids["ai"]]; ai.Title != "Downloads a payload at install time." || ai.PackageName != "evil" ||
		ai.PackageVersion != "2.0.0" {
		t.Fatalf("ai finding = %+v", ai)
	}

	if req := byID[f.ids["requests"]]; req.Status != "suppressed" || req.VEXStatus != models.VEXNotAffected {
		t.Fatalf("requests finding = %+v", req)
	}
}

func TestSecurityFindings_Filters(t *testing.T) {
	f := newSecurityFixture(t)

	cases := []struct {
		name string
		p    models.FindingListParams
		want []string
	}{
		{"type vuln", models.FindingListParams{Types: []string{"vuln"}}, []string{"lodash", "requests"}},
		{"types license+ai", models.FindingListParams{Types: []string{"license", "ai"}}, []string{"license", "ai"}},
		{"severity critical", models.FindingListParams{Severity: "critical"}, []string{"lodash"}},
		{"severity high is exact", models.FindingListParams{Severity: "high"}, []string{"license", "secret", "ai", "requests"}},
		{"status suppressed", models.FindingListParams{Status: "suppressed"}, []string{"requests"}},
		{"status quarantined", models.FindingListParams{Status: "quarantined"}, []string{"license"}},
		{"status open", models.FindingListParams{Status: "open"}, []string{"lodash", "secret", "ai"}},
		{"repo", models.FindingListParams{Repo: "pypi-main"}, []string{"requests"}},
		{"project", models.FindingListParams{Project: "p-pypi-main"}, []string{"requests"}},
		{"q package", models.FindingListParams{Q: "LODASH@4"}, []string{"lodash"}},
		{"q repository", models.FindingListParams{Q: "pypi"}, []string{"requests"}},
		{"q wildcard is literal", models.FindingListParams{Q: "%"}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var want []string
			for _, l := range tc.want {
				want = append(want, f.ids[l])
			}

			got := findingIDs(listAll(t, f, tc.p))
			if len(got) == 0 && len(want) == 0 {
				return
			}

			// the order is the contract's; filters only remove rows
			if !slices.Equal(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}

func TestSecurityFindings_Scope(t *testing.T) {
	f := newSecurityFixture(t)

	got := findingIDs(listAll(t, f, models.FindingListParams{Scope: models.FindingScope{ProjectIDs: []string{f.projectB}}}))
	if !slices.Equal(got, []string{f.ids["requests"]}) {
		t.Fatalf("project B scope = %v", got)
	}

	empty := listAll(t, f, models.FindingListParams{Scope: models.FindingScope{ProjectIDs: []string{}}})
	if len(empty.Findings) != 0 {
		t.Fatalf("an empty scope must see nothing, got %v", findingIDs(empty))
	}

	// a repo filter outside the scope matches nothing
	out := listAll(t, f, models.FindingListParams{Scope: models.FindingScope{ProjectIDs: []string{f.projectA}}, Repo: "pypi-main"})
	if len(out.Findings) != 0 {
		t.Fatalf("repo outside the scope = %v", findingIDs(out))
	}
}

func TestSecurityFindings_KeysetPaging(t *testing.T) {
	f := newSecurityFixture(t)
	all := findingIDs(listAll(t, f, models.FindingListParams{}))

	var got []string

	var after *models.FindingCursor

	for range 10 {
		page := listAll(t, f, models.FindingListParams{Limit: 2, After: after})
		got = append(got, findingIDs(page)...)

		if page.Next == nil {
			break
		}

		after = page.Next
	}

	if !slices.Equal(got, all) {
		t.Fatalf("paged = %v, all = %v", got, all)
	}
}

func TestSecurityFindings_InvalidFilter(t *testing.T) {
	f := newSecurityFixture(t)

	for _, p := range []models.FindingListParams{
		{Types: []string{"nope"}, Limit: 5},
		{Types: models.FindingTypes(), Severity: "urgent", Limit: 5},
		{Types: models.FindingTypes(), Status: "gone", Limit: 5},
		{Types: models.FindingTypes(), Limit: 0},
	} {
		if _, err := f.pg.ListSecurityFindings(context.Background(), p); !errors.Is(err, ErrInvalidFindingFilter) {
			t.Fatalf("%+v: err = %v, want ErrInvalidFindingFilter", p, err)
		}
	}
}

func TestSecurityFindings_Summary(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()

	s, err := f.pg.SecurityFindingsSummary(ctx, models.FindingFilter{Scope: models.FindingScope{All: true}})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}

	if v := s.ByType["vuln"]; v.Total != 2 || v.Critical != 1 || v.High != 1 {
		t.Fatalf("vuln = %+v", v)
	}

	if l := s.ByType["license"]; l.Total != 1 || l.High != 1 || l.Quarantined != 1 {
		t.Fatalf("license = %+v", l)
	}

	if a := s.ByType["ai"]; a.Total != 1 || a.High != 1 || a.Quarantined != 0 {
		t.Fatalf("ai = %+v", a)
	}

	// two scanned repositories of three (a failed scan does not count)
	if s.TotalRepositories != 3 || s.ScannedRepositories != 2 {
		t.Fatalf("coverage = %d/%d", s.ScannedRepositories, s.TotalRepositories)
	}

	if s.LastScanAt == nil || !s.LastScanAt.Equal(f.now.Add(-time.Minute)) {
		t.Fatalf("LastScanAt = %v", s.LastScanAt)
	}

	scoped, err := f.pg.SecurityFindingsSummary(ctx, models.FindingFilter{Scope: models.FindingScope{ProjectIDs: []string{f.projectB}}})
	if err != nil {
		t.Fatalf("scoped summary: %v", err)
	}

	if scoped.ByType["vuln"].Total != 1 || scoped.ByType["license"].Total != 0 || scoped.TotalRepositories != 1 {
		t.Fatalf("scoped = %+v", scoped)
	}
}

func TestSecurityHealthStats(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()

	seedScanJob(t, f.db, f.repoB, "queued@1", "vuln", "osv", "pending", f.now)
	// a failure older than 24 hours (an update trigger would reset updated_at, so insert it)
	refID := mustScan[string](
		t,
		f.db,
		`INSERT INTO artifact_refs (repository_id, artifact_path, format) VALUES ($1, 'old-fail@1', 'raw') RETURNING id`,
		f.repoB,
	)
	mustExec(t, f.db, `INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, updated_at)
		VALUES ($1, 'vuln', 'osv', 'failed', now() - interval '2 days')`, refID)

	st, err := f.pg.SecurityHealthStats(ctx)
	if err != nil {
		t.Fatalf("health stats: %v", err)
	}

	if st.ScansPending != 1 || st.ScansFailed24h != 1 || st.UnscannedRepositories != 1 || st.LastScanAt == nil {
		t.Fatalf("stats = %+v", st)
	}

	last, err := f.pg.LatestFinishedDependencyTrackPush(ctx)
	if err != nil || last != nil {
		t.Fatalf("no push yet: %v, %v", last, err)
	}

	mustExec(
		t,
		f.db,
		`INSERT INTO dependency_track_pushes (repository_id, artifact_id, state) VALUES ($1, 'a@1', 'done'), ($1, 'b@1', 'running')`,
		f.repoA,
	)

	last, err = f.pg.LatestFinishedDependencyTrackPush(ctx)
	if err != nil || last == nil || !last.OK || last.At.IsZero() {
		t.Fatalf("latest push = %+v, %v", last, err)
	}
}

func TestRepoSecuritySummary(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()

	s, err := f.pg.RepoSecuritySummary(ctx, f.repoA, "", "")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}

	if s.VulnCritical != 1 || s.VulnHigh != 1 || s.LicenseViolations != 1 || s.SecretCount != 2 ||
		s.AIQuarantineRecommended != 1 || s.Quarantined != 1 || s.ScannedAt == nil || !s.ScannedAt.Equal(f.now.Add(-time.Minute)) {
		t.Fatalf("repo A = %+v", s)
	}

	pkg, err := f.pg.RepoSecuritySummary(ctx, f.repoA, "lodash", "4.0.0")
	if err != nil {
		t.Fatalf("package summary: %v", err)
	}

	if pkg.VulnCritical != 1 || pkg.LicenseViolations != 0 || pkg.SecretCount != 0 || pkg.Quarantined != 0 {
		t.Fatalf("lodash = %+v", pkg)
	}

	none, err := f.pg.RepoSecuritySummary(ctx, f.repoA, "lodash", "9.9.9")
	if err != nil {
		t.Fatalf("unknown version: %v", err)
	}

	if none.ScannedAt != nil || none.VulnCritical != 0 {
		t.Fatalf("unknown version = %+v", none)
	}
}

// A scan started from a package page records the package row id as artifact_id:
// the finding must carry the package's name and version, never the id.
func TestSecurityFindings_ResolvesPackageRowIDs(t *testing.T) {
	f := newSecurityFixture(t)

	blob := mustScan[string](t, f.db,
		`INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES (repeat('b', 64), 3, repeat('b', 64)) RETURNING id`)
	pkg := mustScan[string](t, f.db,
		`INSERT INTO pypi_packages (repository_id, name, version, filename, package_type, blob_id)
		 VALUES ($1, 'data-pipeline', '2.3.1', 'data_pipeline-2.3.1.tar.gz', 'sdist', $2) RETURNING id`, f.repoB, blob)
	purged := "00000000-0000-4000-8000-000000000001"

	job := seedScanJob(t, f.db, f.repoB, pkg, "license", "license", "completed", f.now)
	lic := mustScan[string](t, f.db, `INSERT INTO license_results (scan_job_id, licenses, quarantined)
		VALUES ($1, '{AGPL-3.0-only}', true) RETURNING id`, job)
	job = seedScanJob(t, f.db, f.repoB, purged, "secret", "secrets", "completed", f.now)
	gone := mustScan[string](t, f.db, `INSERT INTO secret_results (scan_job_id, findings, finding_count)
		VALUES ($1, '[{"type":"aws-key","severity":"high"}]', 1) RETURNING id`, job)

	byID := map[string]*models.Finding{}
	for _, fd := range listAll(t, f, models.FindingListParams{}).Findings {
		byID[fd.ID] = fd
	}

	if got := byID[lic]; got == nil || got.PackageName != "data-pipeline" || got.PackageVersion != "2.3.1" || got.ArtifactID != pkg {
		t.Fatalf("license finding of a package row id = %+v, want data-pipeline@2.3.1", got)
	}

	if got := byID[gone]; got == nil || got.PackageName != "" || got.PackageVersion != "" {
		t.Fatalf("finding of a purged package = %+v, want no name", got)
	}

	// name@version artifact ids keep working untouched
	if got := byID[f.ids["lodash"]]; got.PackageName != "lodash" {
		t.Fatalf("lodash = %+v", got)
	}

	// the text filter matches the resolved name and version of those findings
	for q, want := range map[string]bool{"data-pipe": true, "DATA-PIPELINE@2.3": true, "2.3.1": true, "left-pad": false} {
		got := false

		for _, fd := range listAll(t, f, models.FindingListParams{Q: q}).Findings {
			got = got || fd.ID == lic
		}

		if got != want {
			t.Errorf("q=%q finds the license finding of data-pipeline@2.3.1 = %v, want %v", q, got, want)
		}
	}
}

// seedExploitFindings adds a low-severity result whose vulnerability is in the
// KEV catalog and a medium one with an EPSS score above the threshold, newer
// than the fixture's results.
func seedExploitFindings(t *testing.T, f *securityFixture) {
	t.Helper()

	job := seedScanJob(t, f.db, f.repoA, "kevpkg@1.0.0", "vuln", "osv", "completed", f.now.Add(-30*time.Minute))
	f.ids["kev"] = seedVulnResult(t, f.db, job, `[{"id":"CVE-1","severity":"LOW"}]`, 0, 0, 0, 1, false)
	mustExec(t, f.db, `UPDATE vuln_results SET kev_count = 1, max_epss = 0.02 WHERE id = $1`, f.ids["kev"])

	job = seedScanJob(t, f.db, f.repoA, "epsspkg@1.0.0", "vuln", "osv", "completed", f.now.Add(-20*time.Minute))
	f.ids["epss"] = seedVulnResult(t, f.db, job, `[{"id":"CVE-2","severity":"MEDIUM"}]`, 0, 0, 1, 0, false)
	mustExec(t, f.db, `UPDATE vuln_results SET max_epss = 0.3 WHERE id = $1`, f.ids["epss"])
}

func TestSecurityFindings_PriorityOrder(t *testing.T) {
	f := newSecurityFixture(t)
	seedExploitFindings(t, f)

	const threshold = 0.1

	severity := findingIDs(listAll(t, f, models.FindingListParams{Sort: models.FindingSortSeverity, EPSSThreshold: threshold}))
	if k := slices.Index(severity, f.ids["kev"]); k < slices.Index(severity, f.ids["lodash"]) {
		t.Fatalf("sort=severity must ignore KEV: %v", severity)
	}

	byPriority := listAll(t, f, models.FindingListParams{Sort: models.FindingSortPriority, EPSSThreshold: threshold})
	got := findingIDs(byPriority)

	// KEV first (rank 6, although low), the EPSS row next (rank 5, although
	// medium), then the critical vulnerability and the rest in severity order.
	if len(got) < 4 || got[0] != f.ids["kev"] || got[1] != f.ids["epss"] || got[2] != f.ids["lodash"] {
		t.Fatalf("priority order = %v", got)
	}

	if byPriority.Findings[0].SortRank != 6 || byPriority.Findings[1].SortRank != 5 || byPriority.Findings[2].SortRank != 4 {
		t.Fatalf("sort ranks = %d %d %d", byPriority.Findings[0].SortRank, byPriority.Findings[1].SortRank, byPriority.Findings[2].SortRank)
	}

	if k := byPriority.Findings[0]; k.KEVCount != 1 || k.MaxEPSS == nil || k.SeverityRank != 1 {
		t.Fatalf("kev finding aggregates = %+v", k)
	}

	// the same set as the severity order, nothing lost or duplicated
	if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(severity))) {
		t.Fatalf("priority %v and severity %v list different findings", got, severity)
	}

	// an EPSS score below the threshold does not lift a row
	low := findingIDs(listAll(t, f, models.FindingListParams{Sort: models.FindingSortPriority, EPSSThreshold: 0.5}))
	if low[1] == f.ids["epss"] {
		t.Fatalf("EPSS 0.3 must not rank above a critical one at threshold 0.5: %v", low)
	}
}

func TestSecurityFindings_PriorityKeysetPaging(t *testing.T) {
	f := newSecurityFixture(t)
	seedExploitFindings(t, f)

	params := models.FindingListParams{Sort: models.FindingSortPriority, EPSSThreshold: 0.1}
	all := findingIDs(listAll(t, f, params))

	for _, limit := range []int{1, 2, 3} {
		var got []string

		p := params
		p.Limit = limit

		for range 20 {
			page := listAll(t, f, p)
			got = append(got, findingIDs(page)...)

			if page.Next == nil {
				break
			}

			if page.Next.Sort != models.FindingSortPriority {
				t.Fatalf("cursor sort = %q", page.Next.Sort)
			}

			p.After = page.Next
		}

		if !slices.Equal(got, all) {
			t.Fatalf("limit %d: paged = %v, all = %v", limit, got, all)
		}
	}
}

func TestSecurityFindings_ExploitFilters(t *testing.T) {
	f := newSecurityFixture(t)
	seedExploitFindings(t, f)

	yes, no := true, false
	epss, big := 0.1, 0.9
	kevRank, epssRank, criticalRank, lowRank := 6, 5, 4, 1

	cases := []struct {
		name string
		p    models.FindingListParams
		want []string
	}{
		{"kev true", models.FindingListParams{KEV: &yes}, []string{"kev"}},
		{
			"kev false drops only the KEV row",
			models.FindingListParams{KEV: &no, Types: []string{"vuln"}},
			[]string{"lodash", "epss", "requests"},
		},
		{"epss_min", models.FindingListParams{EPSSMin: &epss}, []string{"epss"}},
		{"epss_min above every score", models.FindingListParams{EPSSMin: &big}, nil},
		{"priority kev", models.FindingListParams{PriorityRank: &kevRank}, []string{"kev"}},
		{"priority epss", models.FindingListParams{PriorityRank: &epssRank}, []string{"epss"}},
		{"priority critical is exact", models.FindingListParams{PriorityRank: &criticalRank}, []string{"lodash"}},
		{"priority low excludes the KEV row", models.FindingListParams{PriorityRank: &lowRank}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.p.EPSSThreshold = 0.1

			var want []string
			for _, l := range tc.want {
				want = append(want, f.ids[l])
			}

			got := findingIDs(listAll(t, f, tc.p))
			slices.Sort(got)
			slices.Sort(want)

			if !slices.Equal(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}
