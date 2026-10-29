// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	formatlifecycle "github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/worker"
)

// fmtRow is one seeded package row: its id, the blob it owns and its size.
type fmtRow struct {
	id, sha string
	size    int64
}

// seedFormat writes one version of a package of any cleanup format the way an
// upload leaves it (blob in the store, quota reservation, generic rows), aged
// by ageDays. Formats with several rows per version take a unique tag in name.
// The returned id is the id of the listed file (the unit the trash and the
// cleanup address); docker keeps its own tables.
func (e *cleanupQuotaEnv) seedFormat(repoID, format, name, version string, ageDays int) fmtRow {
	e.t.Helper()

	blobID, sha := e.blob(10)
	ts := time.Now().Add(-time.Duration(ageDays) * 24 * time.Hour)
	e.seq++

	if format == "docker" {
		digest := fmt.Sprintf("sha256:%064d", e.seq)
		e.exec(
			`INSERT INTO docker_manifests (repository_id, digest, media_type, size, blob_id) VALUES ($1, $2, 'm', 10, $3)`,
			repoID,
			digest,
			blobID,
		)
		id := e.queryID(`INSERT INTO docker_tags (repository_id, name, manifest_digest, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4) RETURNING id`, repoID, version, digest, ts)

		return fmtRow{id: id, sha: sha}
	}

	// Reserve first, as an upload does: the file insert consumes the pending entry.
	size := e.reserve(repoID, blobID)
	id := e.writeGeneric(repoID, format, name, version, blobID, ts)

	return fmtRow{id: id, sha: sha, size: size}
}

// writeGeneric inserts the package, version and file rows of one file pointing
// at blobID, as the format's store writes them, and returns the file id. The
// quota reservation is the caller's: the file insert consumes it.
func (e *cleanupQuotaEnv) writeGeneric(repoID, format, name, version, blobID string, ts time.Time) string {
	e.t.Helper()

	e.seq++
	row := genericRowFor(format, name, version, e.seq)

	return e.queryID(
		`WITH p AS (
		   INSERT INTO packages (repository_id, format, name, name_key, attrs)
		   VALUES ($1, $2, $3, $3, $4::jsonb)
		   ON CONFLICT (repository_id, name_key) DO UPDATE SET name = EXCLUDED.name RETURNING id
		 ), v AS (
		   INSERT INTO package_versions (package_id, repository_id, version, version_key, metadata, created_at, updated_at)
		   SELECT p.id, $1, $5, $6, $7::jsonb, $11, $11 FROM p RETURNING id
		 )
		 INSERT INTO package_files (version_id, repository_id, path, filename, blob_id, arch, part, unit_key, created_at, updated_at)
		 SELECT v.id, $1, $8, $8, $9, $10, $12, $8, $11, $11 FROM v RETURNING id`,
		repoID, strings.TrimSuffix(format, "-provider"), row.name, row.attrs, version, row.versionKey, row.meta,
		row.path, blobID, row.arch, ts, row.part,
	)
}

// genericRow is the shape of one seeded file in the generic tables, as the
// format's store writes it (the per-format mapping specs).
type genericRow struct {
	name, attrs, versionKey, meta, path, arch, part string
}

func genericRowFor(format, name, version string, seq int) genericRow {
	uniq := fmt.Sprintf("%s-%s-%d", name, version, seq)
	r := genericRow{name: name, attrs: "{}", versionKey: version, meta: "{}", path: uniq + ".bin"}

	switch format {
	case "npm":
		r.attrs = fmt.Sprintf(`{"scope":"","name":%q}`, name)
		r.meta = `{"manifest_json":{}}`
		r.path = name + "/-/" + uniq + ".tgz"
	case "maven":
		r.name = "org.acme:" + name
		r.attrs = fmt.Sprintf(`{"group_id":"org.acme","artifact_id":%q}`, name)
		r.meta = `{"is_snapshot":false}`
		r.part = "false"
		r.path = "org/acme/" + uniq + ".jar"
	case "ivy":
		r.name = "org.acme:" + name
		r.path = "org.acme/" + name + "/" + version + "/" + uniq + ".jar"
	case "sbt":
		r.name = "org.acme:" + name
		r.path = "org.acme/" + name + "/" + version + "/" + uniq + ".jar"
		r.arch = "2.13"
	case "composer":
		r.name = "acme/" + name
	case "rubygems":
		r.versionKey = version + "|ruby"
		r.arch = "ruby"
	case "rpm":
		r.versionKey = version + "|x86_64"
		r.arch = "x86_64"
	case "deb":
		r.versionKey = version + "|amd64"
		r.arch = "amd64"
	case "helm":
		r.path = name + "-" + version + ".tgz"
	case "conda":
		r.versionKey = version + "|h0_0"
		r.arch = "linux-64"
	case "conan":
		r.versionKey = version + "#rev"
	case "apk", "pacman":
		r.versionKey = version + "|x86_64"
		r.arch = "x86_64"
	case "terraform":
		r.name = "modules/" + name + "/aws"
		r.attrs = fmt.Sprintf(`{"kind":"module","name":%q,"system":"aws"}`, name)
	case "terraform-provider":
		r.name = "providers/" + name
		r.attrs = fmt.Sprintf(`{"kind":"provider","type":%q}`, name)
		r.arch = "linux_amd64"
	case "raw":
		// One package per path; every raw file is a release with version ''.
		r.path = "dir/" + name + "-" + version + ".bin"
		r.name, r.versionKey = r.path, ""
	}

	if strings.HasSuffix(version, "-SNAPSHOT") {
		r.versionKey = version + "#" + strconv.Itoa(seq)
	}

	return r
}

// packageOf is the package id of a seeded file.
func (e *cleanupQuotaEnv) packageOf(fileID string) string {
	e.t.Helper()

	return e.queryID(`SELECT v.package_id FROM package_files f JOIN package_versions v ON v.id = f.version_id WHERE f.id = $1`, fileID)
}

// markSnapshot turns a seeded maven file into one build of a snapshot, the way
// the maven store records it: the version metadata and the file's cleanup part.
func (e *cleanupQuotaEnv) markSnapshot(fileID, ts string, build int) {
	e.t.Helper()

	e.exec(`UPDATE package_versions SET metadata = jsonb_build_object('is_snapshot', true, 'snapshot_timestamp', $2::text,
	         'snapshot_build_number', $3::int) WHERE id = (SELECT version_id FROM package_files WHERE id = $1)`, fileID, ts, build)
	e.exec(`UPDATE package_files f SET part = 'true:' || v.version FROM package_versions v WHERE f.id = $1 AND v.id = f.version_id`, fileID)
}

func (e *cleanupQuotaEnv) reserve(repoID, blobID string) int64 {
	e.t.Helper()

	var size int64
	if err := e.pg.GetDB().
		QueryRowContext(context.Background(), `SELECT size_bytes FROM blobs WHERE id = $1`, blobID).
		Scan(&size); err != nil {
		e.t.Fatalf("blob size: %v", err)
	}

	if ok, err := e.pg.ReserveStorageUsage(context.Background(), repoID, size, 1); err != nil || !ok {
		e.t.Fatalf("ReserveStorageUsage = %v, %v", ok, err)
	}

	return size
}

// rowTable is the table holding the rows of a format: the generic files, or the docker tags.
func rowTable(format string) string {
	if format == "docker" {
		return "docker_tags"
	}

	return "package_files"
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

func (g gcRec) CollectRemoved(context.Context, *models.Repository) error {
	g.r.mu.Lock()
	defer g.r.mu.Unlock()

	g.r.gcRuns++

	return nil
}

func (e *cleanupQuotaEnv) notifiers() *recordingNotifiers {
	r := &recordingNotifiers{}

	workers := formatlifecycle.NewWorkers()
	workers.Bind("maven", mavenRec{r})
	workers.Bind("npm", npmRec{r})
	workers.Bind("docker", gcRec{r})
	e.worker.SetLifecycleWorkers(workers)

	return r
}

// Every format keeps the newest version, removes the older ones with their
// blobs, and hands their quota usage back.
func TestCleanupPolicyEveryFormatKeepsNewest(t *testing.T) {
	for _, format := range []string{"npm", "maven", "ivy", "sbt", "cargo", "composer", "rubygems", "conda", "conan", "apk", "terraform", "terraform-provider", "pacman", "nix", "ansible", "raw", "docker"} {
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

	pkg := e.packageOf(tagged.id)
	e.exec(`INSERT INTO package_tags (package_id, tag, version) VALUES ($1, 'stable', '1.0.0')`, pkg)

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

	if n := e.countRows(`SELECT count(*) FROM packages WHERE repository_id = $1 AND name = 'old-lib'`, repoID); n != 0 {
		t.Errorf("empty package old-lib still exists")
	}

	if n := e.countRows(`SELECT count(*) FROM packages WHERE repository_id = $1 AND name = 'new-lib'`, repoID); n != 1 {
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
		e.markSnapshot(row.id, ts, build)

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
	e.markSnapshot(row.id, "20250101.100000", 1)

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
	for _, format := range []string{"npm", "maven", "cargo", "composer", "docker", "raw", "conan", "apk", "terraform", "terraform-provider", "pacman", "nix", "ansible"} {
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

	ref := e.queryID(
		`INSERT INTO artifact_refs (repository_id, artifact_path, format) VALUES ($1, $2, 'cargo') RETURNING id`,
		repoID,
		quarantined.id,
	)
	job := e.queryID(
		`INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status) VALUES ($1, 'vuln', 'osv', 'completed') RETURNING id`,
		ref,
	)
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

	for _, format := range []string{"npm", "maven", "ivy", "sbt", "cargo", "composer", "rubygems", "conda", "conan", "apk", "terraform", "terraform-provider", "pacman", "nix", "ansible", "raw", "docker"} {
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

			if _, err := e.worker.DeletePackage(ctx, repo, "not-a-uuid", nil, ""); !errors.Is(err, worker.ErrPackageNotFound) {
				t.Fatalf("bad id: err = %v, want ErrPackageNotFound", err)
			}

			res, err := e.worker.DeletePackage(ctx, repo, a.id, nil, "")
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

			if _, err := e.worker.DeletePackage(ctx, repo, a.id, nil, ""); !errors.Is(err, worker.ErrPackageNotFound) {
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
	}, "alice"); !errors.Is(err, errGuard) {
		t.Fatalf("err = %v, want the guard's", err)
	}

	if !e.live("cargo", a.id) {
		t.Fatal("a guarded delete removed the row")
	}

	res, err := e.worker.DeletePackage(ctx, repo, a.id, nil, "alice")
	if err != nil || !res.Trashed {
		t.Fatalf("DeletePackage = %+v, %v; want trashed", res, err)
	}

	// The trash entry names the requester, not the cleanup policy.
	if n := e.countRows(`SELECT count(*) FROM package_files WHERE id = $1 AND deleted_by = 'alice'`, a.id); n != 1 {
		t.Errorf("explicit delete: %d trashed rows attributed to the requester, want 1", n)
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
	pkg := e.packageOf(v.id)
	e.exec(`INSERT INTO package_tags (package_id, tag, version) VALUES ($1, 'latest', '1.0.0')`, pkg)

	if _, err := e.worker.DeletePackage(ctx, npmRepo, v.id, nil, ""); err != nil {
		t.Fatal(err)
	}

	if n := e.countRows(`SELECT count(*) FROM package_tags WHERE package_id = $1`, pkg); n != 0 {
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
	if err := e.pg.CreateCleanupPolicy(
		ctx,
		&models.CleanupPolicy{RepositoryID: custom, Name: "own", Enabled: true, KeepLastN: &own},
	); err != nil {
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
