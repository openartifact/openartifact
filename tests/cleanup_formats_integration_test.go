// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/worker"
)

// fmtRow is one seeded package row: its id, the blob it owns and its size.
type fmtRow struct {
	id, sha string
	size    int64
}

// seedFormat inserts one version of a package of any cleanup format the way an
// upload leaves it (blob in the store, row, quota reservation), aged by
// ageDays. Formats with several rows per version take a unique tag in name.
func (e *cleanupQuotaEnv) seedFormat(repoID, format, name, version string, ageDays int) fmtRow {
	e.t.Helper()

	blobID, sha := e.blob(10)
	ts := time.Now().Add(-time.Duration(ageDays) * 24 * time.Hour)
	e.seq++
	uniq := fmt.Sprintf("%s-%s-%d", name, version, e.seq)

	var id string

	switch format {
	case "npm":
		pkg := e.queryID(`INSERT INTO npm_packages (repository_id, name, full_name) VALUES ($1, $2, $2)
			ON CONFLICT (repository_id, full_name) DO UPDATE SET name = EXCLUDED.name RETURNING id`, repoID, name)
		id = e.queryID(`INSERT INTO npm_versions (package_id, version, manifest_json, integrity, shasum, tarball_filename, blob_id, created_at, updated_at)
			VALUES ($1, $2, '{}'::jsonb, 'sha512-x', 'abc', $3, $4, $5, $5) RETURNING id`, pkg, version, uniq+".tgz", blobID, ts)
	case "maven":
		id = e.queryID(`INSERT INTO maven_artifacts (repository_id, group_id, artifact_id_coord, version, extension, repo_path, blob_id, created_at, updated_at)
			VALUES ($1, 'org.acme', $2, $3, 'jar', $4, $5, $6, $6) RETURNING id`, repoID, name, version, uniq+".jar", blobID, ts)
	case "ivy":
		id = e.queryID(`INSERT INTO ivy_artifacts (repository_id, organisation, module, revision, artifact_name, artifact_ext, blob_id, created_at, updated_at)
			VALUES ($1, 'org.acme', $2, $3, $2, 'jar', $4, $5, $5) RETURNING id`, repoID, name, version, blobID, ts)
	case "sbt":
		id = e.queryID(`INSERT INTO sbt_artifacts (repository_id, group_id, artifact_id, version, scala_version, filename, blob_id, created_at, updated_at)
			VALUES ($1, 'org.acme', $2, $3, '2.13', $4, $5, $6, $6) RETURNING id`, repoID, name, version, uniq+".jar", blobID, ts)
	case "cargo":
		id = e.queryID(`INSERT INTO cargo_crates (repository_id, name, version, blob_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $5) RETURNING id`, repoID, name, version, blobID, ts)
	case "rubygems":
		id = e.queryID(`INSERT INTO rubygems_gems (repository_id, name, version, platform, blob_id, size, created_at, updated_at)
			VALUES ($1, $2, $3, 'ruby', $4, 10, $5, $5) RETURNING id`, repoID, name, version, blobID, ts)
	case "conda":
		id = e.queryID(`INSERT INTO conda_packages (repository_id, name, version, build_string, platform, blob_id, created_at, updated_at)
			VALUES ($1, $2, $3, 'h0_0', 'linux-64', $4, $5, $5) RETURNING id`, repoID, name, version, blobID, ts)
	case "conan":
		id = e.queryID(`INSERT INTO conan_packages (repository_id, name, version, blob_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $5) RETURNING id`, repoID, name, version, blobID, ts)
	case "raw":
		id = e.queryID(`INSERT INTO raw_artifacts (repository_id, path, blob_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4) RETURNING id`, repoID, "dir/"+name+"-"+version+".bin", blobID, ts)
	case "docker":
		digest := fmt.Sprintf("sha256:%064d", e.seq)
		e.exec(`INSERT INTO docker_manifests (repository_id, digest, media_type, size, blob_id) VALUES ($1, $2, 'm', 10, $3)`, repoID, digest, blobID)
		id = e.queryID(`INSERT INTO docker_tags (repository_id, name, manifest_digest, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4) RETURNING id`, repoID, version, digest, ts)
	default:
		e.t.Fatalf("format %q is not seeded", format)
	}

	var size int64
	if format != "docker" {
		size = e.reserve(repoID, blobID)
	}

	return fmtRow{id: id, sha: sha, size: size}
}

func (e *cleanupQuotaEnv) reserve(repoID, blobID string) int64 {
	e.t.Helper()

	var size int64
	if err := e.pg.GetDB().QueryRowContext(context.Background(), `SELECT size_bytes FROM blobs WHERE id = $1`, blobID).Scan(&size); err != nil {
		e.t.Fatalf("blob size: %v", err)
	}

	if ok, err := e.pg.ReserveStorageUsage(context.Background(), repoID, size, 1); err != nil || !ok {
		e.t.Fatalf("ReserveStorageUsage = %v, %v", ok, err)
	}

	return size
}

// rowTable is the table (and live-row filter) holding the rows of a format.
func rowTable(format string) string {
	return map[string]string{
		"npm": "npm_versions", "maven": "maven_artifacts", "ivy": "ivy_artifacts", "sbt": "sbt_artifacts",
		"cargo": "cargo_crates", "rubygems": "rubygems_gems", "conda": "conda_packages", "conan": "conan_packages",
		"raw": "raw_artifacts", "docker": "docker_tags",
	}[format]
}

func (e *cleanupQuotaEnv) live(format, id string) bool {
	e.t.Helper()

	return e.countRows(`SELECT count(*) FROM `+rowTable(format)+` WHERE id = $1 AND deleted_at IS NULL`, id) == 1
}

func (e *cleanupQuotaEnv) gone(format, id string) bool {
	e.t.Helper()

	return e.countRows(`SELECT count(*) FROM `+rowTable(format)+` WHERE id = $1`, id) == 0
}

func requireRun(t *testing.T, got *models.CleanupPolicy, deleted int) {
	t.Helper()

	if got.LastRunError != "" || got.LastRunDeleted == nil || *got.LastRunDeleted != deleted {
		t.Fatalf("policy run: deleted = %v, error = %q; want %d deleted", got.LastRunDeleted, got.LastRunError, deleted)
	}
}

type recordingNotifiers struct {
	mu     sync.Mutex
	maven  []string
	npm    []string
	gcRuns int
}

type mavenRec struct{ r *recordingNotifiers }

func (m mavenRec) NotifyStale(_, group, artifact string) {
	m.r.mu.Lock()
	defer m.r.mu.Unlock()

	m.r.maven = append(m.r.maven, group+":"+artifact)
}

type npmRec struct{ r *recordingNotifiers }

func (n npmRec) NotifyStale(pkg string) {
	n.r.mu.Lock()
	defer n.r.mu.Unlock()

	n.r.npm = append(n.r.npm, pkg)
}

type gcRec struct{ r *recordingNotifiers }

func (g gcRec) RunRepo(_ context.Context, _ *models.Repository, _ worker.DockerGCOptions) (*worker.DockerGCReport, error) {
	g.r.mu.Lock()
	defer g.r.mu.Unlock()

	g.r.gcRuns++

	return &worker.DockerGCReport{}, nil
}

func (g gcRec) DefaultGrace() time.Duration { return 0 }

func (e *cleanupQuotaEnv) notifiers() *recordingNotifiers {
	r := &recordingNotifiers{}
	e.worker.SetRegenNotifiers(mavenRec{r}, npmRec{r}, gcRec{r})

	return r
}

// Every format keeps the newest version, removes the older ones with their
// blobs, and hands their quota usage back.
func TestCleanupPolicyEveryFormatKeepsNewest(t *testing.T) {
	for _, format := range []string{"npm", "maven", "ivy", "sbt", "cargo", "rubygems", "conda", "conan", "raw", "docker"} {
		t.Run(format, func(t *testing.T) {
			e := newCleanupQuotaEnv(t)
			e.notifiers()

			repoID := e.repo(format)
			oldest := e.seedFormat(repoID, format, "pkg", "1.0.0", 30)
			middle := e.seedFormat(repoID, format, "pkg", "1.1.0", 20)
			newest := e.seedFormat(repoID, format, "pkg", "1.2.0", 10)

			requireRun(t, e.runPolicy(repoID, 1), 2)

			if !e.live(format, newest.id) || !e.blobExists(newest.sha) {
				t.Error("the newest version was removed")
			}

			for _, r := range []fmtRow{oldest, middle} {
				if !e.gone(format, r.id) {
					t.Errorf("old version %s is still there", r.id)
				}

				if format != "docker" && e.blobExists(r.sha) {
					t.Errorf("blob of old version %s is still in storage", r.sha[:12])
				}
			}

			if format == "docker" {
				return
			}

			if size, count := e.usage(repoID); size != newest.size || count != 1 {
				t.Errorf("usage = %d / %d, want %d / 1", size, count, newest.size)
			}
		})
	}
}

// keep_since_days removes only what is older than the window.
func TestCleanupPolicyKeepSinceDays(t *testing.T) {
	e := newCleanupQuotaEnv(t)
	repoID := e.repo("cargo")
	old := e.seedFormat(repoID, "cargo", "serde", "1.0.0", 40)
	recent := e.seedFormat(repoID, "cargo", "serde", "1.0.1", 2)

	days := 7
	policy := &models.CleanupPolicy{RepositoryID: repoID, Name: "age", KeepSinceDays: &days}

	if err := e.pg.CreateCleanupPolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}

	prev, err := e.worker.PreviewPolicy(context.Background(), policy.ID)
	if err != nil {
		t.Fatalf("PreviewPolicy: %v", err)
	}

	if prev.Rows != 1 || len(prev.Items) != 1 || prev.Items[0].ID != old.id || prev.Items[0].Version != "1.0.0" {
		t.Fatalf("preview = %+v, want only the old crate", prev)
	}

	// A dry run changes nothing.
	if !e.live("cargo", old.id) || !e.blobExists(old.sha) {
		t.Fatal("PreviewPolicy removed a row")
	}

	if err := e.worker.TriggerPolicy(context.Background(), policy.ID); err != nil {
		t.Fatal(err)
	}

	e.waitRun(policy.ID)

	if !e.gone("cargo", old.id) || !e.live("cargo", recent.id) {
		t.Error("keep_since_days removed the wrong crate")
	}
}

func (e *cleanupQuotaEnv) waitRun(policyID string) {
	e.t.Helper()

	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		got, err := e.pg.GetCleanupPolicy(context.Background(), policyID)
		if err == nil && got != nil && got.LastRunAt != nil {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	e.t.Fatal("cleanup policy did not finish within 30s")
}

// A version an npm dist-tag points at is kept, and the packument is refreshed
// for the package that lost versions.
func TestCleanupPolicyNPMKeepsDistTaggedVersions(t *testing.T) {
	e := newCleanupQuotaEnv(t)
	rec := e.notifiers()
	repoID := e.repo("npm")

	tagged := e.seedFormat(repoID, "npm", "left-pad", "1.0.0", 30)
	middle := e.seedFormat(repoID, "npm", "left-pad", "1.1.0", 20)
	e.seedFormat(repoID, "npm", "left-pad", "1.2.0", 10)

	pkg := e.queryID(`SELECT package_id FROM npm_versions WHERE id = $1`, tagged.id)
	e.exec(`INSERT INTO npm_dist_tags (package_id, tag, version) VALUES ($1, 'stable', '1.0.0')`, pkg)

	requireRun(t, e.runPolicy(repoID, 1), 1)

	if !e.live("npm", tagged.id) {
		t.Error("the version a dist-tag points at was removed")
	}

	if !e.gone("npm", middle.id) {
		t.Error("the untagged old version was kept")
	}

	if len(rec.npm) != 1 || rec.npm[0] != pkg {
		t.Errorf("npm packument refreshes = %v, want [%s]", rec.npm, pkg)
	}
}

// A package whose last version a policy removes is dropped, with its tags.
func TestCleanupPolicyNPMDropsEmptyPackage(t *testing.T) {
	e := newCleanupQuotaEnv(t)
	e.notifiers()
	repoID := e.repo("npm")

	e.seedFormat(repoID, "npm", "old-lib", "1.0.0", 30)
	e.seedFormat(repoID, "npm", "new-lib", "1.0.0", 1)

	days := 7
	policy := &models.CleanupPolicy{RepositoryID: repoID, Name: "age", KeepSinceDays: &days}

	if err := e.pg.CreateCleanupPolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}

	if err := e.worker.TriggerPolicy(context.Background(), policy.ID); err != nil {
		t.Fatal(err)
	}

	e.waitRun(policy.ID)

	if n := e.countRows(`SELECT count(*) FROM npm_packages WHERE repository_id = $1 AND full_name = 'old-lib'`, repoID); n != 0 {
		t.Errorf("empty package old-lib still exists")
	}

	if n := e.countRows(`SELECT count(*) FROM npm_packages WHERE repository_id = $1 AND full_name = 'new-lib'`, repoID); n != 1 {
		t.Errorf("package new-lib was removed")
	}
}

// Maven: releases and the builds of a snapshot rank separately, the newest
// snapshot build survives any policy, and the metadata of the artifact is
// regenerated.
func TestCleanupPolicyMavenSnapshotsAndMetadata(t *testing.T) {
	e := newCleanupQuotaEnv(t)
	rec := e.notifiers()
	repoID := e.repo("maven")

	r1 := e.seedFormat(repoID, "maven", "lib", "1.0", 30)
	r2 := e.seedFormat(repoID, "maven", "lib", "1.1", 10)

	snap := func(ts string, build, age int) fmtRow {
		row := e.seedFormat(repoID, "maven", "lib", "2.0-SNAPSHOT", age)
		e.exec(`UPDATE maven_artifacts SET is_snapshot = true, snapshot_timestamp = $2, snapshot_build_number = $3 WHERE id = $1`, row.id, ts, build)

		return row
	}

	s1 := snap("20260101.100000", 1, 30)
	s2 := snap("20260102.100000", 2, 20)
	s3 := snap("20260103.100000", 3, 10)

	// Keep the newest 2 releases of the artifact and the newest 2 builds of the
	// snapshot: the two rankings are independent, so only the oldest build goes.
	requireRun(t, e.runPolicy(repoID, 2), 1)

	if !e.live("maven", r1.id) || !e.live("maven", r2.id) {
		t.Error("releases were ranked against the snapshot builds")
	}

	if !e.gone("maven", s1.id) || !e.live("maven", s2.id) || !e.live("maven", s3.id) {
		t.Error("snapshot builds ranking is wrong")
	}

	if len(rec.maven) == 0 || rec.maven[0] != "org.acme:lib" {
		t.Errorf("maven metadata refreshes = %v, want org.acme:lib", rec.maven)
	}
}

func TestCleanupPolicyMavenKeepsNewestSnapshotBuild(t *testing.T) {
	e := newCleanupQuotaEnv(t)
	repoID := e.repo("maven")

	row := e.seedFormat(repoID, "maven", "lib", "3.0-SNAPSHOT", 400)
	e.exec(`UPDATE maven_artifacts SET is_snapshot = true, snapshot_timestamp = '20250101.100000', snapshot_build_number = 1 WHERE id = $1`, row.id)

	days := 30
	policy := &models.CleanupPolicy{RepositoryID: repoID, Name: "age", KeepSinceDays: &days}

	if err := e.pg.CreateCleanupPolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}

	prev, err := e.worker.PreviewPolicy(context.Background(), policy.ID)
	if err != nil {
		t.Fatal(err)
	}

	if prev.Rows != 0 {
		t.Fatalf("preview lists %d rows, want the newest snapshot build kept", prev.Rows)
	}
}

// Docker: a run removes the tags outside the keep rules and, since they were
// removed for good, collects the registry.
func TestCleanupPolicyDockerRemovesTagsAndCollects(t *testing.T) {
	e := newCleanupQuotaEnv(t)
	rec := e.notifiers()
	repoID := e.repo("docker")

	old := e.seedFormat(repoID, "docker", "app", "v1", 30)
	e.seedFormat(repoID, "docker", "app", "v2", 1)

	requireRun(t, e.runPolicy(repoID, 1), 1)

	if !e.gone("docker", old.id) {
		t.Error("old tag still exists")
	}

	if rec.gcRuns != 1 {
		t.Errorf("registry GC ran %d times, want 1", rec.gcRuns)
	}
}

// With a trash, removed rows wait for the purge: blobs, quota and the docker
// registry stay as they were.
func TestCleanupPolicyTrashesEveryFormat(t *testing.T) {
	for _, format := range []string{"npm", "maven", "cargo", "docker", "raw", "conan"} {
		t.Run(format, func(t *testing.T) {
			e := newCleanupQuotaEnv(t)
			e.pg.SetTrashRetentionDays(7)
			rec := e.notifiers()
			repoID := e.repo(format)

			old := e.seedFormat(repoID, format, "pkg", "1.0.0", 30)
			e.seedFormat(repoID, format, "pkg", "1.1.0", 1)

			requireRun(t, e.runPolicy(repoID, 1), 1)

			if e.live(format, old.id) || e.gone(format, old.id) {
				t.Error("the old row was not moved to the trash")
			}

			if !e.blobExists(old.sha) {
				t.Error("a trashed row lost its blob")
			}

			if rec.gcRuns != 0 {
				t.Error("registry GC ran for trashed tags")
			}
		})
	}
}

// Released versions of an immutable repository stay, quarantined rows stay.
func TestCleanupPolicyHonoursImmutableReleasesAndQuarantine(t *testing.T) {
	e := newCleanupQuotaEnv(t)
	repoID := e.repo("cargo")

	release := e.seedFormat(repoID, "cargo", "serde", "1.0.0", 30)
	pre := e.seedFormat(repoID, "cargo", "serde", "1.1.0-rc.1", 20)
	quarantined := e.seedFormat(repoID, "cargo", "serde", "1.1.1-rc.1", 15)
	e.seedFormat(repoID, "cargo", "serde", "2.0.0", 10)
	e.exec(`UPDATE repositories SET immutable_releases = true WHERE id = $1`, repoID)

	ref := e.queryID(`INSERT INTO artifact_refs (repository_id, artifact_path, format) VALUES ($1, $2, 'cargo') RETURNING id`, repoID, quarantined.id)
	job := e.queryID(`INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status) VALUES ($1, 'vuln', 'osv', 'completed') RETURNING id`, ref)
	e.exec(`INSERT INTO vuln_results (scan_job_id, quarantined) VALUES ($1, true)`, job)

	requireRun(t, e.runPolicy(repoID, 1), 1)

	if !e.live("cargo", release.id) {
		t.Error("a released version of an immutable repository was removed")
	}

	if !e.live("cargo", quarantined.id) {
		t.Error("a quarantined row was removed")
	}

	if !e.gone("cargo", pre.id) {
		t.Error("the plain pre-release was kept")
	}
}

// The explicit package delete removes any format's row the way a policy does,
// refuses unknown ids and released versions of an immutable repository.
func TestDeletePackageEveryFormat(t *testing.T) {
	ctx := context.Background()

	for _, format := range []string{"npm", "maven", "ivy", "sbt", "cargo", "rubygems", "conda", "conan", "raw", "docker"} {
		t.Run(format, func(t *testing.T) {
			e := newCleanupQuotaEnv(t)
			rec := e.notifiers()
			repoID := e.repo(format)
			repo, err := e.pg.GetRepositoryByID(ctx, repoID)
			if err != nil || repo == nil {
				t.Fatalf("GetRepositoryByID = %v, %v", repo, err)
			}

			a := e.seedFormat(repoID, format, "pkg", "1.0.0", 5)
			b := e.seedFormat(repoID, format, "pkg", "1.1.0", 4)

			if _, err := e.worker.DeletePackage(ctx, repo, "not-a-uuid", nil); !errors.Is(err, worker.ErrPackageNotFound) {
				t.Fatalf("bad id: err = %v, want ErrPackageNotFound", err)
			}

			res, err := e.worker.DeletePackage(ctx, repo, a.id, nil)
			if err != nil {
				t.Fatalf("DeletePackage: %v", err)
			}

			if res.Name == "" || res.Trashed {
				t.Errorf("result = %+v", res)
			}

			if !e.gone(format, a.id) || !e.live(format, b.id) {
				t.Error("wrong rows removed")
			}

			if format != "docker" && e.blobExists(a.sha) {
				t.Error("blob of the deleted package is still in storage")
			}

			if _, err := e.worker.DeletePackage(ctx, repo, a.id, nil); !errors.Is(err, worker.ErrPackageNotFound) {
				t.Fatalf("second delete: err = %v, want ErrPackageNotFound", err)
			}

			if format == "maven" && len(rec.maven) == 0 {
				t.Error("maven metadata was not refreshed")
			}
		})
	}
}

func TestDeletePackageGuardAndTrash(t *testing.T) {
	ctx := context.Background()
	e := newCleanupQuotaEnv(t)
	e.pg.SetTrashRetentionDays(7)
	repoID := e.repo("cargo")
	repo, _ := e.pg.GetRepositoryByID(ctx, repoID)

	a := e.seedFormat(repoID, "cargo", "serde", "1.0.0", 5)
	errGuard := errors.New("guarded")

	if _, err := e.worker.DeletePackage(ctx, repo, a.id, func(p worker.DeletedPackage) error {
		if p.Version != "1.0.0" || p.Name != "serde" {
			t.Errorf("guard got %+v", p)
		}

		return errGuard
	}); !errors.Is(err, errGuard) {
		t.Fatalf("err = %v, want the guard's", err)
	}

	if !e.live("cargo", a.id) {
		t.Fatal("a guarded delete removed the row")
	}

	res, err := e.worker.DeletePackage(ctx, repo, a.id, nil)
	if err != nil || !res.Trashed {
		t.Fatalf("DeletePackage = %+v, %v; want trashed", res, err)
	}

	if e.live("cargo", a.id) || e.gone("cargo", a.id) || !e.blobExists(a.sha) {
		t.Error("the row did not go to the trash with its blob")
	}
}

// An npm version deleted for good loses its dist-tags.
func TestDeletePackageNPMDropsDistTags(t *testing.T) {
	ctx := context.Background()
	e := newCleanupQuotaEnv(t)

	npmID := e.repo("npm")
	npmRepo, _ := e.pg.GetRepositoryByID(ctx, npmID)
	v := e.seedFormat(npmID, "npm", "lib", "1.0.0", 5)
	pkg := e.queryID(`SELECT package_id FROM npm_versions WHERE id = $1`, v.id)
	e.exec(`INSERT INTO npm_dist_tags (package_id, tag, version) VALUES ($1, 'latest', '1.0.0')`, pkg)

	if _, err := e.worker.DeletePackage(ctx, npmRepo, v.id, nil); err != nil {
		t.Fatal(err)
	}

	if n := e.countRows(`SELECT count(*) FROM npm_dist_tags WHERE package_id = $1`, pkg); n != 0 {
		t.Errorf("%d dist-tags left for a deleted version", n)
	}
}

// The dry run of a project default policy expands like a run: repositories with
// their own enabled policy are left out, others are listed, nothing is removed.
func TestPreviewProjectPolicyExpandsLikeARun(t *testing.T) {
	e := newCleanupQuotaEnv(t)
	ctx := context.Background()

	project := e.queryID(`INSERT INTO projects (id, name, slug) VALUES (gen_random_uuid(), 'prevproj', 'prevproj') RETURNING id`)
	plain := e.projectRepo(project, "plain", "cargo")
	custom := e.projectRepo(project, "custom", "cargo")

	oldPlain := e.seedFormat(plain, "cargo", "serde", "1.0.0", 30)
	e.seedFormat(plain, "cargo", "serde", "2.0.0", 1)
	e.seedFormat(custom, "cargo", "serde", "1.0.0", 30)
	e.seedFormat(custom, "cargo", "serde", "2.0.0", 1)

	own := 10
	if err := e.pg.CreateCleanupPolicy(ctx, &models.CleanupPolicy{RepositoryID: custom, Name: "own", Enabled: true, KeepLastN: &own}); err != nil {
		t.Fatal(err)
	}

	keep := 1
	def := &models.CleanupPolicy{ProjectID: project, Name: "default", Enabled: true, KeepLastN: &keep}

	if err := e.pg.CreateCleanupPolicy(ctx, def); err != nil {
		t.Fatal(err)
	}

	got, err := e.worker.PreviewProjectPolicy(ctx, def.ID)
	if err != nil {
		t.Fatalf("PreviewProjectPolicy: %v", err)
	}

	if len(got) != 1 || got[0].Repository != "plain" || got[0].Rows != 1 || got[0].Items[0].ID != oldPlain.id {
		t.Fatalf("preview = %+v, want only the old crate of repository plain", got)
	}

	if !e.live("cargo", oldPlain.id) {
		t.Error("the dry run removed a row")
	}

	if _, err := e.worker.PreviewPolicy(ctx, def.ID); !errors.Is(err, worker.ErrPolicyNotFound) {
		t.Errorf("PreviewPolicy on a project policy: err = %v, want ErrPolicyNotFound", err)
	}
}
