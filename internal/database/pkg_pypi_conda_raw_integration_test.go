// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database/models"
)

func testUUID(seed string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(seed)).String()
}

func testSHA(seed string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(seed)))
}

// addCondaBuild stores one file of a conda build through the typed store.
func addCondaBuild(t *testing.T, pg *PostgresDB, repoID, name, version, build, platform, ext string) *models.CondaPackage {
	t.Helper()

	sum := testSHA(repoID + name + version + build + platform + ext)
	p := &models.CondaPackage{
		RepositoryID: repoID, Name: name, Version: version, BuildString: build, Platform: platform, Extension: ext,
		SHA256: sum, BlobKey: sum, SizeBytes: 5,
	}

	if err := pg.CreateCondaPackage(context.Background(), p); err != nil {
		t.Fatalf("CreateCondaPackage %s %s: %v", name, ext, err)
	}

	return p
}

// trashCondaPackage stamps every file and version of the package the way the
// trash does.
func trashCondaPackage(t *testing.T, s *quotaSeeder, name string) {
	t.Helper()

	s.exec(`UPDATE package_files SET deleted_at = now() WHERE repository_id = $1 AND version_id IN
	        (SELECT v.id FROM package_versions v JOIN packages p ON p.id = v.package_id WHERE p.repository_id = $1 AND lower(p.name) = $2)`,
		s.repo, name)
	s.exec(`UPDATE package_versions SET deleted_at = now() WHERE repository_id = $1 AND package_id IN
	        (SELECT id FROM packages WHERE repository_id = $1 AND lower(name) = $2)`, s.repo, name)
}

type pypiGenericCounts struct{ packages, versions, files int }

func pypiCountGeneric(t *testing.T, s *quotaSeeder) pypiGenericCounts {
	t.Helper()

	var c pypiGenericCounts

	err := s.pg.GetDB().QueryRowContext(context.Background(), `SELECT
	  (SELECT count(*) FROM packages WHERE repository_id = $1),
	  (SELECT count(*) FROM package_versions WHERE repository_id = $1),
	  (SELECT count(*) FROM package_files WHERE repository_id = $1)`, s.repo).Scan(&c.packages, &c.versions, &c.files)
	if err != nil {
		t.Fatalf("count generic rows: %v", err)
	}

	return c
}

// pendingRelease is the usage a removed file handed back to the quota ledger.
func pendingRelease(t *testing.T, s *quotaSeeder) (int64, int64) {
	t.Helper()

	var size, count int64

	err := s.pg.GetDB().QueryRowContext(context.Background(),
		`SELECT COALESCE(sum(size_bytes), 0), COALESCE(sum(artifact_count), 0) FROM quota_pending WHERE repository_id = $1`, s.repo,
	).Scan(&size, &count)
	if err != nil {
		t.Fatalf("read quota ledger: %v", err)
	}

	return size, count
}

func TestPyPIRoundTrip(t *testing.T) { //nolint:gocyclo // one linear write/read/delete scenario
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "rt-pypi", "pypi")

	sum := testSHA("pypi-sdist")
	sdist := &models.PyPIPackage{
		RepositoryID: s.repo, Name: "demo-pkg", Version: "1.0", Filename: "demo_pkg-1.0.tar.gz", PackageType: "sdist",
		RequiresPython: ">=3.9", Summary: "S", Author: "A", AuthorEmail: "a@x", License: "MIT", Keywords: "k",
		Classifiers: []string{"c1", "c2"}, SHA256Digest: sum, BlobKey: sum, SizeBytes: 11,
	}

	if err := pg.CreatePyPIPackage(ctx, sdist); err != nil {
		t.Fatalf("CreatePyPIPackage sdist: %v", err)
	}

	wheelSum := testSHA("pypi-wheel")
	wheel := &models.PyPIPackage{
		RepositoryID: s.repo, Name: "demo-pkg", Version: "1.0", Filename: "demo_pkg-1.0-py3-none-any.whl", PackageType: "bdist_wheel",
		PythonVersion: "py3", Summary: "S", Author: "A", AuthorEmail: "a@x", License: "MIT", Keywords: "k",
		Classifiers: []string{"c1", "c2"}, SHA256Digest: wheelSum, BlobKey: wheelSum, SizeBytes: 13,
	}

	if err := pg.CreatePyPIPackage(ctx, wheel); err != nil {
		t.Fatalf("CreatePyPIPackage wheel: %v", err)
	}

	if sdist.ID == "" || sdist.ID == wheel.ID || sdist.CreatedAt.IsZero() {
		t.Fatalf("ids/timestamps not filled: %+v %+v", sdist, wheel)
	}

	if got := pypiCountGeneric(t, s); got != (pypiGenericCounts{1, 1, 2}) {
		t.Fatalf("generic rows = %+v, want 1 package / 1 version / 2 files", got)
	}

	// The generic rows.
	var path, arch, unit, attrs, sums string

	err := pg.GetDB().QueryRowContext(ctx,
		`SELECT path, arch, unit_key, attrs::text, checksums::text FROM package_files WHERE id = $1`, wheel.ID,
	).Scan(&path, &arch, &unit, &attrs, &sums)
	if err != nil {
		t.Fatal(err)
	}

	if path != "demo-pkg/1.0/demo_pkg-1.0-py3-none-any.whl" || unit != path || arch != "py3" || sums != "{}" ||
		!strings.Contains(attrs, `"bdist_wheel"`) {
		t.Fatalf("file row = path %q unit %q arch %q attrs %s sums %s", path, unit, arch, attrs, sums)
	}

	// Typed reads.
	got, err := pg.GetPyPIPackageByFilename(ctx, s.repo, wheel.Filename)
	if err != nil || got == nil {
		t.Fatalf("GetPyPIPackageByFilename = %v, %v", got, err)
	}

	if got.ID != wheel.ID || got.Name != "demo-pkg" || got.Version != "1.0" || got.PythonVersion != "py3" || got.PackageType != "bdist_wheel" ||
		got.Summary != "S" || got.Author != "A" || got.AuthorEmail != "a@x" || got.License != "MIT" || got.Keywords != "k" ||
		!slices.Equal(got.Classifiers, []string{"c1", "c2"}) || got.SHA256Digest != wheelSum || got.BlobKey != wheelSum ||
		got.SizeBytes != 13 ||
		got.RequiresPython != "" {
		t.Fatalf("round trip = %+v", got)
	}

	sd, err := pg.GetPyPIPackageByFilename(ctx, s.repo, sdist.Filename)
	if err != nil || sd == nil || sd.RequiresPython != ">=3.9" || sd.PythonVersion != "" {
		t.Fatalf("sdist = %+v, %v", sd, err)
	}

	list, err := pg.ListPyPIPackageVersions(ctx, s.repo, "demo-pkg")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListPyPIPackageVersions = %d rows, %v", len(list), err)
	}

	names, err := pg.ListPyPIPackageNames(ctx, s.repo)
	if err != nil || !slices.Equal(names, []string{"demo-pkg"}) {
		t.Fatalf("names = %v, %v", names, err)
	}

	// A duplicate file is ErrDuplicate and leaves nothing behind.
	if err := pg.CreatePyPIPackage(ctx, &models.PyPIPackage{
		RepositoryID: s.repo, Name: "demo-pkg", Version: "1.0", Filename: sdist.Filename, PackageType: "sdist",
		SHA256Digest: sum, BlobKey: sum, SizeBytes: 11,
	}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate upload = %v, want ErrDuplicate", err)
	}

	if got := pypiCountGeneric(t, s); got != (pypiGenericCounts{1, 1, 2}) {
		t.Fatalf("after duplicate = %+v", got)
	}

	// A later file of the release rewrites the release metadata, never resets it.
	if v, err := pg.GetPyPIPackageByFilename(ctx, s.repo, sdist.Filename); err != nil || v.Summary != "S" {
		t.Fatalf("release metadata after second file: %+v, %v", v, err)
	}

	// Deleting removes the file, then the empty version and package; the quota
	// ledger gets the file's usage back.
	if err := pg.DeletePyPIPackage(ctx, s.repo, "demo-pkg", "1.0", wheel.Filename); err != nil {
		t.Fatal(err)
	}

	if got := pypiCountGeneric(t, s); got != (pypiGenericCounts{1, 1, 1}) {
		t.Fatalf("after first delete = %+v", got)
	}

	if err := pg.DeletePyPIPackage(ctx, s.repo, "demo-pkg", "1.0", sdist.Filename); err != nil {
		t.Fatal(err)
	}

	if got := pypiCountGeneric(t, s); got != (pypiGenericCounts{}) {
		t.Fatalf("after last delete = %+v", got)
	}

	if size, count := pendingRelease(t, s); size != 24 || count != 2 {
		t.Fatalf("released usage = %d bytes / %d files, want 24 / 2", size, count)
	}

	if names, _ := pg.ListPyPIPackageNames(ctx, s.repo); len(names) != 0 {
		t.Fatalf("names after delete = %v", names)
	}
}

func TestCondaRoundTrip(t *testing.T) { //nolint:gocyclo // one linear write/read/delete scenario
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "rt-conda", "conda")

	sum := testSHA("conda-bz2")
	bz2 := &models.CondaPackage{
		RepositoryID: s.repo, Name: "Numpy", Version: "1.2", BuildString: "py39_0", Platform: "linux-64",
		BuildNumber: 3, Timestamp: 1700000000000, Depends: []string{"python >=3.9", "libgcc"},
		Extra: []byte(`{"license":"BSD","noarch":"python"}`), SHA256: sum, BlobKey: sum, SizeBytes: 9, MD5: "abc123",
	}

	if err := pg.CreateCondaPackage(ctx, bz2); err != nil {
		t.Fatalf("CreateCondaPackage: %v", err)
	}

	if bz2.Extension != models.CondaExtTarBz2 {
		t.Fatalf("default extension = %q", bz2.Extension)
	}

	conda := addCondaBuild(t, pg, s.repo, "Numpy", "1.2", "py39_0", "linux-64", models.CondaExtConda)
	other := addCondaBuild(t, pg, s.repo, "Numpy", "1.3", "py39_0", "noarch", models.CondaExtConda)

	// One package, two builds, three files: the two extensions of a build are files of one version.
	if got := pypiCountGeneric(t, s); got != (pypiGenericCounts{1, 2, 3}) {
		t.Fatalf("generic rows = %+v, want 1 / 2 / 3", got)
	}

	var path, arch string

	if err := pg.GetDB().QueryRowContext(ctx, `SELECT path, arch FROM package_files WHERE id = $1`, bz2.ID).Scan(&path, &arch); err != nil {
		t.Fatal(err)
	}

	if path != "linux-64/Numpy-1.2-py39_0.tar.bz2" || arch != "linux-64" {
		t.Fatalf("file row = %q %q", path, arch)
	}

	var versionKey string

	if err := pg.GetDB().QueryRowContext(ctx,
		`SELECT v.version_key FROM package_versions v JOIN package_files f ON f.version_id = v.id WHERE f.id = $1`, bz2.ID).
		Scan(&versionKey); err != nil {
		t.Fatal(err)
	}

	if versionKey != "1.2-py39_0|linux-64" {
		t.Fatalf("version_key = %q", versionKey)
	}

	got, err := pg.GetCondaPackageFile(ctx, s.repo, "Numpy", "1.2", "py39_0", "linux-64", models.CondaExtTarBz2)
	if err != nil || got == nil {
		t.Fatalf("GetCondaPackageFile = %v, %v", got, err)
	}

	if got.ID != bz2.ID || got.Name != "Numpy" || got.BuildString != "py39_0" || got.Platform != "linux-64" ||
		got.Extension != models.CondaExtTarBz2 || got.BuildNumber != 3 || got.Timestamp != 1700000000000 ||
		!slices.Equal(got.Depends, []string{"python >=3.9", "libgcc"}) || got.MD5 != "abc123" ||
		got.SHA256 != sum || got.BlobKey != sum || got.SizeBytes != 9 || !strings.Contains(string(got.Extra), `"BSD"`) {
		t.Fatalf("round trip = %+v", got)
	}

	if missing, err := pg.GetCondaPackageFile(ctx, s.repo, "Numpy", "1.2", "py39_0", "linux-64", ".other"); err != nil || missing != nil {
		t.Fatalf("missing extension = %v, %v", missing, err)
	}

	// Listing of a subdir: both extensions of the build, none of the other platform.
	list, err := pg.ListCondaPackages(ctx, s.repo, "linux-64")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListCondaPackages = %d, %v", len(list), err)
	}

	if list[0].Extension != models.CondaExtConda || list[1].Extension != models.CondaExtTarBz2 {
		t.Fatalf("order = %s, %s", list[0].Extension, list[1].Extension)
	}

	if got, _ := pg.GetCondaPackageFile(
		ctx,
		s.repo,
		"Numpy",
		"1.2",
		"py39_0",
		"linux-64",
		models.CondaExtConda,
	); got == nil ||
		got.ID != conda.ID {
		t.Fatalf("conda file = %+v", got)
	}

	// Duplicate -> ErrDuplicate, nothing written.
	dup := &models.CondaPackage{
		RepositoryID: s.repo,
		Name:         "Numpy",
		Version:      "1.2",
		BuildString:  "py39_0",
		Platform:     "linux-64",
		SHA256:       sum,
		BlobKey:      sum,
		SizeBytes:    9,
	}
	if err := pg.CreateCondaPackage(ctx, dup); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate = %v, want ErrDuplicate", err)
	}

	if got := pypiCountGeneric(t, s); got != (pypiGenericCounts{1, 2, 3}) {
		t.Fatalf("after duplicate = %+v", got)
	}

	// Name probes read packages, case-insensitively.
	if ok, err := pg.HasCondaPackageName(ctx, s.repo, "numpy"); err != nil || !ok {
		t.Fatalf("HasCondaPackageName = %v, %v", ok, err)
	}

	stampA, err := pg.CondaRepodataStamp(ctx, s.repo, "linux-64")
	if err != nil || !strings.HasPrefix(stampA, "2:") {
		t.Fatalf("stamp = %q, %v", stampA, err)
	}

	// Delete one extension: the build stays while the other file lives.
	if err := pg.DeleteCondaPackage(ctx, bz2.ID); err != nil {
		t.Fatal(err)
	}

	if got := pypiCountGeneric(t, s); got != (pypiGenericCounts{1, 2, 2}) {
		t.Fatalf("after deleting one extension = %+v", got)
	}

	if err := pg.DeleteCondaPackage(ctx, conda.ID); err != nil {
		t.Fatal(err)
	}

	if got := pypiCountGeneric(t, s); got != (pypiGenericCounts{1, 1, 1}) {
		t.Fatalf("after deleting the build = %+v", got)
	}

	if err := pg.DeleteCondaPackage(ctx, other.ID); err != nil {
		t.Fatal(err)
	}

	if got := pypiCountGeneric(t, s); got != (pypiGenericCounts{}) {
		t.Fatalf("after last delete = %+v", got)
	}

	if size, count := pendingRelease(t, s); size != 19 || count != 3 {
		t.Fatalf("released usage = %d bytes / %d files, want 19 / 3", size, count)
	}
}

func TestRawRoundTrip(t *testing.T) { //nolint:gocyclo // one linear write/read/delete scenario
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "rt-raw", "raw")

	put := func(path, content string) *models.RawArtifact {
		t.Helper()

		sum := testSHA(content)
		a := &models.RawArtifact{
			ID: testUUID(path + content), RepositoryID: s.repo, Path: path, ContentType: "text/plain",
			SHA256: sum, BlobKey: sum, SizeBytes: int64(len(content)), Metadata: map[string]any{"k": "v"},
		}

		if err := pg.UpsertRawArtifact(ctx, a); err != nil {
			t.Fatalf("UpsertRawArtifact %s: %v", path, err)
		}

		return a
	}

	first := put("docs/a.txt", "one")
	put("docs/b.txt", "two")
	put("docs/sub/c.txt", "three")
	put("other/d.txt", "four")
	put("docs_x/e.txt", "five") // LIKE wildcard must not widen the prefix

	if got := pypiCountGeneric(t, s); got != (pypiGenericCounts{5, 5, 5}) {
		t.Fatalf("generic rows = %+v", got)
	}

	got, err := pg.GetRawArtifact(ctx, s.repo, "docs/a.txt")
	if err != nil || got == nil {
		t.Fatalf("GetRawArtifact = %v, %v", got, err)
	}

	if got.ID != first.ID || got.ContentType != "text/plain" || got.SHA256 != testSHA("one") || got.SizeBytes != 3 ||
		got.Metadata["k"] != "v" || got.BlobID == "" || got.CreatedAt.IsZero() {
		t.Fatalf("round trip = %+v", got)
	}

	var filename, unit, role, version string

	if err := pg.GetDB().QueryRowContext(ctx,
		`SELECT f.filename, f.unit_key, f.role, v.version_key FROM package_files f JOIN package_versions v ON v.id = f.version_id WHERE f.id = $1`,
		first.ID).
		Scan(&filename, &unit, &role, &version); err != nil {
		t.Fatal(err)
	}

	if filename != "docs/a.txt" || unit != "docs/a.txt" || role != "primary" || version != "" {
		t.Fatalf("file row = %q %q %q %q", filename, unit, role, version)
	}

	all, err := pg.ListRawArtifacts(ctx, s.repo, "")
	if err != nil || len(all) != 5 || all[0].Path != "docs/a.txt" {
		t.Fatalf("ListRawArtifacts = %d, %v", len(all), err)
	}

	byPrefix, err := pg.ListRawArtifacts(ctx, s.repo, "docs/")
	if err != nil || len(byPrefix) != 3 {
		t.Fatalf("prefix docs/ = %d, %v", len(byPrefix), err)
	}

	if wild, _ := pg.ListRawArtifacts(ctx, s.repo, "docs_"); len(wild) != 1 || wild[0].Path != "docs_x/e.txt" {
		t.Fatalf("prefix docs_ = %+v", wild)
	}

	// Overwrite keeps the file id and created_at, replaces blob and content type.
	over := &models.RawArtifact{
		ID: testUUID("other"), RepositoryID: s.repo, Path: "docs/a.txt", ContentType: "text/markdown",
		SHA256: testSHA("uno"), BlobKey: testSHA("uno"), SizeBytes: 3,
	}
	if err := pg.UpsertRawArtifact(ctx, over); err != nil {
		t.Fatal(err)
	}

	after, _ := pg.GetRawArtifact(ctx, s.repo, "docs/a.txt")
	if after.ID != first.ID || !after.CreatedAt.Equal(got.CreatedAt) || after.SHA256 != testSHA("uno") ||
		after.ContentType != "text/markdown" {
		t.Fatalf("overwrite = %+v (was %+v)", after, got)
	}

	if got := pypiCountGeneric(t, s); got != (pypiGenericCounts{5, 5, 5}) {
		t.Fatalf("after overwrite = %+v", got)
	}

	// If-absent: refused on a taken path, accepted on a free one.
	ins, err := pg.InsertRawArtifactIfAbsent(ctx, &models.RawArtifact{
		ID: testUUID(
			"x1",
		),
		RepositoryID: s.repo,
		Path:         "docs/a.txt",
		ContentType:  "x",
		SHA256:       testSHA("z"),
		BlobKey:      testSHA("z"),
		SizeBytes:    1,
	})
	if err != nil || ins {
		t.Fatalf("InsertRawArtifactIfAbsent on taken path = %v, %v", ins, err)
	}

	ins, err = pg.InsertRawArtifactIfAbsent(ctx, &models.RawArtifact{
		ID: testUUID(
			"x2",
		),
		RepositoryID: s.repo,
		Path:         "new.txt",
		ContentType:  "x",
		SHA256:       testSHA("z"),
		BlobKey:      testSHA("z"),
		SizeBytes:    1,
	})
	if err != nil || !ins {
		t.Fatalf("InsertRawArtifactIfAbsent on free path = %v, %v", ins, err)
	}

	// Delete removes file, version and package.
	if err := pg.DeleteRawArtifact(ctx, s.repo, "new.txt"); err != nil {
		t.Fatal(err)
	}

	if got := pypiCountGeneric(t, s); got != (pypiGenericCounts{5, 5, 5}) {
		t.Fatalf("after delete = %+v", got)
	}

	if g, err := pg.GetRawArtifact(ctx, s.repo, "new.txt"); err != nil || g != nil {
		t.Fatalf("deleted artifact = %v, %v", g, err)
	}

	if size, count := pendingRelease(t, s); size != 1 || count != 1 {
		t.Fatalf("released usage = %d bytes / %d files, want 1 / 1", size, count)
	}
}

// A directory listing is an index scan bounded by the repository
// (uq_package_files_path or idx_package_files_filename, both lead with
// repository_id and raw filename = path); like the former (repository_id, path)
// index it cannot turn LIKE into a range under a non-C collation, so the
// prefix is a filter on the repository's index range.
func TestRawPrefixListingUsesPathIndex(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "rt-raw-plan", "raw")

	for i := range 300 {
		path := fmt.Sprintf("dir%d/file%d.txt", i%30, i)
		sum := testSHA(strconv.Itoa(i))

		if err := pg.UpsertRawArtifact(ctx, &models.RawArtifact{
			ID: testUUID(path + sum), RepositoryID: s.repo, Path: path, SHA256: sum, BlobKey: sum, SizeBytes: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}

	s.exec(`ANALYZE package_files`)

	tx, err := pg.GetDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}

	rows, err := tx.QueryContext(ctx, `EXPLAIN SELECT f.id FROM package_files f
	  WHERE f.repository_id = $1 AND f.path LIKE 'dir1/%' AND f.deleted_at IS NULL ORDER BY f.path`, s.repo)
	if err != nil {
		t.Fatal(err)
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

	if strings.Contains(plan.String(), "Seq Scan") || !strings.Contains(plan.String(), "idx_package_files_path_prefix") {
		t.Errorf("prefix listing does not use idx_package_files_path_prefix:\n%s", plan.String())
	}
}
