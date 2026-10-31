// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// cleanupSeed is a purgeSeed whose server keeps no trash, so a policy run
// removes rows for good, with the cleanup worker over it.
type cleanupSeed struct {
	*purgeSeed

	w *CleanupWorker
}

func newCleanupSeed(t *testing.T) *cleanupSeed {
	t.Helper()

	s := newPurgeSeed(t)
	s.pg.SetTrashRetentionDays(0)

	return &cleanupSeed{purgeSeed: s, w: NewCleanupWorker(s.pg, s.blobs, testLogger(t))}
}

// mvn writes the row (with a blob of its own) into repoID and returns its id
// and the blob's sha256.
func (s *cleanupSeed) mvn(repoID string, r mvnRow) (string, string) {
	s.t.Helper()

	s.seq++

	blobID, sha := s.blob(fmt.Sprintf("content %d %v", s.seq, r), 2*time.Hour)

	return s.mavenFile(repoID, blobID, r), sha
}

func (s *cleanupSeed) policy(repoID string, keepLast, keepSince *int, pattern string) *models.CleanupPolicy {
	s.t.Helper()

	p := &models.CleanupPolicy{
		RepositoryID: repoID, Name: "p", Enabled: true,
		KeepLastN: keepLast, KeepSinceDays: keepSince, NamePattern: pattern,
	}
	if err := s.pg.CreateCleanupPolicy(context.Background(), p); err != nil {
		s.t.Fatalf("CreateCleanupPolicy: %v", err)
	}

	return p
}

func (s *cleanupSeed) preview(p *models.CleanupPolicy) *CleanupPreview {
	s.t.Helper()

	prev, err := s.w.PreviewPolicy(context.Background(), p.ID)
	if err != nil {
		s.t.Fatalf("PreviewPolicy: %v", err)
	}

	return prev
}

func previewIDs(p *CleanupPreview) []string {
	ids := make([]string, len(p.Items))
	for i, it := range p.Items {
		ids[i] = it.ID
	}

	slices.Sort(ids)

	return ids
}

// TestCleanupPolicySelection pins which rows each retention criterion selects
// and that the dry-run preview names exactly the rows a real run removes.
func TestCleanupPolicySelection(t *testing.T) {
	s := newCleanupSeed(t)
	ctx := context.Background()

	// "a" has releases 1..4 aged 40/30/20/10 days; 1 and 2 ship a jar and a pom.
	// "b" has a single old release.
	rows := []mvnRow{
		{artifact: "a", version: "1.0", ext: "jar", ageDays: 40},
		{artifact: "a", version: "1.0", ext: "pom", ageDays: 40},
		{artifact: "a", version: "2.0", ext: "jar", ageDays: 30},
		{artifact: "a", version: "2.0", ext: "pom", ageDays: 30},
		{artifact: "a", version: "3.0", ext: "jar", ageDays: 20},
		{artifact: "a", version: "4.0", ext: "jar", ageDays: 10},
		{artifact: "b", version: "1.0", ext: "jar", ageDays: 50},
	}

	cases := []struct {
		name      string
		keepLast  *int
		keepSince *int
		pattern   string
		// gone are the "artifact/version/ext" rows that must be removed.
		gone []string
	}{
		{
			name: "keep last 2 versions counts jar and pom as one version", keepLast: new(2),
			gone: []string{"a/1.0/jar", "a/1.0/pom", "a/2.0/jar", "a/2.0/pom"},
		},
		{
			name: "keep last 1 ranks each artifact on its own", keepLast: new(1),
			gone: []string{"a/1.0/jar", "a/1.0/pom", "a/2.0/jar", "a/2.0/pom", "a/3.0/jar"},
		},
		{
			name: "keep since 25 days removes only the older", keepSince: new(25),
			gone: []string{"a/1.0/jar", "a/1.0/pom", "a/2.0/jar", "a/2.0/pom", "b/1.0/jar"},
		},
		{
			name: "keep since beyond the timestamp range keeps everything", keepSince: new(math.MaxInt32),
			gone: nil,
		},
		{
			name: "both criteria: a row is kept when either protects it", keepLast: new(4), keepSince: new(25),
			gone: nil,
		},
		{
			name: "both criteria keep only what is outside the newest and older", keepLast: new(1), keepSince: new(35),
			gone: []string{"a/1.0/jar", "a/1.0/pom"},
		},
		{
			name: "name pattern restricts the selection", keepLast: new(1), pattern: `^g:b$`,
			gone: nil,
		},
		{
			name: "name pattern selects the artifact", keepSince: new(1), pattern: `^g:b$`,
			gone: []string{"b/1.0/jar"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s.t = t

			repoID := s.repo("sel-" + strconv.FormatInt(time.Now().UnixNano(), 10))
			ids := map[string]string{}

			for _, r := range rows {
				id, _ := s.mvn(repoID, r)
				ids[r.artifact+"/"+r.version+"/"+r.ext] = id
			}

			var want []string
			for _, g := range tc.gone {
				want = append(want, ids[g])
			}

			slices.Sort(want)

			p := s.policy(repoID, tc.keepLast, tc.keepSince, tc.pattern)

			prev := s.preview(p)
			if got := previewIDs(prev); !slices.Equal(got, want) || prev.Rows != len(want) {
				t.Fatalf("preview = %v (rows %d), want %v", got, prev.Rows, want)
			}

			// The preview removed nothing.
			if n := s.count(`SELECT COUNT(*) FROM package_files WHERE repository_id = $1`, repoID); n != len(rows) {
				t.Fatalf("preview changed the table: %d rows, want %d", n, len(rows))
			}

			deleted, err := s.w.runPolicy(ctx, p)
			if err != nil || deleted != len(want) {
				t.Fatalf("runPolicy = %d, %v; want %d", deleted, err, len(want))
			}

			var left []string

			for key, id := range ids {
				if s.rowExists(id) == slices.Contains(want, id) {
					left = append(left, key)
				}
			}

			if len(left) != 0 {
				t.Errorf("run and preview disagree on %v", left)
			}
		})
	}
}

// TestCleanupPolicyRanksArchitecturesSeparately: a newer build for one
// architecture must not evict the only build of another.
func TestCleanupPolicyRanksArchitecturesSeparately(t *testing.T) {
	s := newCleanupSeed(t)

	repo := &models.Repository{Name: "rpms", Format: "rpm", RepoType: "hosted", ProjectID: new(trashTestOrgID)}
	if err := s.pg.CreateRepository(context.Background(), repo); err != nil {
		t.Fatal(err)
	}

	add := func(version, arch string, ageDays int) string {
		s.seq++
		blobID, _ := s.blob(fmt.Sprintf("rpm %d", s.seq), time.Hour)

		var id string
		if err := s.pg.GetDB().QueryRowContext(context.Background(),
			`WITH p AS (INSERT INTO packages (repository_id, format, name, name_key) VALUES ($1, 'rpm', 'tool', 'tool')
			            ON CONFLICT (repository_id, name_key) DO UPDATE SET name = EXCLUDED.name RETURNING id),
			      v AS (INSERT INTO package_versions (package_id, repository_id, version, version_key, created_at)
			            SELECT p.id, $1, $3::text, $3::text || '|' || $4::text, now() - make_interval(days => $5) FROM p RETURNING id)
			 INSERT INTO package_files (version_id, repository_id, path, filename, blob_id, arch, created_at)
			 SELECT v.id, $1, 'tool-' || $3::text || '.' || $4::text || '.rpm', 'tool-' || $3::text || '.' || $4::text || '.rpm', $2, $4::text,
			        now() - make_interval(days => $5) FROM v RETURNING id`,
			repo.ID, blobID, version, arch, ageDays).Scan(&id); err != nil {
			t.Fatal(err)
		}

		return id
	}

	oldAMD, oldARM, newAMD := add("1", "x86_64", 30), add("1", "aarch64", 30), add("2", "x86_64", 1)

	deleted, err := s.w.runPolicy(context.Background(), s.policy(repo.ID, new(1), nil, ""))
	if err != nil || deleted != 1 {
		t.Fatalf("runPolicy = %d, %v; want 1", deleted, err)
	}

	live := func(id string) bool {
		return s.rowExists(id)
	}

	if live(oldAMD) || !live(oldARM) || !live(newAMD) {
		t.Errorf("live: old amd64 %v (want false), old arm64 %v, new amd64 %v (want true)", live(oldAMD), live(oldARM), live(newAMD))
	}
}

// TestCleanupPolicyRefcountsSharedBlobs: bytes leave storage with the last row
// that references them, not with the first row a policy removes.
func TestCleanupPolicyRefcountsSharedBlobs(t *testing.T) {
	s := newCleanupSeed(t)
	ctx := context.Background()

	repoA, repoB := s.repo("rc-a"), s.repo("rc-b")

	sharedID, sharedSHA := s.blob("shared by both repositories", 2*time.Hour)
	ownID, ownSHA := s.blob("only the old row uses it", 2*time.Hour)

	// Distinct coordinates and ages: keep the newest of repo A's two rows.
	oldA := s.mavenFile(repoA, ownID, mvnRow{artifact: "x", version: "1", ext: "jar", ageDays: 30})
	newA := s.mavenFile(repoA, sharedID, mvnRow{artifact: "x", version: "2", ext: "jar"})
	sharedB := s.mavenFile(repoB, sharedID, mvnRow{artifact: "x", version: "3", ext: "jar", ageDays: 30})

	deleted, err := s.w.runPolicy(ctx, s.policy(repoA, new(1), nil, ""))
	if err != nil || deleted != 1 {
		t.Fatalf("runPolicy = %d, %v; want 1", deleted, err)
	}

	if s.rowExists(oldA) || !s.rowExists(newA) || !s.rowExists(sharedB) {
		t.Fatal("wrong rows removed")
	}

	if s.storedBlob(ownSHA) || s.blobRowExists(ownSHA) {
		t.Error("blob of the removed row was not freed")
	}

	// Now the shared blob's rows go, one repository at a time.
	if _, err := s.w.runPolicy(ctx, s.policy(repoB, nil, new(1), "")); err != nil {
		t.Fatal(err)
	}

	if !s.storedBlob(sharedSHA) || !s.blobRowExists(sharedSHA) {
		t.Error("shared blob freed while repository A still references it")
	}

	s.exec(`UPDATE package_files SET created_at = now() - interval '30 days' WHERE id = $1`, newA)

	if _, err := s.w.runPolicy(ctx, s.policy(repoA, nil, new(1), "")); err != nil {
		t.Fatal(err)
	}

	if s.storedBlob(sharedSHA) || s.blobRowExists(sharedSHA) {
		t.Error("shared blob survived the removal of its last row")
	}
}

// TestCleanupPolicyProtections: release bundles, quarantine, immutable
// releases and the newest snapshot build keep rows a policy selects, in both
// the preview and the run.
func TestCleanupPolicyProtections(t *testing.T) {
	s := newCleanupSeed(t)
	ctx := context.Background()

	repoID := s.repo("protect")

	var (
		bundled, quarantined, released, snapOld, snapNew, plain string
		bundledSHA                                              string
	)

	bundled, bundledSHA = s.mvn(repoID, mvnRow{artifact: "bundled", version: "1.0-SNAPSHOT", ext: "jar", ageDays: 90})
	quarantined, _ = s.mvn(repoID, mvnRow{artifact: "quar", version: "1.0-SNAPSHOT", ext: "jar", ageDays: 90})
	released, _ = s.mvn(repoID, mvnRow{artifact: "rel", version: "1.0", ext: "jar", ageDays: 90})
	snapOld, _ = s.mvn(
		repoID,
		mvnRow{artifact: "snap", version: "2.0-SNAPSHOT", ext: "jar", ageDays: 90, snapTS: "20200101.000000", snapBuild: 1},
	)
	snapNew, _ = s.mvn(
		repoID,
		mvnRow{artifact: "snap", version: "2.0-SNAPSHOT", ext: "jar", ageDays: 80, snapTS: "20200102.000000", snapBuild: 2},
	)
	plain, _ = s.mvn(repoID, mvnRow{artifact: "plain", version: "1.0-SNAPSHOT", ext: "jar", ageDays: 90})

	s.exec(`UPDATE repositories SET immutable_releases = true WHERE id = $1`, repoID)

	var bundleID string
	if err := s.pg.GetDB().
		QueryRowContext(ctx, `INSERT INTO release_bundles (name, version) VALUES ('b', '1') RETURNING id`).
		Scan(&bundleID); err != nil {
		t.Fatal(err)
	}

	s.exec(`INSERT INTO release_bundle_items (bundle_id, source_repo_id, sha256, artifact_path, format) VALUES ($1, $2, $3, 'p', 'maven')`,
		bundleID, repoID, bundledSHA)

	var refID, jobID string
	if err := s.pg.GetDB().QueryRowContext(ctx,
		`INSERT INTO artifact_refs (repository_id, artifact_path, format) VALUES ($1, $2, 'maven') RETURNING id`, repoID, quarantined).
		Scan(&refID); err != nil {
		t.Fatal(err)
	}

	if err := s.pg.GetDB().QueryRowContext(ctx,
		`INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status) VALUES ($1, 'vuln', 'osv', 'completed') RETURNING id`, refID).
		Scan(&jobID); err != nil {
		t.Fatal(err)
	}

	s.exec(`INSERT INTO vuln_results (scan_job_id, quarantined) VALUES ($1, true)`, jobID)

	p := s.policy(repoID, nil, new(30), "")

	prev := s.preview(p)
	if got, want := previewIDs(prev), sorted(snapOld, plain); !slices.Equal(got, want) {
		t.Fatalf("preview = %v, want %v (bundled, quarantined, released and the newest snapshot kept)", got, want)
	}

	if deleted, err := s.w.runPolicy(ctx, p); err != nil || deleted != 2 {
		t.Fatalf("runPolicy = %d, %v; want 2", deleted, err)
	}

	for name, id := range map[string]string{"bundled": bundled, "quarantined": quarantined, "released": released, "newest snapshot": snapNew} {
		if !s.rowExists(id) {
			t.Errorf("%s row was removed", name)
		}
	}

	if s.rowExists(snapOld) || s.rowExists(plain) {
		t.Error("unprotected rows survived")
	}

	if !s.storedBlob(bundledSHA) {
		t.Error("the bundled blob left storage")
	}
}

func sorted(ids ...string) []string {
	slices.Sort(ids)

	return ids
}

// TestCleanupPolicyPreviewIsBounded: the preview lists at most
// CleanupPreviewLimit rows but still reports how many a run would remove.
func TestCleanupPolicyPreviewIsBounded(t *testing.T) {
	s := newCleanupSeed(t)

	repoID := s.repo("bulk")
	blobID, _ := s.blob("bulk", time.Hour)

	extra := 5
	total := CleanupPreviewLimit + extra

	s.exec(`WITH p AS (INSERT INTO packages (repository_id, format, name, name_key)
	                   VALUES ($1, 'maven', 'g:bulk', 'g:bulk') RETURNING id),
	             v AS (INSERT INTO package_versions (package_id, repository_id, version, version_key, created_at)
	                   SELECT p.id, $1, 'v' || n, 'v' || n, now() - make_interval(days => 100 + n)
	                   FROM p, generate_series(1, $3::int) n RETURNING id, version, created_at)
	       INSERT INTO package_files (version_id, repository_id, path, filename, blob_id, part, created_at)
	       SELECT v.id, $1, 'g/bulk/' || v.version || '.jar', 'g/bulk/' || v.version || '.jar', $2, 'false', v.created_at FROM v`,
		repoID, blobID, total)

	prev := s.preview(s.policy(repoID, new(1), nil, ""))
	if prev.Rows != total-1 || len(prev.Items) != CleanupPreviewLimit || !prev.Truncated {
		t.Fatalf(
			"preview rows=%d items=%d truncated=%v; want %d, %d, true",
			prev.Rows,
			len(prev.Items),
			prev.Truncated,
			total-1,
			CleanupPreviewLimit,
		)
	}

	prev = s.preview(s.policy(repoID, new(total), nil, ""))
	if prev.Rows != 0 || prev.Truncated || len(prev.Items) != 0 {
		t.Fatalf("keeping every version previewed %+v", prev)
	}
}

// TestCleanupTriggerPolicy: manual runs are exclusive per policy, report an
// unknown policy and record the outcome, including the failure of a policy
// that cannot run safely.
func TestCleanupTriggerPolicy(t *testing.T) {
	s := newCleanupSeed(t)
	ctx := context.Background()

	repoID := s.repo("trigger")
	old, _ := s.mvn(repoID, mvnRow{artifact: "a", version: "1", ext: "jar", ageDays: 10})
	s.mvn(repoID, mvnRow{artifact: "a", version: "2", ext: "jar", ageDays: 1})

	p := s.policy(repoID, new(1), nil, "")

	if err := s.w.TriggerPolicy(ctx, "00000000-0000-0000-0000-0000000000ff"); !errors.Is(err, ErrPolicyNotFound) {
		t.Fatalf("TriggerPolicy(unknown) = %v, want ErrPolicyNotFound", err)
	}

	hold, err := s.w.acquirePolicyLock(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.w.TriggerPolicy(ctx, p.ID); !errors.Is(err, ErrPolicyBusy) {
		t.Fatalf("TriggerPolicy while running elsewhere = %v, want ErrPolicyBusy", err)
	}

	if !s.rowExists(old) {
		t.Fatal("a busy policy deleted rows")
	}

	hold()

	if err := s.w.TriggerPolicy(ctx, p.ID); err != nil {
		t.Fatalf("TriggerPolicy after release = %v", err)
	}

	got := s.waitRun(p.ID)
	if got.LastRunError != "" || got.LastRunDeleted == nil || *got.LastRunDeleted != 1 || s.rowExists(old) {
		t.Fatalf("outcome = %+v, old row live %v; want 1 deleted, no error", got, s.rowExists(old))
	}

	// A pattern that does not compile is refused before anything is selected.
	bad := &models.CleanupPolicy{RepositoryID: repoID, Name: "bad", KeepSinceDays: new(1), NamePattern: "("}
	if err := s.pg.CreateCleanupPolicy(ctx, bad); err != nil {
		t.Skipf("the database refuses to store an invalid pattern: %v", err)
	}

	if err := s.w.TriggerPolicy(ctx, bad.ID); err != nil {
		t.Fatal(err)
	}

	got = s.waitRun(bad.ID)
	if got.LastRunError == "" || got.LastRunDeleted == nil || *got.LastRunDeleted != 0 {
		t.Fatalf("invalid policy outcome = %+v, want an error and 0 deleted", got)
	}

	if n := s.count(`SELECT COUNT(*) FROM package_files WHERE repository_id = $1`, repoID); n != 1 {
		t.Fatalf("%d rows left, an invalid policy must not delete", n)
	}
}

func (s *cleanupSeed) waitRun(policyID string) *models.CleanupPolicy {
	s.t.Helper()

	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		got, err := s.pg.GetCleanupPolicy(context.Background(), policyID)
		if err != nil || got == nil {
			s.t.Fatalf("GetCleanupPolicy = %v, %v", got, err)
		}

		if got.LastRunAt != nil {
			return got
		}

		time.Sleep(20 * time.Millisecond)
	}

	s.t.Fatal("policy run was not recorded within 30s")

	return nil
}

// TestCleanupCycleRunsOnlyDuePolicies: the scheduler runs an enabled policy
// whose schedule fired, skips a disabled one and one that ran recently.
func TestCleanupCycleRunsOnlyDuePolicies(t *testing.T) {
	s := newCleanupSeed(t)
	ctx := context.Background()

	mk := func(name string) (*models.CleanupPolicy, string) {
		repoID := s.repo(name)
		old, _ := s.mvn(repoID, mvnRow{artifact: "a", version: "1", ext: "jar", ageDays: 10})
		s.mvn(repoID, mvnRow{artifact: "a", version: "2", ext: "jar", ageDays: 1})

		return s.policy(repoID, new(1), nil, ""), old
	}

	due, dueRow := mk("due")
	disabled, disabledRow := mk("disabled")
	recent, recentRow := mk("recent")

	s.exec(`UPDATE cleanup_policies SET created_at = now() - interval '3 days'`)
	s.exec(`UPDATE cleanup_policies SET enabled = false WHERE id = $1`, disabled.ID)
	s.exec(`UPDATE cleanup_policies SET last_run_at = now() - interval '1 hour' WHERE id = $1`, recent.ID)

	// Orphan collection and quota reconcile are throttled out of this cycle.
	s.w.lastOrphanGC, s.w.lastQuotaReconcileAt = time.Now(), time.Now()

	s.w.runCleanupCycle(ctx)

	if s.rowExists(dueRow) {
		t.Error("the due policy did not run")
	}

	if !s.rowExists(disabledRow) || !s.rowExists(recentRow) {
		t.Error("a disabled or recently run policy ran")
	}

	if got := s.waitRun(due.ID); got.LastRunDeleted == nil || *got.LastRunDeleted != 1 {
		t.Errorf("due policy recorded %+v", got)
	}

	// The next cycle finds it not due any more.
	again, _ := s.mvn(due.RepositoryID, mvnRow{artifact: "a", version: "0", ext: "jar", ageDays: 50})
	s.w.runCleanupCycle(ctx)

	if !s.rowExists(again) {
		t.Error("a policy that just ran a second time")
	}
}

// npmVersion writes one version (with one tarball) of the package pkg of repoID.
func (s *cleanupSeed) npmVersion(repoID, pkg, version string, ageDays int) string {
	s.t.Helper()

	s.seq++

	blobID, _ := s.blob(fmt.Sprintf("npm %d", s.seq), time.Hour)

	var id string
	if err := s.pg.GetDB().QueryRowContext(context.Background(),
		`WITH p AS (INSERT INTO packages (repository_id, format, name, name_key) VALUES ($1, 'npm', $2::text, $2::text)
		            ON CONFLICT (repository_id, name_key) DO UPDATE SET name = EXCLUDED.name RETURNING id),
		      v AS (INSERT INTO package_versions (package_id, repository_id, version, version_key, created_at)
		            SELECT p.id, $1, $3::text, $3::text, now() - make_interval(days => $5) FROM p RETURNING id)
		 INSERT INTO package_files (version_id, repository_id, path, filename, blob_id, created_at)
		 SELECT v.id, $1, $2::text || '/-/' || $3::text || '.tgz', $3::text || '.tgz', $4, now() - make_interval(days => $5) FROM v RETURNING id`,
		repoID, pkg, version, blobID, ageDays).Scan(&id); err != nil {
		s.t.Fatalf("insert npm version: %v", err)
	}

	return id
}

func (s *cleanupSeed) formatRepo(name, format string) *models.Repository {
	s.t.Helper()

	repo := &models.Repository{Name: name, Format: format, RepoType: "hosted", ProjectID: new(trashTestOrgID)}
	if err := s.pg.CreateRepository(context.Background(), repo); err != nil {
		s.t.Fatalf("CreateRepository(%s): %v", name, err)
	}

	return repo
}

// A version a dist-tag points at is never removed by a policy, however old.
func TestCleanupPolicyNPMProtectsTaggedVersions(t *testing.T) {
	s := newCleanupSeed(t)

	repo := s.formatRepo("npm-protect", "npm")
	tagged := s.npmVersion(repo.ID, "left-pad", "1.0.0", 30)
	older := s.npmVersion(repo.ID, "left-pad", "1.1.0", 20)
	newest := s.npmVersion(repo.ID, "left-pad", "1.2.0", 10)

	s.exec(
		`INSERT INTO package_tags (package_id, tag, version) SELECT id, 'stable', '1.0.0' FROM packages WHERE repository_id = $1`,
		repo.ID,
	)

	p := s.policy(repo.ID, new(1), nil, "")
	if got, want := previewIDs(s.preview(p)), []string{older}; !slices.Equal(got, want) {
		t.Fatalf("preview = %v, want only the untagged old version %v", got, want)
	}

	if deleted, err := s.w.runPolicy(context.Background(), p); err != nil || deleted != 1 {
		t.Fatalf("runPolicy = %d, %v; want 1", deleted, err)
	}

	if s.rowExists(older) || !s.rowExists(tagged) || !s.rowExists(newest) {
		t.Errorf(
			"rows: older live %v (want false), tagged %v, newest %v (want true)",
			s.rowExists(older),
			s.rowExists(tagged),
			s.rowExists(newest),
		)
	}
}

// An explicit delete of an npm version drops the dist-tags that pointed at it
// (the descriptor's OnPurge statement), because the repository keeps no trash.
func TestDeletePackageDropsNPMTags(t *testing.T) {
	s := newCleanupSeed(t)

	repo := s.formatRepo("npm-delete", "npm")
	gone := s.npmVersion(repo.ID, "left-pad", "1.0.0", 5)
	kept := s.npmVersion(repo.ID, "left-pad", "2.0.0", 1)

	s.exec(`INSERT INTO package_tags (package_id, tag, version) SELECT id, 'old', '1.0.0' FROM packages WHERE repository_id = $1`, repo.ID)
	s.exec(
		`INSERT INTO package_tags (package_id, tag, version) SELECT id, 'latest', '2.0.0' FROM packages WHERE repository_id = $1`,
		repo.ID,
	)

	res, err := s.w.DeletePackage(context.Background(), repo, gone, nil, "alice")
	if err != nil || res.Trashed || res.Rows != 1 || res.Name != "left-pad" || res.Version != "1.0.0" {
		t.Fatalf("DeletePackage = %+v, %v; want one row removed for good", res, err)
	}

	if s.rowExists(gone) || !s.rowExists(kept) {
		t.Fatal("wrong version removed")
	}

	if n := s.count(`SELECT COUNT(*) FROM package_tags WHERE tag = 'old'`); n != 0 {
		t.Errorf("the dist-tag of the deleted version survived")
	}

	if n := s.count(`SELECT COUNT(*) FROM package_tags WHERE tag = 'latest'`); n != 1 {
		t.Errorf("the dist-tag of the kept version was dropped")
	}

	if _, err := s.w.DeletePackage(context.Background(), repo, gone, nil, "alice"); !errors.Is(err, ErrPackageNotFound) {
		t.Errorf("deleting a removed row = %v, want ErrPackageNotFound", err)
	}
}

// Docker is the one special case of the engine: tags of an image rank by their
// last update, per image, and a tag removal leaves the other images alone.
func TestCleanupPolicyDockerTags(t *testing.T) {
	s := newCleanupSeed(t)

	repo := s.formatRepo("docker-clean", "docker")
	blobID, _ := s.blob("manifest", time.Hour)

	tag := func(image, name string, ageDays int) string {
		s.t.Helper()

		digest := fmt.Sprintf("sha256:%s-%s", image, name)
		s.exec(`INSERT INTO docker_manifests (repository_id, image, digest, media_type, blob_id)
		        VALUES ($1, $2, $3, 'application/vnd.oci.image.manifest.v1+json', $4) ON CONFLICT DO NOTHING`, repo.ID, image, digest, blobID)

		var id string
		if err := s.pg.GetDB().QueryRowContext(context.Background(),
			`INSERT INTO docker_tags (repository_id, image, name, manifest_digest, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, now() - make_interval(days => $5), now() - make_interval(days => $5)) RETURNING id`,
			repo.ID, image, name, digest, ageDays).Scan(&id); err != nil {
			s.t.Fatalf("insert tag: %v", err)
		}

		return id
	}

	appOld, appNew, otherOnly := tag("app", "v1", 30), tag("app", "v2", 1), tag("other", "v1", 60)

	deleted, err := s.w.runPolicy(context.Background(), s.policy(repo.ID, new(1), nil, ""))
	if err != nil || deleted != 1 {
		t.Fatalf("runPolicy = %d, %v; want 1", deleted, err)
	}

	live := func(id string) bool {
		return s.count(`SELECT COUNT(*) FROM docker_tags WHERE id = $1`, id) == 1
	}

	if live(appOld) || !live(appNew) || !live(otherOnly) {
		t.Errorf("tags live: old app %v (want false), new app %v, other %v (want true)", live(appOld), live(appNew), live(otherOnly))
	}
}
