// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// reconcileRepoStats runs reconcile passes until nothing is left to do and
// returns the repositories recomputed.
func reconcileRepoStats(t *testing.T, pg *PostgresDB) int {
	t.Helper()

	total := 0

	for range 20 {
		n, err := pg.ReconcileRepoStats(context.Background(), 50, time.Hour)
		if err != nil {
			t.Fatalf("ReconcileRepoStats: %v", err)
		}

		total += n

		if n < 50 {
			return total
		}
	}

	t.Fatal("ReconcileRepoStats did not settle")

	return total
}

func repoStatsOf(t *testing.T, pg *PostgresDB, repo string) models.RepoStats {
	t.Helper()

	stats, err := pg.RepoCatalogStats(context.Background(), []string{repo})
	if err != nil {
		t.Fatalf("RepoCatalogStats: %v", err)
	}

	return stats[repo]
}

func dirtyMarks(t *testing.T, pg *PostgresDB) int {
	t.Helper()

	return countRows(t, pg, `SELECT count(*) FROM repo_stats_dirty`)
}

// The stored figures lag the data until the worker's next pass and then follow
// every kind of write: upload, delete, trash, restore and scan.
//
//nolint:gocyclo,cyclop // one subtest per kind of change over one shared fixture
func TestRepoStatsFollowTheData(t *testing.T) {
	registerFixtures(t)

	ctx := context.Background()
	f, base := seedCatalog(t)

	if s := repoStatsOf(t, f.pg, f.repo); s.PackageCount != 0 || s.LastPublishedAt != nil || s.Security.Critical != 0 {
		t.Fatalf("before the first pass the figures must read as zeros, got %+v", s)
	}

	reconcileRepoStats(t, f.pg)

	s := repoStatsOf(t, f.pg, f.repo)
	if s.PackageCount != 2 || s.Security.Critical != 1 || s.Security.High != 2 ||
		s.LastPublishedAt == nil || !s.LastPublishedAt.Equal(base.Add(2*time.Hour)) || s.Security.ScannedAt == nil {
		t.Fatalf("seeded figures %+v", s)
	}

	if n := dirtyMarks(t, f.pg); n != 0 {
		t.Fatalf("%d marks left after the pass", n)
	}

	t.Run("upload", func(t *testing.T) {
		f.seedNPMVersion(t, "gamma", "1.0.0", 7, base.Add(5*time.Hour))
		reconcileRepoStats(t, f.pg)

		s := repoStatsOf(t, f.pg, f.repo)
		if s.PackageCount != 3 || s.LastPublishedAt == nil || !s.LastPublishedAt.Equal(base.Add(5*time.Hour)) {
			t.Fatalf("after an upload: %+v", s)
		}
	})

	t.Run("a new version of a known package is not a new package", func(t *testing.T) {
		f.seedNPMVersion(t, "gamma", "1.1.0", 7, base.Add(6*time.Hour))
		reconcileRepoStats(t, f.pg)

		if s := repoStatsOf(t, f.pg, f.repo); s.PackageCount != 3 || !s.LastPublishedAt.Equal(base.Add(6*time.Hour)) {
			t.Fatalf("after a second version: %+v", s)
		}
	})

	t.Run("trash and restore", func(t *testing.T) {
		f.pg.SetTrashRetentionDays(7)

		for _, v := range []string{"gamma@1.0.0", "gamma@1.1.0"} {
			if _, err := f.pg.TrashRows(ctx, f.repo, models.TrashKindNPM, []string{f.ids[v]}, "alice"); err != nil {
				t.Fatalf("trash %s: %v", v, err)
			}
		}

		reconcileRepoStats(t, f.pg)

		if s := repoStatsOf(t, f.pg, f.repo); s.PackageCount != 2 || !s.LastPublishedAt.Equal(base.Add(2*time.Hour)) {
			t.Fatalf("after trashing every version of a package: %+v", s)
		}

		if _, err := f.pg.RestoreTrash(ctx, models.TrashKindNPM, f.ids["gamma@1.0.0"]); err != nil {
			t.Fatalf("restore: %v", err)
		}

		reconcileRepoStats(t, f.pg)

		if s := repoStatsOf(t, f.pg, f.repo); s.PackageCount != 3 {
			t.Fatalf("after a restore: %+v", s)
		}
	})

	t.Run("delete", func(t *testing.T) {
		if _, err := f.pg.deleteFiles(ctx, f.repo, []string{f.ids["gamma@1.0.0"], f.ids["gamma@1.1.0"]}); err != nil {
			t.Fatalf("delete: %v", err)
		}

		reconcileRepoStats(t, f.pg)

		if s := repoStatsOf(t, f.pg, f.repo); s.PackageCount != 2 || !s.LastPublishedAt.Equal(base.Add(2*time.Hour)) {
			t.Fatalf("after a delete: %+v", s)
		}
	})

	t.Run("a trashed repository counts nothing and comes back whole", func(t *testing.T) {
		if _, err := f.pg.TrashRepository(ctx, f.repo, "alice"); err != nil {
			t.Fatalf("trash repository: %v", err)
		}

		reconcileRepoStats(t, f.pg)

		if s := repoStatsOf(t, f.pg, f.repo); s.PackageCount != 0 {
			t.Fatalf("trashed repository: %+v", s)
		}

		if _, err := f.pg.RestoreTrash(ctx, models.TrashKindRepository, f.repo); err != nil {
			t.Fatalf("restore repository: %v", err)
		}

		reconcileRepoStats(t, f.pg)

		if s := repoStatsOf(t, f.pg, f.repo); s.PackageCount != 2 {
			t.Fatalf("restored repository: %+v", s)
		}
	})

	t.Run("scan", func(t *testing.T) {
		secret := seedScanJob(t, f.db, f.repo, "beta-1.0.0.tgz", "secret", "gitleaks", "completed", base.Add(7*time.Hour))
		mustExec(t, f.db,
			`INSERT INTO secret_results (scan_job_id, findings, finding_count, quarantined) VALUES ($1, '[]', 3, true)`, secret)
		reconcileRepoStats(t, f.pg)

		s := repoStatsOf(t, f.pg, f.repo)
		if s.Security.Secrets != 3 || s.Security.Quarantined != 1 || !s.Security.Flagged() ||
			s.Security.ScannedAt == nil || !s.Security.ScannedAt.Equal(base.Add(7*time.Hour)) {
			t.Fatalf("after a secret scan: %+v", s.Security)
		}

		// A newer vulnerability scan of the same artifact with other findings
		// replaces the older one; the older one stops counting.
		newer := seedScanJob(t, f.db, f.repo, f.ids["alpha@2.0.0"], "vuln", "trivy", "completed", base.Add(8*time.Hour))
		seedVulnResult(t, f.db, newer, `[]`, 0, 0, 0, 5, false)
		reconcileRepoStats(t, f.pg)

		s = repoStatsOf(t, f.pg, f.repo)
		if s.Security.Critical != 0 || s.Security.High != 0 || s.Security.Low != 5 {
			t.Fatalf("after a newer vulnerability scan: %+v", s.Security)
		}

		// Deleting the vulnerability scans takes their counts away.
		mustExec(t, f.db, `DELETE FROM scan_jobs WHERE scan_type = 'vuln'`)
		reconcileRepoStats(t, f.pg)

		if s = repoStatsOf(t, f.pg, f.repo); s.Security.Low != 0 || s.Security.Secrets != 3 {
			t.Fatalf("after deleting the vulnerability scans: %+v", s.Security)
		}
	})
}

// Only the latest job of a type counts: a second secret scan of the same
// artifact (another scanner configuration) must not double the findings.
func TestRepoStatsCountOnlyTheLatestJobOfAType(t *testing.T) {
	f, base := seedCatalog(t)

	old := seedScanJob(t, f.db, f.repo, "beta-1.0.0.tgz", "secret", "gitleaks", "completed", base.Add(3*time.Hour))
	mustExec(t, f.db, `INSERT INTO secret_results (scan_job_id, findings, finding_count) VALUES ($1, '[]', 4)`, old)

	latest := seedScanJob(t, f.db, f.repo, "beta-1.0.0.tgz", "secret", "trufflehog", "completed", base.Add(4*time.Hour))
	mustExec(t, f.db, `INSERT INTO secret_results (scan_job_id, findings, finding_count) VALUES ($1, '[]', 2)`, latest)

	reconcileRepoStats(t, f.pg)

	if s := repoStatsOf(t, f.pg, f.repo); s.Security.Secrets != 2 {
		t.Fatalf("secrets = %d, want 2 (the latest job only)", s.Security.Secrets)
	}
}

// The reconcile pass repairs figures no write announced: a row that drifted is
// recomputed once it is older than the drift age, and one that was never
// computed is filled in.
func TestRepoStatsReconcileRepairsDrift(t *testing.T) {
	ctx := context.Background()
	f, _ := seedCatalog(t)
	other := seedRepo(t, f.db, "catalog-other", "npm")

	reconcileRepoStats(t, f.pg)

	// Corrupt the row and lose the marks: a fresh row is trusted.
	mustExec(t, f.db, `UPDATE repo_stats SET package_count = 99 WHERE repository_id = $1`, f.repo)
	mustExec(t, f.db, `DELETE FROM repo_stats_dirty`)
	reconcileRepoStats(t, f.pg)

	if s := repoStatsOf(t, f.pg, f.repo); s.PackageCount != 99 {
		t.Fatalf("a fresh unmarked row must be left alone, got %d", s.PackageCount)
	}

	// Aged past the drift age it is recomputed.
	mustExec(t, f.db, `UPDATE repo_stats SET refreshed_at = now() - interval '2 hours' WHERE repository_id = $1`, f.repo)

	if n := reconcileRepoStats(t, f.pg); n != 1 {
		t.Fatalf("recomputed %d repositories, want 1", n)
	}

	if s := repoStatsOf(t, f.pg, f.repo); s.PackageCount != 2 {
		t.Fatalf("after the drift pass: %d packages, want 2", s.PackageCount)
	}

	// A repository without any row is filled in even without a mark.
	mustExec(t, f.db, `DELETE FROM repo_stats WHERE repository_id = $1`, other)
	mustExec(t, f.db, `DELETE FROM repo_stats_dirty`)

	if n, err := f.pg.ReconcileRepoStats(ctx, 50, time.Hour); err != nil || n != 1 {
		t.Fatalf("ReconcileRepoStats = %d, %v; want the one repository without a row", n, err)
	}

	if got := countRows(t, f.pg, `SELECT count(*) FROM repo_stats WHERE repository_id = $1`, other); got != 1 {
		t.Fatalf("%d rows for the empty repository, want 1", got)
	}

	if n := reconcileRepoStats(t, f.pg); n != 0 {
		t.Fatalf("a settled database recomputed %d repositories", n)
	}
}

// A pass that fails leaves the marks it claimed, and a repository deleted
// while marked is dropped without error.
func TestRepoStatsMarksSurviveAFailedPassAndDeletedRepositories(t *testing.T) {
	f, _ := seedCatalog(t)
	reconcileRepoStats(t, f.pg)

	f.seedNPMVersion(t, "delta", "1.0.0", 1, time.Now().UTC())

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := f.pg.ReconcileRepoStats(cancelled, 50, time.Hour); err == nil {
		t.Fatal("a cancelled pass must fail")
	}

	if n := dirtyMarks(t, f.pg); n == 0 {
		t.Fatal("the failed pass lost the marks")
	}

	gone := seedRepo(t, f.db, "catalog-gone", "npm")
	mustExec(t, f.db, `DELETE FROM repositories WHERE id = $1`, gone)
	reconcileRepoStats(t, f.pg)

	if s := repoStatsOf(t, f.pg, f.repo); s.PackageCount != 3 {
		t.Fatalf("after the retried pass: %+v", s)
	}

	if n := dirtyMarks(t, f.pg); n != 0 {
		t.Fatalf("%d marks left", n)
	}
}

// writeNPMPackage stores one npm version; unlike seedPackageFile it reports an
// error, for use in goroutines.
func writeNPMPackage(pg *PostgresDB, repo, name string, created time.Time) error {
	sum := sha256.Sum256([]byte(repo + "/" + name))
	sha := hex.EncodeToString(sum[:])
	filename := name + "-1.0.0.tgz"

	_, err := pg.writePackage(context.Background(), &PackageWrite{
		RepoID: repo, Format: "npm", Name: name, NameKey: name, Version: "1.0.0", VersionKey: "1.0.0", CreatedAt: created,
		Files: []FileWrite{{
			Path: filename, Filename: filename,
			Blob: BlobWrite{SHA256: sha, Key: sha, MediaType: "application/octet-stream", Size: 1},
		}},
	})

	return err
}

// Uploads racing each other and the reconcile pass lose no mark: once the
// uploads end and one more pass runs, the count is exact.
func TestRepoStatsConcurrentUploadsLoseNothing(t *testing.T) {
	f := newCatalogFixture(t)
	reconcileRepoStats(t, f.pg)

	const uploads = 40

	var (
		wg       sync.WaitGroup
		stop     = make(chan struct{})
		passDone = make(chan struct{})
		errs     = make(chan error, uploads+1)
	)

	go func() {
		defer close(passDone)

		for {
			select {
			case <-stop:
				return
			default:
			}

			if _, err := f.pg.ReconcileRepoStats(context.Background(), 50, time.Hour); err != nil {
				errs <- err

				return
			}
		}
	}()

	now := time.Now().UTC()

	for i := range uploads {
		wg.Go(func() {
			if err := writeNPMPackage(f.pg, f.repo, fmt.Sprintf("pkg-%02d", i), now); err != nil {
				errs <- err
			}
		})
	}

	wg.Wait()
	close(stop)
	<-passDone
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}

	reconcileRepoStats(t, f.pg)

	if s := repoStatsOf(t, f.pg, f.repo); s.PackageCount != uploads {
		t.Fatalf("package count %d after %d concurrent uploads", s.PackageCount, uploads)
	}
}

// A statement that writes many rows of one repository leaves one mark.
func TestRepoStatsOneMarkPerTransactionAndRepository(t *testing.T) {
	f := newCatalogFixture(t)
	reconcileRepoStats(t, f.pg)

	mustExec(t, f.db, `
		INSERT INTO packages (repository_id, format, name, name_key)
		SELECT $1, 'npm', 'bulk-' || g, 'bulk-' || g FROM generate_series(1, 200) g`, f.repo)

	if n := dirtyMarks(t, f.pg); n != 1 {
		t.Fatalf("%d marks for one statement of 200 rows, want 1", n)
	}
}

// scan_jobs.is_latest points at the newest job of each (artifact, type) and
// follows inserts and deletes; demoting a job is not a change of the job.
func TestScanJobLatestFlag(t *testing.T) {
	f := newCatalogFixture(t)
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)

	latest := func(job string) bool {
		t.Helper()

		return mustScan[bool](t, f.db, `SELECT is_latest FROM scan_jobs WHERE id = $1`, job)
	}

	older := seedScanJob(t, f.db, f.repo, "a.tgz", "vuln", "grype", "completed", base)
	if !latest(older) {
		t.Fatal("the only job is the latest")
	}

	updatedBefore := mustScan[time.Time](t, f.db, `SELECT updated_at FROM scan_jobs WHERE id = $1`, older)

	newer := seedScanJob(t, f.db, f.repo, "a.tgz", "vuln", "trivy", "completed", base.Add(time.Minute))
	if latest(older) || !latest(newer) {
		t.Fatalf("after a newer job: older=%v newer=%v", latest(older), latest(newer))
	}

	if got := mustScan[time.Time](t, f.db, `SELECT updated_at FROM scan_jobs WHERE id = $1`, older); !got.Equal(updatedBefore) {
		t.Fatalf("demoting a job changed its updated_at: %v -> %v", updatedBefore, got)
	}

	// A job inserted out of order does not take the flag from a newer one.
	oldest := seedScanJob(t, f.db, f.repo, "a.tgz", "vuln", "osv", "completed", base.Add(-time.Minute))
	if latest(oldest) || !latest(newer) {
		t.Fatalf("after an older job: oldest=%v newer=%v", latest(oldest), latest(newer))
	}

	// Another type, and another artifact, are independent.
	secret := seedScanJob(t, f.db, f.repo, "a.tgz", "secret", "gitleaks", "completed", base.Add(-time.Hour))
	otherArtifact := seedScanJob(t, f.db, f.repo, "b.tgz", "vuln", "grype", "completed", base.Add(-time.Hour))

	if !latest(secret) || !latest(otherArtifact) || !latest(newer) {
		t.Fatal("jobs of another type or artifact must keep their flag")
	}

	// Deleting the latest job hands the flag to the next newest.
	mustExec(t, f.db, `DELETE FROM scan_jobs WHERE id = $1`, newer)

	if !latest(older) || latest(oldest) {
		t.Fatalf("after deleting the latest: older=%v oldest=%v", latest(older), latest(oldest))
	}

	// Deleting a superseded job changes nothing.
	mustExec(t, f.db, `DELETE FROM scan_jobs WHERE id = $1`, oldest)

	if !latest(older) {
		t.Fatal("the remaining job lost the flag")
	}
}

// The security view reads one job per artifact for vulnerabilities, the latest.
func TestSecurityViewListsTheLatestVulnerabilityJobOnly(t *testing.T) {
	f := newCatalogFixture(t)
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)

	old := seedScanJob(t, f.db, f.repo, "a.tgz", "vuln", "grype", "completed", base)
	seedVulnResult(t, f.db, old, `[{"id":"CVE-1","severity":"high"}]`, 0, 1, 0, 0, false)

	newer := seedScanJob(t, f.db, f.repo, "a.tgz", "vuln", "trivy", "completed", base.Add(time.Minute))
	seedVulnResult(t, f.db, newer, `[{"id":"CVE-2","severity":"high"},{"id":"CVE-3","severity":"low"}]`, 0, 1, 0, 1, false)

	got := countRows(t, f.pg, `SELECT count(*) FROM security_results WHERE repository_id = $1 AND kind = 'vuln'`, f.repo)
	if got != 1 {
		t.Fatalf("%d vulnerability rows for one artifact, want the latest job's one", got)
	}

	sum, err := f.pg.RepoSecuritySummary(context.Background(), f.repo, "", "")
	if err != nil {
		t.Fatalf("RepoSecuritySummary: %v", err)
	}

	if sum.VulnHigh != 1 || sum.VulnLow != 1 {
		t.Fatalf("summary %+v, want the newer job's counts", sum)
	}
}
