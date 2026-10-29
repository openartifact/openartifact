// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))

	return hex.EncodeToString(sum[:])
}

func jvmCountRows(t *testing.T, s *quotaSeeder, query string, args ...any) int {
	t.Helper()

	var n int
	if err := s.pg.GetDB().QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}

	return n
}

// genericCounts returns the live packages, versions and files of a repository.
func genericCounts(t *testing.T, s *quotaSeeder) (int, int, int) {
	t.Helper()

	pkgs := jvmCountRows(t, s, `SELECT count(*) FROM packages WHERE repository_id = $1`, s.repo)
	vers := jvmCountRows(t, s, `SELECT count(*) FROM package_versions WHERE repository_id = $1 AND deleted_at IS NULL`, s.repo)
	files := jvmCountRows(t, s, `SELECT count(*) FROM package_files WHERE repository_id = $1 AND deleted_at IS NULL`, s.repo)

	return pkgs, vers, files
}

//nolint:gocyclo,cyclop // one round trip pins every typed read and the generic rows behind it
func TestMavenRoundTripOnGenericTables(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "mvn-rt", "maven")

	s.corrupt(0, 0) // counters are maintained once the repository has a row
	reserveN(t, pg, s.repo, 7, 1)

	jar := &models.MavenArtifact{
		RepositoryID: s.repo, BlobID: s.blob(7), GroupID: "com.ex", ArtifactCoord: "app", Version: "1.0",
		Extension: "jar", RepoPath: "com/ex/app/1.0/app-1.0.jar", MD5: "md5a", SHA1: "sha1a", SHA512: "sha512a",
	}
	if err := pg.CreateMavenArtifact(ctx, jar); err != nil {
		t.Fatalf("CreateMavenArtifact: %v", err)
	}

	requireUsage(t, pg, s.repo, 7, 1)

	if n := pendingEntries(t, pg, s.repo); n != 0 {
		t.Errorf("%d pending quota entries left, want 0", n)
	}

	src := &models.MavenArtifact{
		RepositoryID: s.repo, BlobID: s.blob(3), GroupID: "com.ex", ArtifactCoord: "app", Version: "1.0",
		Classifier: "sources", Extension: "jar", RepoPath: "com/ex/app/1.0/app-1.0-sources.jar", SHA1: "sha1s",
	}
	if err := pg.CreateMavenArtifact(ctx, src); err != nil {
		t.Fatalf("CreateMavenArtifact(sources): %v", err)
	}

	snap1 := &models.MavenArtifact{
		RepositoryID: s.repo, BlobID: s.blob(1), GroupID: "com.ex", ArtifactCoord: "app", Version: "2.0-SNAPSHOT",
		Extension: "jar", SnapshotTimestamp: "20240101.000000", SnapshotBuildNumber: 1, IsSnapshot: true,
		RepoPath: "com/ex/app/2.0-SNAPSHOT/app-2.0-20240101.000000-1.jar",
	}
	snap2 := &models.MavenArtifact{
		RepositoryID: s.repo, BlobID: s.blob(2), GroupID: "com.ex", ArtifactCoord: "app", Version: "2.0-SNAPSHOT",
		Extension: "jar", SnapshotTimestamp: "20240102.000000", SnapshotBuildNumber: 2, IsSnapshot: true,
		RepoPath: "com/ex/app/2.0-SNAPSHOT/app-2.0-20240102.000000-2.jar",
	}

	for _, a := range []*models.MavenArtifact{snap1, snap2} {
		if err := pg.CreateMavenArtifact(ctx, a); err != nil {
			t.Fatalf("CreateMavenArtifact(snapshot): %v", err)
		}
	}

	// One package; 1.0 and each snapshot build are versions of their own.
	if p, v, f := genericCounts(t, s); p != 1 || v != 3 || f != 4 {
		t.Errorf("packages/versions/files = %d/%d/%d, want 1/3/4", p, v, f)
	}

	var name, nameKey, groupID, artifactID string
	if err := pg.GetDB().QueryRowContext(ctx,
		`SELECT name, name_key, attrs->>'group_id', attrs->>'artifact_id' FROM packages WHERE repository_id = $1`, s.repo,
	).Scan(&name, &nameKey, &groupID, &artifactID); err != nil {
		t.Fatal(err)
	}

	if name != "com.ex:app" || nameKey != name || groupID != "com.ex" || artifactID != "app" {
		t.Errorf("package = %q/%q %q %q", name, nameKey, groupID, artifactID)
	}

	versionKeys := jvmCountRows(t, s, `SELECT count(*) FROM package_versions WHERE repository_id = $1
	    AND version_key IN ('1.0', '2.0-SNAPSHOT#20240101.000000-1', '2.0-SNAPSHOT#20240102.000000-2')`, s.repo)
	if versionKeys != 3 {
		t.Errorf("%d version keys match the spec, want 3", versionKeys)
	}

	var arch, part, unitKey, role, sha1sum string

	var listed bool
	if err := pg.GetDB().QueryRowContext(ctx,
		`SELECT f.arch, f.part, f.unit_key, f.role, f.listed, f.checksums->>'sha1'
		   FROM package_files f WHERE f.id = $1`, src.ID,
	).Scan(&arch, &part, &unitKey, &role, &listed, &sha1sum); err != nil {
		t.Fatal(err)
	}

	if arch != "sources" || part != "false" || unitKey != src.RepoPath || role != "primary" || !listed || sha1sum != "sha1s" {
		t.Errorf("sources file = %q %q %q %q %v %q", arch, part, unitKey, role, listed, sha1sum)
	}

	// Only the digests that are known are in F.checksums.
	if n := jvmCountRows(
		t,
		s,
		`SELECT count(*) FROM package_files WHERE id = $1 AND checksums ? 'md5' OR id = $1 AND checksums ? 'sha512'`,
		src.ID,
	); n != 0 {
		t.Error("unknown digests are stored in F.checksums")
	}

	if err := pg.GetDB().
		QueryRowContext(ctx, `SELECT part FROM package_files WHERE id = $1`, snap2.ID).
		Scan(&part); err != nil ||
		part != "true:2.0-SNAPSHOT" {
		t.Errorf("snapshot file part = %q, %v", part, err)
	}

	// Typed reads.
	got, err := pg.GetMavenArtifactByPath(ctx, s.repo, jar.RepoPath)
	if err != nil || got == nil {
		t.Fatalf("GetMavenArtifactByPath = %v, %v", got, err)
	}

	if got.ID != jar.ID || got.GroupID != "com.ex" || got.ArtifactCoord != "app" || got.Version != "1.0" || got.MD5 != "md5a" ||
		got.SHA1 != "sha1a" || got.SHA512 != "sha512a" || got.SHA256 == "" || got.BlobID != jar.BlobID || got.BlobSizeBytes != 7 {
		t.Errorf("GetMavenArtifactByPath = %+v", got)
	}

	byGAV, err := pg.GetMavenArtifactsByGAV(ctx, s.repo, "com.ex", "app", "1.0")
	if err != nil || len(byGAV) != 2 {
		t.Fatalf("GetMavenArtifactsByGAV = %d rows, %v; want 2", len(byGAV), err)
	}

	snapAll, err := pg.GetMavenArtifactsByGAV(ctx, s.repo, "com.ex", "app", "2.0-SNAPSHOT")
	if err != nil || len(snapAll) != 2 {
		t.Fatalf("GetMavenArtifactsByGAV(snapshot) = %d rows, %v; want 2", len(snapAll), err)
	}

	latest, err := pg.GetLatestSnapshot(ctx, s.repo, "com.ex", "app", "2.0-SNAPSHOT")
	if err != nil || latest == nil || latest.SnapshotBuildNumber != 2 {
		t.Fatalf("GetLatestSnapshot = %+v, %v; want build 2", latest, err)
	}

	list, err := pg.ListMavenArtifactsByRepo(ctx, s.repo)
	if err != nil || len(list) != 4 {
		t.Fatalf("ListMavenArtifactsByRepo = %d rows, %v; want 4", len(list), err)
	}

	for _, a := range list {
		if a.BlobSHA256 == "" || a.BlobSHA256 != a.SHA256 {
			t.Errorf("list row %s has BlobSHA256 %q / SHA256 %q", a.RepoPath, a.BlobSHA256, a.SHA256)
		}
	}

	// A snapshot overwrite repoints the file and replaces the digests; the id
	// and the creation time stay.
	again := &models.MavenArtifact{
		RepositoryID: s.repo, BlobID: s.blob(9), GroupID: "com.ex", ArtifactCoord: "app", Version: "2.0-SNAPSHOT",
		Extension: "jar", SnapshotTimestamp: "20240102.000000", SnapshotBuildNumber: 2, IsSnapshot: true,
		RepoPath: snap2.RepoPath, SHA1: "newsha1",
	}
	if err := pg.CreateMavenArtifact(ctx, again); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	if again.ID != snap2.ID {
		t.Errorf("overwrite id = %s, want %s", again.ID, snap2.ID)
	}

	if p, v, f := genericCounts(t, s); p != 1 || v != 3 || f != 4 {
		t.Errorf("after overwrite packages/versions/files = %d/%d/%d, want 1/3/4", p, v, f)
	}

	if a, err := pg.GetMavenArtifactByPath(
		ctx,
		s.repo,
		snap2.RepoPath,
	); err != nil || a == nil || a.SHA1 != "newsha1" ||
		a.BlobSizeBytes != 9 {
		t.Errorf("after overwrite = %+v, %v", a, err)
	}

	if n := jvmCountRows(t, s, `SELECT count(*) FROM package_files WHERE id = $1 AND checksums->>'sha1' = 'newsha1'`, snap2.ID); n != 1 {
		t.Error("F.checksums not updated with the maven_files digests")
	}

	// Deleting the last file of a version prunes the version; the package stays
	// while another version is live.
	if err := pg.DeleteMavenArtifact(ctx, snap1.ID); err != nil {
		t.Fatalf("DeleteMavenArtifact: %v", err)
	}

	if p, v, f := genericCounts(t, s); p != 1 || v != 2 || f != 3 {
		t.Errorf("after delete packages/versions/files = %d/%d/%d, want 1/2/3", p, v, f)
	}

	if gone, err := pg.GetMavenArtifactByPath(ctx, s.repo, snap1.RepoPath); err != nil || gone != nil {
		t.Errorf("deleted artifact = %v, %v; want nil", gone, err)
	}

	for _, a := range []*models.MavenArtifact{jar, src, again} {
		if err := pg.DeleteMavenArtifact(ctx, a.ID); err != nil {
			t.Fatal(err)
		}
	}

	if p, v, f := genericCounts(t, s); p != 0 || v != 0 || f != 0 {
		t.Errorf("after deleting everything packages/versions/files = %d/%d/%d, want 0/0/0", p, v, f)
	}
}

//nolint:gocyclo,cyclop // one round trip pins every typed read and the generic rows behind it
func TestIvyRoundTripOnGenericTables(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "ivy-rt", "ivy")

	art := func(name, ext, sha string) *models.IvyArtifact {
		return &models.IvyArtifact{
			RepositoryID: s.repo, Organisation: "org.ex", Module: "mod", Revision: "1.0", ArtifactName: name,
			ArtifactType: "jar", ArtifactExt: ext, SHA256: sha, BlobKey: "k" + sha, SizeBytes: 4,
		}
	}

	// The handler stores the whole file name (sub-directory included) as the name.
	whole := art("jars/mod-1.0.jar", "jar", sha256Of("a"))
	if err := pg.CreateIvyArtifact(ctx, whole); err != nil {
		t.Fatalf("CreateIvyArtifact: %v", err)
	}

	// Older rows carry a bare name and the extension separately.
	bare := art("mod", "xml", sha256Of("b"))
	if err := pg.CreateIvyArtifact(ctx, bare); err != nil {
		t.Fatalf("CreateIvyArtifact(bare): %v", err)
	}

	var filename, path string
	if err := pg.GetDB().
		QueryRowContext(ctx, `SELECT filename, path FROM package_files WHERE id = $1`, whole.ID).
		Scan(&filename, &path); err != nil {
		t.Fatal(err)
	}

	if filename != "jars/mod-1.0.jar" || path != "org.ex/mod/1.0/jars/mod-1.0.jar" {
		t.Errorf("whole-name file = %q %q (extension must not be doubled)", filename, path)
	}

	if err := pg.GetDB().
		QueryRowContext(ctx, `SELECT filename FROM package_files WHERE id = $1`, bare.ID).
		Scan(&filename); err != nil ||
		filename != "mod.xml" {
		t.Errorf("bare-name filename = %q, %v; want mod.xml", filename, err)
	}

	if p, v, f := genericCounts(t, s); p != 1 || v != 1 || f != 2 {
		t.Errorf("packages/versions/files = %d/%d/%d, want 1/1/2", p, v, f)
	}

	got, err := pg.GetIvyArtifact(ctx, s.repo, "org.ex", "mod", "1.0", "jars/mod-1.0.jar", "jar")
	if err != nil || got == nil {
		t.Fatalf("GetIvyArtifact = %v, %v", got, err)
	}

	if got.ID != whole.ID || got.Organisation != "org.ex" || got.Module != "mod" || got.Revision != "1.0" ||
		got.ArtifactName != "jars/mod-1.0.jar" || got.ArtifactType != "jar" || got.ArtifactExt != "jar" ||
		got.SHA256 != whole.SHA256 || got.BlobKey != whole.BlobKey || got.SizeBytes != 4 {
		t.Errorf("GetIvyArtifact = %+v", got)
	}

	if got, err := pg.GetIvyArtifact(ctx, s.repo, "org.ex", "mod", "1.0", "mod", "xml"); err != nil || got == nil || got.ID != bare.ID {
		t.Errorf("GetIvyArtifact(bare) = %v, %v", got, err)
	}

	// Republishing different bytes repoints the same row.
	again := art("jars/mod-1.0.jar", "jar", sha256Of("c"))
	if err := pg.CreateIvyArtifact(ctx, again); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	if again.ID != whole.ID || !again.CreatedAt.Equal(whole.CreatedAt) {
		t.Errorf("overwrite id/created_at = %s/%v, want %s/%v", again.ID, again.CreatedAt, whole.ID, whole.CreatedAt)
	}

	if got, err := pg.GetIvyArtifact(
		ctx,
		s.repo,
		"org.ex",
		"mod",
		"1.0",
		"jars/mod-1.0.jar",
		"jar",
	); err != nil || got == nil ||
		got.SHA256 != again.SHA256 {
		t.Errorf("after overwrite = %v, %v", got, err)
	}

	if ok, err := pg.IvyModuleExists(ctx, s.repo, "org.ex", "mod"); err != nil || !ok {
		t.Errorf("IvyModuleExists = %v, %v; want true", ok, err)
	}

	if ok, err := pg.IvyModuleExists(ctx, s.repo, "org.ex", "other"); err != nil || ok {
		t.Errorf("IvyModuleExists(other) = %v, %v; want false", ok, err)
	}

	for _, a := range []*models.IvyArtifact{whole, bare} {
		if err := pg.DeleteIvyArtifact(ctx, a.ID); err != nil {
			t.Fatal(err)
		}
	}

	if p, v, f := genericCounts(t, s); p != 0 || v != 0 || f != 0 {
		t.Errorf("after delete packages/versions/files = %d/%d/%d, want 0/0/0", p, v, f)
	}

	if ok, err := pg.IvyModuleExists(ctx, s.repo, "org.ex", "mod"); err != nil || ok {
		t.Errorf("IvyModuleExists after delete = %v, %v; want false", ok, err)
	}
}

//nolint:gocyclo,cyclop // one round trip pins every typed read and the generic rows behind it
func TestSBTRoundTripOnGenericTables(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "sbt-rt", "sbt")

	s.corrupt(0, 0) // counters are maintained once the repository has a row
	reserveN(t, pg, s.repo, 4, 1)

	a := &models.SBTArtifact{
		RepositoryID: s.repo, GroupID: "org.ex", ArtifactID: "lib_2.13", Version: "1.0", Filename: "lib_2.13-1.0.jar",
		ScalaVersion: "2.13", SHA256: sha256Of("a"), BlobKey: "ka", SizeBytes: 4,
	}
	if err := pg.CreateSBTArtifact(ctx, a); err != nil {
		t.Fatalf("CreateSBTArtifact: %v", err)
	}

	requireUsage(t, pg, s.repo, 4, 1)

	var name, path, arch, part, unitKey *string
	if err := pg.GetDB().QueryRowContext(ctx,
		`SELECT p.name, f.path, f.arch, f.part, f.unit_key FROM package_files f
		   JOIN package_versions v ON v.id = f.version_id JOIN packages p ON p.id = v.package_id WHERE f.id = $1`, a.ID,
	).Scan(&name, &path, &arch, &part, &unitKey); err != nil {
		t.Fatal(err)
	}

	if *name != "org.ex:lib_2.13" || *path != "org.ex/lib_2.13/1.0/lib_2.13-1.0.jar" || *arch != "2.13" || *part != "" || unitKey != nil {
		t.Errorf("rows = %q %q %q %q %v", *name, *path, *arch, *part, unitKey)
	}

	got, err := pg.GetSBTArtifact(ctx, s.repo, "org.ex", "lib_2.13", "1.0", "lib_2.13-1.0.jar")
	if err != nil || got == nil {
		t.Fatalf("GetSBTArtifact = %v, %v", got, err)
	}

	if got.ID != a.ID || got.GroupID != "org.ex" || got.ArtifactID != "lib_2.13" || got.Version != "1.0" ||
		got.ScalaVersion != "2.13" || got.Filename != "lib_2.13-1.0.jar" || got.SHA256 != a.SHA256 || got.SizeBytes != 4 {
		t.Errorf("GetSBTArtifact = %+v", got)
	}

	if got, err := pg.GetSBTArtifact(ctx, s.repo, "org.ex", "lib_2.13", "1.0", "missing.jar"); err != nil || got != nil {
		t.Errorf("GetSBTArtifact(missing) = %v, %v; want nil", got, err)
	}

	if ok, err := pg.SBTModuleExists(ctx, s.repo, "org.ex", "lib_2.13"); err != nil || !ok {
		t.Errorf("SBTModuleExists = %v, %v; want true", ok, err)
	}

	if err := pg.DeleteSBTArtifact(ctx, a.ID); err != nil {
		t.Fatal(err)
	}

	if p, v, f := genericCounts(t, s); p != 0 || v != 0 || f != 0 {
		t.Errorf("after delete packages/versions/files = %d/%d/%d, want 0/0/0", p, v, f)
	}

	// The hard delete leaves the file's usage pending until the handler
	// releases it, which consumes the entry.
	requireUsage(t, pg, s.repo, 4, 1)
	releaseN(t, pg, s.repo, 4, 1)
	requireUsage(t, pg, s.repo, 0, 0)

	if n := pendingEntries(t, pg, s.repo); n != 0 {
		t.Errorf("%d pending quota entries left, want 0", n)
	}

	if ok, err := pg.SBTModuleExists(ctx, s.repo, "org.ex", "lib_2.13"); err != nil || ok {
		t.Errorf("SBTModuleExists after delete = %v, %v; want false", ok, err)
	}
}
