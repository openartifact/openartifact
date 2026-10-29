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
	"reflect"
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

// requireGenericCounts fails unless the repository holds exactly want generic rows.
func requireGenericCounts(t *testing.T, s *quotaSeeder, what string, want pypiGenericCounts) {
	t.Helper()

	if got := pypiCountGeneric(t, s); got != want {
		t.Fatalf("%s: generic rows = %+v, want %+v", what, got, want)
	}
}

// requireLedgerRelease fails unless the removed files handed back exactly this usage.
func requireLedgerRelease(t *testing.T, s *quotaSeeder, wantSize, wantCount int64) {
	t.Helper()

	if size, count := pendingRelease(t, s); size != wantSize || count != wantCount {
		t.Fatalf("released usage = %d bytes / %d files, want %d / %d", size, count, wantSize, wantCount)
	}
}

// pypiFixture is the two files of one release the pypi round trip works on.
type pypiFixture struct {
	pg           *PostgresDB
	s            *quotaSeeder
	sdist, wheel *models.PyPIPackage
}

func newPyPIFixture(t *testing.T) *pypiFixture {
	t.Helper()

	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "rt-pypi", "pypi")

	release := models.PyPIPackage{
		RepositoryID: s.repo, Name: "demo-pkg", Version: "1.0",
		Summary: "S", Author: "A", AuthorEmail: "a@x", License: "MIT", Keywords: "k", Classifiers: []string{"c1", "c2"},
	}

	sdist, wheel := release, release
	sdist.Filename, sdist.PackageType, sdist.RequiresPython = "demo_pkg-1.0.tar.gz", "sdist", ">=3.9"
	sdist.SHA256Digest, sdist.SizeBytes = testSHA("pypi-sdist"), 11
	wheel.Filename, wheel.PackageType, wheel.PythonVersion = "demo_pkg-1.0-py3-none-any.whl", "bdist_wheel", "py3"
	wheel.SHA256Digest, wheel.SizeBytes = testSHA("pypi-wheel"), 13
	sdist.BlobKey, wheel.BlobKey = sdist.SHA256Digest, wheel.SHA256Digest

	for _, p := range []*models.PyPIPackage{&sdist, &wheel} {
		if err := pg.CreatePyPIPackage(ctx, p); err != nil {
			t.Fatalf("CreatePyPIPackage %s: %v", p.Filename, err)
		}
	}

	return &pypiFixture{pg: pg, s: s, sdist: &sdist, wheel: &wheel}
}

func (f *pypiFixture) requireGenericRows(t *testing.T) {
	t.Helper()

	if f.sdist.ID == "" || f.sdist.ID == f.wheel.ID || f.sdist.CreatedAt.IsZero() {
		t.Fatalf("ids/timestamps not filled: %+v %+v", f.sdist, f.wheel)
	}

	requireGenericCounts(t, f.s, "after two uploads", pypiGenericCounts{1, 1, 2})

	var path, arch, unit, attrs, sums string

	err := f.pg.GetDB().QueryRowContext(context.Background(),
		`SELECT path, arch, unit_key, attrs::text, checksums::text FROM package_files WHERE id = $1`, f.wheel.ID,
	).Scan(&path, &arch, &unit, &attrs, &sums)
	if err != nil {
		t.Fatal(err)
	}

	if path != "demo-pkg/1.0/demo_pkg-1.0-py3-none-any.whl" || unit != path || arch != "py3" || sums != "{}" ||
		!strings.Contains(attrs, `"bdist_wheel"`) {
		t.Fatalf("file row = path %q unit %q arch %q attrs %s sums %s", path, unit, arch, attrs, sums)
	}
}

func (f *pypiFixture) requireTypedReads(t *testing.T) {
	t.Helper()

	ctx := context.Background()

	got, err := f.pg.GetPyPIPackageByFilename(ctx, f.s.repo, f.wheel.Filename)
	if err != nil || got == nil {
		t.Fatalf("GetPyPIPackageByFilename = %v, %v", got, err)
	}

	// Everything the wheel was stored with, read back (ids and timestamps aside).
	want := *f.wheel
	want.CreatedAt, want.UpdatedAt = got.CreatedAt, got.UpdatedAt

	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}

	sd, err := f.pg.GetPyPIPackageByFilename(ctx, f.s.repo, f.sdist.Filename)
	if err != nil || sd == nil || sd.RequiresPython != ">=3.9" || sd.PythonVersion != "" || sd.Summary != "S" {
		t.Fatalf("sdist = %+v, %v", sd, err)
	}

	list, err := f.pg.ListPyPIPackageVersions(ctx, f.s.repo, "demo-pkg")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListPyPIPackageVersions = %d rows, %v", len(list), err)
	}

	names, err := f.pg.ListPyPIPackageNames(ctx, f.s.repo)
	if err != nil || !slices.Equal(names, []string{"demo-pkg"}) {
		t.Fatalf("names = %v, %v", names, err)
	}
}

func (f *pypiFixture) requireDuplicateRefused(t *testing.T) {
	t.Helper()

	dup := *f.sdist
	dup.ID = ""

	if err := f.pg.CreatePyPIPackage(context.Background(), &dup); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate upload = %v, want ErrDuplicate", err)
	}

	requireGenericCounts(t, f.s, "after duplicate", pypiGenericCounts{1, 1, 2})
}

func (f *pypiFixture) requireDeletes(t *testing.T) {
	t.Helper()

	ctx := context.Background()

	// Deleting removes the file, then the empty version and package; the quota
	// ledger gets the files' usage back.
	for i, p := range []*models.PyPIPackage{f.wheel, f.sdist} {
		if err := f.pg.DeletePyPIPackage(ctx, f.s.repo, p.Name, p.Version, p.Filename); err != nil {
			t.Fatal(err)
		}

		want := pypiGenericCounts{1, 1, 1}
		if i == 1 {
			want = pypiGenericCounts{}
		}

		requireGenericCounts(t, f.s, "after delete of "+p.Filename, want)
	}

	requireLedgerRelease(t, f.s, 24, 2)

	if names, _ := f.pg.ListPyPIPackageNames(ctx, f.s.repo); len(names) != 0 {
		t.Fatalf("names after delete = %v", names)
	}
}

func TestPyPIRoundTrip(t *testing.T) {
	f := newPyPIFixture(t)

	// The steps share the fixture and run in order.
	for _, step := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"generic rows", f.requireGenericRows},
		{"typed reads", f.requireTypedReads},
		{"duplicate", f.requireDuplicateRefused},
		{"delete", f.requireDeletes},
	} {
		t.Run(step.name, step.run)
	}
}

// condaFixture is one package with a build in two extensions and a second build
// on another platform.
type condaFixture struct {
	pg                *PostgresDB
	s                 *quotaSeeder
	bz2, conda, other *models.CondaPackage
	bz2SHA            string
}

func newCondaFixture(t *testing.T) *condaFixture {
	t.Helper()

	pg := newQuotaTestDB(t, 4)
	s := newQuotaSeeder(t, pg, "rt-conda", "conda")

	sum := testSHA("conda-bz2")
	bz2 := &models.CondaPackage{
		RepositoryID: s.repo, Name: "Numpy", Version: "1.2", BuildString: "py39_0", Platform: "linux-64",
		BuildNumber: 3, Timestamp: 1700000000000, Depends: []string{"python >=3.9", "libgcc"},
		Extra: []byte(`{"license":"BSD","noarch":"python"}`), SHA256: sum, BlobKey: sum, SizeBytes: 9, MD5: "abc123",
	}

	if err := pg.CreateCondaPackage(context.Background(), bz2); err != nil {
		t.Fatalf("CreateCondaPackage: %v", err)
	}

	return &condaFixture{
		pg: pg, s: s, bz2: bz2, bz2SHA: sum,
		conda: addCondaBuild(t, pg, s.repo, "Numpy", "1.2", "py39_0", "linux-64", models.CondaExtConda),
		other: addCondaBuild(t, pg, s.repo, "Numpy", "1.3", "py39_0", "noarch", models.CondaExtConda),
	}
}

func (f *condaFixture) requireGenericRows(t *testing.T) {
	t.Helper()

	if f.bz2.Extension != models.CondaExtTarBz2 {
		t.Fatalf("default extension = %q", f.bz2.Extension)
	}

	// One package, two builds, three files: the two extensions of a build are files of one version.
	requireGenericCounts(t, f.s, "after three uploads", pypiGenericCounts{1, 2, 3})

	var path, arch, versionKey string

	err := f.pg.GetDB().QueryRowContext(context.Background(),
		`SELECT f.path, f.arch, v.version_key FROM package_files f JOIN package_versions v ON v.id = f.version_id WHERE f.id = $1`,
		f.bz2.ID).Scan(&path, &arch, &versionKey)
	if err != nil {
		t.Fatal(err)
	}

	if path != "linux-64/Numpy-1.2-py39_0.tar.bz2" || arch != "linux-64" || versionKey != "1.2-py39_0|linux-64" {
		t.Fatalf("file row = %q %q, version_key %q", path, arch, versionKey)
	}
}

func (f *condaFixture) requireTypedReads(t *testing.T) {
	t.Helper()

	ctx := context.Background()

	got, err := f.pg.GetCondaPackageFile(ctx, f.s.repo, "Numpy", "1.2", "py39_0", "linux-64", models.CondaExtTarBz2)
	if err != nil || got == nil {
		t.Fatalf("GetCondaPackageFile = %v, %v", got, err)
	}

	if got.ID != f.bz2.ID || got.BuildString != "py39_0" || got.Platform != "linux-64" || got.BuildNumber != 3 ||
		got.Timestamp != 1700000000000 || !slices.Equal(got.Depends, []string{"python >=3.9", "libgcc"}) ||
		got.MD5 != "abc123" || got.SHA256 != f.bz2SHA || got.BlobKey != f.bz2SHA || got.SizeBytes != 9 ||
		!strings.Contains(string(got.Extra), `"BSD"`) {
		t.Fatalf("round trip = %+v", got)
	}

	if missing, err := f.pg.GetCondaPackageFile(
		ctx,
		f.s.repo,
		"Numpy",
		"1.2",
		"py39_0",
		"linux-64",
		".other",
	); err != nil ||
		missing != nil {
		t.Fatalf("missing extension = %v, %v", missing, err)
	}

	byExt, err := f.pg.GetCondaPackageFile(ctx, f.s.repo, "Numpy", "1.2", "py39_0", "linux-64", models.CondaExtConda)
	if err != nil || byExt == nil || byExt.ID != f.conda.ID {
		t.Fatalf("conda file = %+v, %v", byExt, err)
	}
}

func (f *condaFixture) requireListing(t *testing.T) {
	t.Helper()

	// Listing of a subdir: both extensions of the build, none of the other platform.
	list, err := f.pg.ListCondaPackages(context.Background(), f.s.repo, "linux-64")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListCondaPackages = %d, %v", len(list), err)
	}

	if list[0].Extension != models.CondaExtConda || list[1].Extension != models.CondaExtTarBz2 {
		t.Fatalf("order = %s, %s", list[0].Extension, list[1].Extension)
	}
}

func (f *condaFixture) requireDuplicateAndProbes(t *testing.T) {
	t.Helper()

	ctx := context.Background()

	// Duplicate -> ErrDuplicate, nothing written.
	dup := *f.bz2
	dup.ID = ""

	if err := f.pg.CreateCondaPackage(ctx, &dup); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate = %v, want ErrDuplicate", err)
	}

	requireGenericCounts(t, f.s, "after duplicate", pypiGenericCounts{1, 2, 3})

	// Name probes read packages, case-insensitively.
	if ok, err := f.pg.HasCondaPackageName(ctx, f.s.repo, "numpy"); err != nil || !ok {
		t.Fatalf("HasCondaPackageName = %v, %v", ok, err)
	}

	stamp, err := f.pg.CondaRepodataStamp(ctx, f.s.repo, "linux-64")
	if err != nil || !strings.HasPrefix(stamp, "2:") {
		t.Fatalf("stamp = %q, %v", stamp, err)
	}
}

func (f *condaFixture) requireDeletes(t *testing.T) {
	t.Helper()

	// Deleting one extension keeps the build while the other file lives; the
	// last file takes the build, then the package.
	for _, step := range []struct {
		file *models.CondaPackage
		want pypiGenericCounts
	}{
		{f.bz2, pypiGenericCounts{1, 2, 2}},
		{f.conda, pypiGenericCounts{1, 1, 1}},
		{f.other, pypiGenericCounts{}},
	} {
		if err := f.pg.DeleteCondaPackage(context.Background(), step.file.ID); err != nil {
			t.Fatal(err)
		}

		requireGenericCounts(t, f.s, "after deleting "+step.file.Filename(), step.want)
	}

	requireLedgerRelease(t, f.s, 19, 3)
}

func TestCondaRoundTrip(t *testing.T) {
	f := newCondaFixture(t)

	for _, step := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"generic rows", f.requireGenericRows},
		{"typed reads", f.requireTypedReads},
		{"listing", f.requireListing},
		{"duplicate and probes", f.requireDuplicateAndProbes},
		{"delete", f.requireDeletes},
	} {
		t.Run(step.name, step.run)
	}
}

// rawFixture is five raw files in three directories.
type rawFixture struct {
	pg    *PostgresDB
	s     *quotaSeeder
	first *models.RawArtifact
	read  *models.RawArtifact
}

func (f *rawFixture) put(t *testing.T, path, content string) *models.RawArtifact {
	t.Helper()

	sum := testSHA(content)
	a := &models.RawArtifact{
		ID: testUUID(path + content), RepositoryID: f.s.repo, Path: path, ContentType: "text/plain",
		SHA256: sum, BlobKey: sum, SizeBytes: int64(len(content)), Metadata: map[string]any{"k": "v"},
	}

	if err := f.pg.UpsertRawArtifact(context.Background(), a); err != nil {
		t.Fatalf("UpsertRawArtifact %s: %v", path, err)
	}

	return a
}

func newRawFixture(t *testing.T) *rawFixture {
	t.Helper()

	pg := newQuotaTestDB(t, 4)
	f := &rawFixture{pg: pg, s: newQuotaSeeder(t, pg, "rt-raw", "raw")}

	f.first = f.put(t, "docs/a.txt", "one")
	f.put(t, "docs/b.txt", "two")
	f.put(t, "docs/sub/c.txt", "three")
	f.put(t, "other/d.txt", "four")
	f.put(t, "docs_x/e.txt", "five") // LIKE wildcard must not widen the prefix

	return f
}

func (f *rawFixture) requireGenericRows(t *testing.T) {
	t.Helper()

	requireGenericCounts(t, f.s, "after five uploads", pypiGenericCounts{5, 5, 5})

	got, err := f.pg.GetRawArtifact(context.Background(), f.s.repo, "docs/a.txt")
	if err != nil || got == nil {
		t.Fatalf("GetRawArtifact = %v, %v", got, err)
	}

	if got.ID != f.first.ID || got.ContentType != "text/plain" || got.SHA256 != testSHA("one") || got.SizeBytes != 3 ||
		got.Metadata["k"] != "v" || got.BlobID == "" || got.CreatedAt.IsZero() {
		t.Fatalf("round trip = %+v", got)
	}

	f.read = got

	var filename, unit, role, version string

	err = f.pg.GetDB().QueryRowContext(context.Background(),
		`SELECT f.filename, f.unit_key, f.role, v.version_key FROM package_files f JOIN package_versions v ON v.id = f.version_id WHERE f.id = $1`,
		f.first.ID).
		Scan(&filename, &unit, &role, &version)
	if err != nil {
		t.Fatal(err)
	}

	if filename != "docs/a.txt" || unit != "docs/a.txt" || role != "primary" || version != "" {
		t.Fatalf("file row = %q %q %q %q", filename, unit, role, version)
	}
}

func (f *rawFixture) requireListings(t *testing.T) {
	t.Helper()

	ctx := context.Background()

	all, err := f.pg.ListRawArtifacts(ctx, f.s.repo, "")
	if err != nil || len(all) != 5 || all[0].Path != "docs/a.txt" {
		t.Fatalf("ListRawArtifacts = %d, %v", len(all), err)
	}

	for prefix, want := range map[string]int{"docs/": 3, "docs_": 1, "nothing/": 0} {
		got, err := f.pg.ListRawArtifacts(ctx, f.s.repo, prefix)
		if err != nil || len(got) != want {
			t.Fatalf("prefix %q = %d rows, %v; want %d", prefix, len(got), err, want)
		}
	}
}

func (f *rawFixture) requireOverwrite(t *testing.T) {
	t.Helper()

	ctx := context.Background()

	// Overwrite keeps the file id and created_at, replaces blob and content type.
	over := &models.RawArtifact{
		ID: testUUID("other"), RepositoryID: f.s.repo, Path: "docs/a.txt", ContentType: "text/markdown",
		SHA256: testSHA("uno"), BlobKey: testSHA("uno"), SizeBytes: 3,
	}
	if err := f.pg.UpsertRawArtifact(ctx, over); err != nil {
		t.Fatal(err)
	}

	after, err := f.pg.GetRawArtifact(ctx, f.s.repo, "docs/a.txt")
	if err != nil || after == nil {
		t.Fatalf("GetRawArtifact = %v, %v", after, err)
	}

	if after.ID != f.first.ID || !after.CreatedAt.Equal(f.read.CreatedAt) || after.SHA256 != testSHA("uno") ||
		after.ContentType != "text/markdown" {
		t.Fatalf("overwrite = %+v (was %+v)", after, f.read)
	}

	requireGenericCounts(t, f.s, "after overwrite", pypiGenericCounts{5, 5, 5})
}

func (f *rawFixture) requireIfAbsentAndDelete(t *testing.T) {
	t.Helper()

	ctx := context.Background()
	zSum := testSHA("z")

	// If-absent: refused on a taken path, accepted on a free one.
	for _, step := range []struct {
		path string
		want bool
	}{{"docs/a.txt", false}, {"new.txt", true}} {
		inserted, err := f.pg.InsertRawArtifactIfAbsent(ctx, &models.RawArtifact{
			ID: testUUID("x" + step.path), RepositoryID: f.s.repo, Path: step.path, ContentType: "x",
			SHA256: zSum, BlobKey: zSum, SizeBytes: 1,
		})
		if err != nil || inserted != step.want {
			t.Fatalf("InsertRawArtifactIfAbsent(%s) = %v, %v; want %v", step.path, inserted, err, step.want)
		}
	}

	// Delete removes file, version and package.
	if err := f.pg.DeleteRawArtifact(ctx, f.s.repo, "new.txt"); err != nil {
		t.Fatal(err)
	}

	requireGenericCounts(t, f.s, "after delete", pypiGenericCounts{5, 5, 5})

	if g, err := f.pg.GetRawArtifact(ctx, f.s.repo, "new.txt"); err != nil || g != nil {
		t.Fatalf("deleted artifact = %v, %v", g, err)
	}

	requireLedgerRelease(t, f.s, 1, 1)
}

func TestRawRoundTrip(t *testing.T) {
	f := newRawFixture(t)

	for _, step := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"generic rows", f.requireGenericRows},
		{"listings", f.requireListings},
		{"overwrite", f.requireOverwrite},
		{"if absent and delete", f.requireIfAbsentAndDelete},
	} {
		t.Run(step.name, step.run)
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
