// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/storage"
)

// TestTrashKindsComeFromDescriptors: the kinds the trash accepts are the ones
// formats registered (plus docker and repository), every registered kind lists
// and resolves without error, and an unregistered kind is refused.
func TestTrashKindsComeFromDescriptors(t *testing.T) {
	registerTrashFixtures(t)

	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()

	kinds := []models.TrashKind{models.TrashKindDockerManifest, models.TrashKindDockerTag}
	for _, k := range storage.TrashKinds() {
		kinds = append(kinds, models.TrashKind(k))
	}

	for _, kind := range kinds {
		if _, _, err := pg.ListTrash(ctx, models.TrashFilter{Kind: kind}); err != nil {
			t.Errorf("ListTrash(%s): %v", kind, err)
		}

		if _, err := pg.GetTrashItem(ctx, kind, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrTrashNotFound) {
			t.Errorf("GetTrashItem(%s) = %v, want ErrTrashNotFound", kind, err)
		}
	}

	if _, err := pg.GetTrashItem(ctx, "no_such_kind", "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrTrashUnknownKind) {
		t.Errorf("GetTrashItem(no_such_kind) = %v, want ErrTrashUnknownKind", err)
	}

	if _, err := pg.ListExpiredTrash(ctx, 10); err != nil {
		t.Errorf("ListExpiredTrash: %v", err)
	}

	if _, _, err := pg.ListTrash(ctx, models.TrashFilter{IncludeRepositories: true}); err != nil {
		t.Fatalf("global ListTrash: %v", err)
	}
}

// fileID returns the live package_files id at path.
func fileID(t *testing.T, s *quotaSeeder, path string) string {
	t.Helper()

	return s.queryID(`SELECT id FROM package_files WHERE repository_id = $1 AND path = $2 AND deleted_at IS NULL`, s.repo, path)
}

func mavenArtifactID(t *testing.T, s *quotaSeeder, path string) string {
	t.Helper()

	return fileID(t, s, path)
}

func countRows(t *testing.T, pg *PostgresDB, query string, args ...any) int {
	t.Helper()

	var n int
	if err := pg.GetDB().QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}

	return n
}

// checkTrashedMavenItem asserts the single trash item of repoID: the maven
// descriptor names it by path, the version comes from the version row and the
// purge date is the retention window after the delete.
func checkTrashedMavenItem(t *testing.T, pg *PostgresDB, repoID, id, path string) {
	t.Helper()

	items, total, err := pg.ListTrash(context.Background(), models.TrashFilter{RepositoryID: repoID})
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("ListTrash = %d items, total %d, err %v; want 1", len(items), total, err)
	}

	it := items[0]
	if it.Kind != models.TrashKindMaven || it.ID != id || it.Name != path || it.Version != "1.0" ||
		it.SizeBytes != 100 || it.DeletedBy != "alice" || it.RepositoryName != "trash-maven" || it.Format != "maven" {
		t.Fatalf("unexpected item %+v", it)
	}

	if d := it.PurgeAt.Sub(it.DeletedAt).Hours(); d < 7*24-1 || d > 7*24+1 {
		t.Fatalf("purge_at - deleted_at = %vh, want 168h", d)
	}
}

func TestTrashRowsLifecycle(t *testing.T) {
	registerTrashFixtures(t)

	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "trash-maven", "maven")
	s.maven(s.blob(100))

	path := "g/a1/1.0/a-1.0.jar"
	id := mavenArtifactID(t, s, path)

	soft, err := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{id}, "alice")
	if err != nil || !soft {
		t.Fatalf("TrashRows = %v, %v; want soft delete", soft, err)
	}

	// The file and its version are stamped together; nothing else is touched.
	if n := countRows(
		t,
		pg,
		`SELECT COUNT(*) FROM package_files WHERE id = $1 AND deleted_at IS NOT NULL AND deleted_by = 'alice'`,
		id,
	); n != 1 {
		t.Fatalf("file not stamped: %d", n)
	}

	if n := countRows(t, pg, `SELECT COUNT(*) FROM package_versions WHERE repository_id = $1 AND deleted_at IS NOT NULL`, s.repo); n != 1 {
		t.Fatalf("version of the last file not stamped: %d", n)
	}

	// Trashing twice finds no live row.
	if _, err := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{id}, "alice"); !errors.Is(err, ErrTrashNotFound) {
		t.Fatalf("second TrashRows = %v, want ErrTrashNotFound", err)
	}

	checkTrashedMavenItem(t, pg, s.repo, id, path)

	// Restoring un-stamps the file and its version.
	if _, err := pg.RestoreTrash(ctx, models.TrashKindMaven, id); err != nil {
		t.Fatalf("RestoreTrash: %v", err)
	}

	if n := countRows(t, pg, `SELECT COUNT(*) FROM package_files f JOIN package_versions v ON v.id = f.version_id
	                          WHERE f.id = $1 AND f.deleted_at IS NULL AND v.deleted_at IS NULL`, id); n != 1 {
		t.Fatalf("file or version still trashed after the restore: %d", n)
	}
}

func TestTrashRestoreConflictsWithReusedPath(t *testing.T) {
	registerTrashFixtures(t)

	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "trash-conflict", "maven")
	s.maven(s.blob(100))

	path := "g/a1/1.0/a-1.0.jar"
	id := mavenArtifactID(t, s, path)

	if soft, err := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{id}, "alice"); err != nil || !soft {
		t.Fatalf("TrashRows = %v, %v", soft, err)
	}

	// Reusing the path while the old row sits in the trash is allowed (same
	// package and version: a new live version incarnation)...
	s.write("maven", "g:a1", "1.0", path, s.blob(5))

	// ...and makes the restore a conflict.
	if _, err := pg.RestoreTrash(ctx, models.TrashKindMaven, id); !errors.Is(err, ErrTrashConflict) {
		t.Fatalf("RestoreTrash with reused path = %v, want ErrTrashConflict", err)
	}

	s.exec(`DELETE FROM package_files WHERE repository_id = $1 AND path = $2 AND deleted_at IS NULL`, s.repo, path)

	// The re-published version is live and empty: the restored file joins it
	// and the stale incarnation is pruned.
	got, err := pg.RestoreTrash(ctx, models.TrashKindMaven, id)
	if err != nil || got.RepositoryID != s.repo {
		t.Fatalf("RestoreTrash = %+v, %v", got, err)
	}

	if mavenArtifactID(t, s, path) != id {
		t.Fatal("restored row is not the original")
	}

	if n := countRows(t, pg, `SELECT COUNT(*) FROM package_versions WHERE repository_id = $1`, s.repo); n != 1 {
		t.Fatalf("%d versions after the restore, want the single live one", n)
	}

	if _, err := pg.RestoreTrash(ctx, models.TrashKindMaven, id); !errors.Is(err, ErrTrashNotFound) {
		t.Fatalf("restoring a live row = %v, want ErrTrashNotFound", err)
	}
}

// A file of a multi-file version that is trashed alone leaves the version live;
// the version is stamped only with its last live file.
func TestTrashVersionStampFollowsLastFile(t *testing.T) {
	registerTrashFixtures(t)

	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "trash-lastfile", "maven")

	s.writeFiles(&PackageWrite{
		RepoID: s.repo, Format: "maven", Name: "g:a", NameKey: "g:a", Version: "1.0", VersionKey: "1.0",
		Files: []FileWrite{
			{Path: "g/a/1.0/a-1.0.jar", Filename: "a-1.0.jar", Blob: s.blobs[s.blob(10)]},
			{Path: "g/a/1.0/a-1.0.pom", Filename: "a-1.0.pom", Blob: s.blobs[s.blob(3)]},
		},
	})

	jar, pom := fileID(t, s, "g/a/1.0/a-1.0.jar"), fileID(t, s, "g/a/1.0/a-1.0.pom")
	liveVersions := func() int {
		return countRows(t, pg, `SELECT COUNT(*) FROM package_versions WHERE repository_id = $1 AND deleted_at IS NULL`, s.repo)
	}

	if _, err := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{jar}, "x"); err != nil || liveVersions() != 1 {
		t.Fatalf("trashing the jar: err %v, live versions %d; want the version live", err, liveVersions())
	}

	if _, err := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{pom}, "x"); err != nil || liveVersions() != 0 {
		t.Fatalf("trashing the pom: err %v, live versions %d; want the version stamped", err, liveVersions())
	}

	if _, err := pg.RestoreTrash(ctx, models.TrashKindMaven, jar); err != nil || liveVersions() != 1 {
		t.Fatalf("restoring the jar: err %v, live versions %d; want the version live again", err, liveVersions())
	}

	// The pom restores into the version the jar revived, not into a new one.
	if _, err := pg.RestoreTrash(ctx, models.TrashKindMaven, pom); err != nil {
		t.Fatal(err)
	}

	if n := countRows(
		t,
		pg,
		`SELECT COUNT(DISTINCT version_id) FROM package_files WHERE repository_id = $1 AND deleted_at IS NULL`,
		s.repo,
	); n != 1 {
		t.Fatalf("files spread over %d versions after the restores, want 1", n)
	}
}

func TestTrashPurgeReportsBlobsAndQuota(t *testing.T) {
	registerTrashFixtures(t)

	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "trash-purge", "maven")
	blob := s.blob(250)
	s.maven(blob)

	id := mavenArtifactID(t, s, "g/a1/1.0/a-1.0.jar")

	if _, err := pg.PurgeTrash(ctx, models.TrashKindMaven, id); !errors.Is(err, ErrTrashNotFound) {
		t.Fatalf("purging a live row = %v, want ErrTrashNotFound", err)
	}

	if soft, err := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{id}, "bob"); err != nil || !soft {
		t.Fatalf("TrashRows = %v, %v", soft, err)
	}

	res, err := pg.PurgeTrash(ctx, models.TrashKindMaven, id)
	if err != nil {
		t.Fatal(err)
	}

	if len(res.BlobSHA256) != 1 || res.SizeBytes != 250 || res.QuotaArtifacts != 1 || res.QuotaBytes != 250 {
		t.Fatalf("purge result = %+v", res)
	}

	if n := countRows(t, pg, `SELECT COUNT(*) FROM package_files WHERE id = $1`, id); n != 0 {
		t.Fatalf("file still present after purge: %d", n)
	}

	// The emptied version and package go with the last file.
	if n := countRows(t, pg, `SELECT COUNT(*) FROM package_versions WHERE repository_id = $1`, s.repo); n != 0 {
		t.Fatalf("%d versions left after the purge", n)
	}

	if n := countRows(t, pg, `SELECT COUNT(*) FROM packages WHERE repository_id = $1`, s.repo); n != 0 {
		t.Fatalf("%d packages left after the purge", n)
	}

	// The hard delete queued the release the purge worker takes back.
	if n := pendingEntries(t, pg, s.repo); n != 1 {
		t.Fatalf("%d pending quota releases after the purge, want 1", n)
	}
}

// Trashing, restoring and purging a go module acts on the zip and the go.mod
// together: one item, one artifact, both blobs.
func TestTrashVersionScopeTakesCompanionFiles(t *testing.T) {
	registerTrashFixtures(t)

	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "trash-gomod", "go")

	zip, mod := s.blob(100), s.blob(7)
	s.writeFiles(&PackageWrite{
		RepoID: s.repo, Format: "go", Name: "example.com/m", NameKey: "example.com/m", Version: "v1.0.0", VersionKey: "v1.0.0",
		Files: []FileWrite{
			{Path: "example.com/m/@v/v1.0.0.zip", Filename: "v1.0.0.zip", Blob: s.blobs[zip]},
			{Path: "example.com/m/@v/v1.0.0.mod", Filename: "v1.0.0.mod", Role: "mod", Unlisted: true, Blob: s.blobs[mod]},
		},
	})

	zipID := fileID(t, s, "example.com/m/@v/v1.0.0.zip")

	if soft, err := pg.TrashRows(ctx, s.repo, models.TrashKindGoMod, []string{zipID}, "dan"); err != nil || !soft {
		t.Fatalf("TrashRows = %v, %v", soft, err)
	}

	if n := countRows(t, pg, `SELECT COUNT(*) FROM package_files WHERE repository_id = $1 AND deleted_at IS NOT NULL`, s.repo); n != 2 {
		t.Fatalf("%d files stamped, want zip and go.mod", n)
	}

	items, total, err := pg.ListTrash(ctx, models.TrashFilter{RepositoryID: s.repo})
	if err != nil || total != 1 || len(items) != 1 || items[0].ID != zipID || items[0].SizeBytes != 107 {
		t.Fatalf("ListTrash = %+v, total %d, %v; want the zip item sized 107", items, total, err)
	}

	if _, err := pg.RestoreTrash(ctx, models.TrashKindGoMod, zipID); err != nil {
		t.Fatal(err)
	}

	if n := countRows(t, pg, `SELECT COUNT(*) FROM package_files WHERE repository_id = $1 AND deleted_at IS NULL`, s.repo); n != 2 {
		t.Fatalf("%d files live after the restore, want 2", n)
	}

	if _, err := pg.TrashRows(ctx, s.repo, models.TrashKindGoMod, []string{zipID}, "dan"); err != nil {
		t.Fatal(err)
	}

	res, err := pg.PurgeTrash(ctx, models.TrashKindGoMod, zipID)
	if err != nil {
		t.Fatal(err)
	}

	if len(res.BlobSHA256) != 2 || res.QuotaArtifacts != 1 || res.QuotaBytes != 107 {
		t.Fatalf("purge result = %+v, want 2 blobs, 1 artifact, 107 bytes", res)
	}

	if n := countRows(t, pg, `SELECT COUNT(*) FROM package_files WHERE repository_id = $1`, s.repo); n != 0 {
		t.Fatalf("%d files left after the purge", n)
	}
}

// The npm descriptor hooks keep the packument validator and the dist-tags in
// step with trash, restore, purge and the retention worker's hard delete.
func TestTrashNPMHooks(t *testing.T) {
	registerTrashFixtures(t)

	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "trash-npm", "npm")

	s.npm(s.blob(10))

	pkgID := s.queryID(`SELECT id FROM packages WHERE repository_id = $1`, s.repo)
	s.exec(`INSERT INTO package_tags (package_id, tag, version) VALUES ($1, 'latest', '1.0.0')`, pkgID)

	id := s.queryID(`SELECT id FROM package_files WHERE repository_id = $1`, s.repo)

	rewind := func() {
		s.exec(`UPDATE packages SET updated_at = now() - interval '1 hour' WHERE id = $1`, pkgID)
	}
	bumped := func(what string) {
		t.Helper()

		if n := countRows(
			t,
			pg,
			`SELECT COUNT(*) FROM packages WHERE id = $1 AND updated_at > now() - interval '1 minute'`,
			pkgID,
		); n != 1 {
			t.Fatalf("%s did not bump the packument validator", what)
		}
	}

	rewind()

	if _, err := pg.TrashRows(ctx, s.repo, models.TrashKindNPM, []string{id}, "eve"); err != nil {
		t.Fatal(err)
	}

	bumped("trash")
	rewind()

	if _, err := pg.RestoreTrash(ctx, models.TrashKindNPM, id); err != nil {
		t.Fatal(err)
	}

	bumped("restore")

	keys, err := newKeysOf(pg, ctx, id)
	if err != nil || keys != pkgID {
		t.Fatalf("RegenKeys = %q, %v; want the package id", keys, err)
	}

	// A purge drops the tags of the purged version, unless a live one remains.
	if _, err := pg.TrashRows(ctx, s.repo, models.TrashKindNPM, []string{id}, "eve"); err != nil {
		t.Fatal(err)
	}

	rewind()

	if _, err := pg.PurgeTrash(ctx, models.TrashKindNPM, id); err != nil {
		t.Fatal(err)
	}

	if n := countRows(t, pg, `SELECT COUNT(*) FROM package_tags WHERE package_id = $1`, pkgID); n != 0 {
		t.Fatalf("%d dist-tags survived the purge of their version", n)
	}

	// The package row went with its last version, so there is no packument to bump.
	if n := countRows(t, pg, `SELECT COUNT(*) FROM packages WHERE id = $1`, pkgID); n != 0 {
		t.Fatalf("package row survived its last version: %d", n)
	}
}

func newKeysOf(pg *PostgresDB, ctx context.Context, id string) (string, error) {
	a, _, err := pg.RegenKeys(ctx, models.TrashKindNPM, id)

	return a, err
}

// HardDeleteFiles removes live files for good and reports what the caller frees.
func TestHardDeleteFilesReportsUsage(t *testing.T) {
	registerTrashFixtures(t)

	pg := newQuotaTestDB(t, 8)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "hard-delete", "maven")

	s.maven(s.blob(40))
	s.maven(s.blob(60))

	a, b := fileID(t, s, "g/a1/1.0/a-1.0.jar"), fileID(t, s, "g/a2/1.0/a-1.0.jar")

	// A trashed file is not live: the hard delete skips it.
	pg.SetTrashRetentionDays(7)

	if soft, err := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{b}, "x"); err != nil || !soft {
		t.Fatalf("TrashRows = %v, %v", soft, err)
	}

	got, err := pg.HardDeleteFiles(ctx, s.repo, []string{a, b})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].ID != a || got[0].QuotaArtifacts != 1 || got[0].QuotaBytes != 40 || len(got[0].BlobSHA256) != 1 {
		t.Fatalf("HardDeleteFiles = %+v, want only the live file with 1 artifact, 40 bytes, 1 blob", got)
	}

	if n := countRows(t, pg, `SELECT COUNT(*) FROM package_files WHERE id = $1`, a); n != 0 {
		t.Fatal("file survived the hard delete")
	}
}

func TestTrashRetentionResolution(t *testing.T) {
	registerTrashFixtures(t)

	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "trash-retention", "maven")

	s.maven(s.blob(1))
	id := mavenArtifactID(t, s, "g/a1/1.0/a-1.0.jar")

	repo, err := pg.GetRepositoryByNameUncached(ctx, "trash-retention")
	if err != nil || repo == nil {
		t.Fatalf("GetRepositoryByNameUncached = %v, %v", repo, err)
	}

	// Retention 0 on the repository: no soft delete.
	repo.TrashRetentionDays = new(0)
	if err := pg.UpdateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	if soft, err := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{id}, "x"); err != nil || soft {
		t.Fatalf("retention 0: TrashRows = %v, %v; want hard-delete signal", soft, err)
	}

	// Server default 0 with a NULL repository value: no soft delete either.
	repo.TrashRetentionDays = nil
	if err := pg.UpdateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	pg.SetTrashRetentionDays(0)

	if soft, _ := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{id}, "x"); soft {
		t.Fatal("default 0 must not soft delete")
	}

	// A repository value overrides the default; a 1 day window expires.
	repo.TrashRetentionDays = new(1)
	if err := pg.UpdateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	if soft, err := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{id}, "x"); err != nil || !soft {
		t.Fatalf("retention 1: TrashRows = %v, %v", soft, err)
	}

	refs, err := pg.ListExpiredTrash(ctx, 10)
	if err != nil || len(refs) != 0 {
		t.Fatalf("fresh item expired early: %v, %v", refs, err)
	}

	s.exec(`UPDATE package_files SET deleted_at = now() - interval '25 hours' WHERE id = $1`, id)

	refs, err = pg.ListExpiredTrash(ctx, 10)
	if err != nil || len(refs) != 1 || refs[0].ID != id || refs[0].Kind != models.TrashKindMaven {
		t.Fatalf("ListExpiredTrash = %v, %v; want the maven row", refs, err)
	}
}

func TestTrashRepositoryHidesAndRestores(t *testing.T) {
	registerTrashFixtures(t)

	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "trash-repo", "maven")
	s.maven(s.blob(10))

	soft, err := pg.TrashRepository(ctx, s.repo, "carol")
	if err != nil || !soft {
		t.Fatalf("TrashRepository = %v, %v", soft, err)
	}

	if r, err := pg.GetRepositoryByName(ctx, "trash-repo"); err != nil || r != nil {
		t.Fatalf("trashed repository still resolves: %v, %v", r, err)
	}

	if r, err := pg.GetRepositoryByID(ctx, s.repo); err != nil || r != nil {
		t.Fatalf("trashed repository still resolves by id: %v, %v", r, err)
	}

	// The name stays reserved until the purge.
	dup := &models.Repository{Name: "trash-repo", Format: "maven", RepoType: "hosted", ProjectID: new(systemOrgID)}
	if err := pg.CreateRepository(ctx, dup); !IsUniqueViolation(err) {
		t.Fatalf("re-creating a trashed name = %v, want unique violation", err)
	}

	items, _, err := pg.ListTrash(ctx, models.TrashFilter{IncludeRepositories: true})
	if err != nil || len(items) != 1 || items[0].Kind != models.TrashKindRepository || items[0].DeletedBy != "carol" {
		t.Fatalf("global ListTrash = %+v, %v", items, err)
	}

	if _, err := pg.RestoreTrash(ctx, models.TrashKindRepository, s.repo); err != nil {
		t.Fatal(err)
	}

	if r, err := pg.GetRepositoryByName(ctx, "trash-repo"); err != nil || r == nil {
		t.Fatalf("restored repository missing: %v, %v", r, err)
	}

	// Purge only takes trashed repositories.
	if _, err := pg.PurgeTrash(ctx, models.TrashKindRepository, s.repo); !errors.Is(err, ErrTrashNotFound) {
		t.Fatalf("purging a live repository = %v, want ErrTrashNotFound", err)
	}

	if _, err := pg.TrashRepository(ctx, s.repo, "carol"); err != nil {
		t.Fatal(err)
	}

	if _, err := pg.PurgeTrash(ctx, models.TrashKindRepository, s.repo); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := pg.GetDB().
		QueryRowContext(ctx, `SELECT COUNT(*) FROM package_files WHERE repository_id = $1`, s.repo).
		Scan(&n); err != nil ||
		n != 0 {
		t.Fatalf("repository contents survived the purge: %d, %v", n, err)
	}
}

func TestPruneMavenMetadataKeepsNeighbours(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "prune-maven", "maven")

	const sha = "0000000000000000000000000000000000000000000000000000000000000000"

	paths := []string{
		"com/example/a/maven-metadata.xml",
		"com/example/a/1.0-SNAPSHOT/maven-metadata.xml",
		"com/example/a/2.0-SNAPSHOT/maven-metadata.xml",
		// A different artifact whose coordinates live under the same directory.
		"com/example/a/b/maven-metadata.xml",
		"com/example/other/maven-metadata.xml",
	}
	for _, p := range paths {
		s.exec(`INSERT INTO repository_metadata (repository_id, path, blob_sha256, blob_size) VALUES ($1, $2, $3, 1)`, s.repo, p, sha)
	}

	keep := []string{"com/example/a/1.0-SNAPSHOT/maven-metadata.xml"}
	if _, err := pg.PruneMavenMetadata(ctx, s.repo, "com/example/a/", keep); err != nil {
		t.Fatalf("PruneMavenMetadata: %v", err)
	}

	want := map[string]bool{
		"com/example/a/maven-metadata.xml":              false,
		"com/example/a/1.0-SNAPSHOT/maven-metadata.xml": true,
		"com/example/a/2.0-SNAPSHOT/maven-metadata.xml": false,
		"com/example/a/b/maven-metadata.xml":            true,
		"com/example/other/maven-metadata.xml":          true,
	}
	for p, present := range want {
		meta, err := pg.GetRepoMetadataByPath(ctx, s.repo, p)
		if err != nil {
			t.Fatal(err)
		}

		if (meta != nil) != present {
			t.Errorf("%s present = %v, want %v", p, meta != nil, present)
		}
	}
}
