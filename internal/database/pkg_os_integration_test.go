// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// newOSRepo creates a hosted repository of the given format with a quota row,
// so the usage counters follow the package files.
func newOSRepo(tb testing.TB, pg *PostgresDB, name, format string) string {
	tb.Helper()

	ctx := context.Background()
	org := systemOrgID

	repo := &models.Repository{Name: name, Format: format, RepoType: "hosted", ProjectID: &org}
	if err := pg.CreateRepository(ctx, repo); err != nil {
		tb.Fatalf("CreateRepository: %v", err)
	}

	q, err := pg.GetOrCreateStorageQuota(ctx, repo.ID)
	if err != nil {
		tb.Fatalf("GetOrCreateStorageQuota: %v", err)
	}

	q.MaxSizeBytes, q.MaxArtifactCount = 1<<40, 1<<20
	if err := pg.UpdateStorageQuota(ctx, q); err != nil {
		tb.Fatalf("UpdateStorageQuota: %v", err)
	}

	return repo.ID
}

// osCounts reads the generic row counts of a repository.
func osCounts(tb testing.TB, pg *PostgresDB, repoID string) (int, int, int) {
	tb.Helper()

	var n [3]int

	for i, table := range []string{"packages", "package_versions", "package_files"} {
		if err := pg.GetDB().QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM `+table+` WHERE repository_id = $1`, repoID).Scan(&n[i]); err != nil {
			tb.Fatalf("count %s: %v", table, err)
		}
	}

	return n[0], n[1], n[2]
}

// osFile is the generic row of the only file of a repository.
type osFile struct {
	Path, Filename, Arch, Part, Role, VersionKey, NameKey, VersionText string
	UnitKey                                                            *string
	Attrs, Meta, Checksums                                             string
	Size                                                               *int64
}

func readOSFile(tb testing.TB, pg *PostgresDB, repoID string) osFile {
	tb.Helper()

	var f osFile

	err := pg.GetDB().QueryRowContext(context.Background(), `
		SELECT f.path, COALESCE(f.filename, ''), COALESCE(f.arch, ''), f.part, f.role, f.unit_key,
		       f.attrs::text, f.checksums::text, f.size_bytes,
		       v.version_key, v.version, v.metadata::text, p.name_key
		FROM package_files f
		JOIN package_versions v ON v.id = f.version_id
		JOIN packages p ON p.id = v.package_id
		WHERE f.repository_id = $1`, repoID).
		Scan(&f.Path, &f.Filename, &f.Arch, &f.Part, &f.Role, &f.UnitKey,
			&f.Attrs, &f.Checksums, &f.Size, &f.VersionKey, &f.VersionText, &f.Meta, &f.NameKey)
	if err != nil {
		tb.Fatalf("read file row: %v", err)
	}

	return f
}

func jsonEq(tb testing.TB, what, got, want string) {
	tb.Helper()

	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		tb.Fatalf("%s: %v (%q)", what, err, got)
	}

	if err := json.Unmarshal([]byte(want), &w); err != nil {
		tb.Fatal(err)
	}

	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)

	if string(gb) != string(wb) {
		tb.Errorf("%s = %s, want %s", what, gb, wb)
	}
}

// reserveOS reserves quota the way the upload handlers do before they write a
// package: the file insert then takes the reservation back out of the ledger,
// and deleting the file queues a release (quota_pending) for the reconciler.
func reserveOS(tb testing.TB, pg *PostgresDB, repoID string, size int64) {
	tb.Helper()

	if ok, err := pg.ReserveStorageUsage(context.Background(), repoID, size, 1); err != nil || !ok {
		tb.Fatalf("ReserveStorageUsage = %v, %v", ok, err)
	}
}

func requireOSPending(tb testing.TB, pg *PostgresDB, repoID string, want int) {
	tb.Helper()

	if got := pendingEntries(tb, pg, repoID); got != want {
		tb.Errorf("quota_pending entries = %d, want %d", got, want)
	}
}

func requireOSUsage(tb testing.TB, pg *PostgresDB, repoID string, size, count int64) {
	tb.Helper()

	if b, c, _ := quotaUsage(tb, pg, repoID); b != size || c != count {
		tb.Errorf("quota usage = %d bytes, %d files, want %d, %d", b, c, size, count)
	}
}

// TestDebStoreRoundTrip writes through the typed store and reads every typed read and the
// generic rows back.
//
//nolint:gocyclo,cyclop // one scenario: create, typed reads, duplicates, delete
func TestDebStoreRoundTrip(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	repoID := newOSRepo(t, pg, "deb-rt", "deb")

	dist, err := pg.GetOrCreateDebianDistribution(ctx, repoID, "stable")
	if err != nil {
		t.Fatal(err)
	}

	comp, err := pg.GetOrCreateDebianComponent(ctx, dist.ID, "main")
	if err != nil {
		t.Fatal(err)
	}

	const sha = "00000000000000000000000000000000000000000000000000000000000000d1"

	blobID, err := pg.UpsertBlob(ctx, sha, sha, 100)
	if err != nil {
		t.Fatal(err)
	}

	pkg := &models.Component{
		RepositoryID: repoID, ArtifactID: blobID, Name: "hello", Version: "1:2.10-1", Arch: "amd64",
		Summary: "greeter", Description: "says hello", License: "GPL", Filename: "hello_2.10-1_amd64.deb", InstalledSize: 4096,
	}
	reserveOS(t, pg, repoID, 100)

	if err := pg.CreateDebPackage(ctx, pkg, dist.ID, comp.ID); err != nil {
		t.Fatalf("CreateDebPackage: %v", err)
	}

	if pkg.ID == "" || pkg.CreatedAt.IsZero() || pkg.UpdatedAt.IsZero() {
		t.Fatalf("create did not stamp the package: %+v", pkg)
	}

	control := map[string]string{"Package": "hello", "Section": "utils"}
	if err := pg.SetDebControlFields(ctx, pkg.ID, "Jane <j@x>", "https://x", control); err != nil {
		t.Fatalf("SetDebControlFields: %v", err)
	}

	// Generic rows.
	if p, v, f := osCounts(t, pg, repoID); p != 1 || v != 1 || f != 1 {
		t.Fatalf("rows = %d/%d/%d packages/versions/files, want 1/1/1", p, v, f)
	}

	row := readOSFile(t, pg, repoID)
	if row.Path != "pool/main/h/hello/hello_2.10-1_amd64.deb" || row.Filename != "hello_2.10-1_amd64.deb" || row.Arch != "amd64" ||
		row.Part != dist.ID || row.Role != "primary" || row.UnitKey != nil || row.NameKey != "hello" || row.VersionText != "1:2.10-1" ||
		row.VersionKey != "1:2.10-1|amd64|"+comp.ID || row.Size != nil {
		t.Errorf("unexpected file row: %+v", row)
	}

	jsonEq(t, "attrs", row.Attrs, `{"installed_size": 4096}`)
	jsonEq(t, "checksums", row.Checksums, `{}`)
	jsonEq(t, "metadata", row.Meta,
		`{"summary":"greeter","description":"says hello","license":"GPL","maintainer":"Jane <j@x>","homepage":"https://x"}`)

	var storedControl string
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT control_fields::text FROM deb_files WHERE id = $1`, pkg.ID).
		Scan(&storedControl); err != nil {
		t.Fatal(err)
	}

	jsonEq(t, "control_fields", storedControl, `{"Package":"hello","Section":"utils"}`)

	requireOSUsage(t, pg, repoID, 100, 1)
	requireOSPending(t, pg, repoID, 0)

	// Typed reads.
	got, err := pg.GetDebPackageByID(ctx, pkg.ID)
	if err != nil || got == nil {
		t.Fatalf("GetDebPackageByID = %v, %v", got, err)
	}

	if got.RepositoryID != repoID || got.DistributionID != dist.ID || got.ComponentID != comp.ID || got.Name != "hello" ||
		got.Version != "1:2.10-1" || got.Arch != "amd64" || got.Summary != "greeter" || got.Description != "says hello" ||
		got.License != "GPL" || got.Maintainer != "Jane <j@x>" || got.Homepage != "https://x" ||
		got.Filename != "hello_2.10-1_amd64.deb" || got.InstalledSize != 4096 || got.BlobID != blobID {
		t.Errorf("GetDebPackageByID = %+v", got)
	}

	if list, err := pg.ListDebPackagesByRepo(ctx, repoID); err != nil || len(list) != 1 || list[0].ID != pkg.ID {
		t.Errorf("ListDebPackagesByRepo = %v, %v", list, err)
	}

	byName, err := pg.GetDebianPackageByFilename(ctx, repoID, "hello_2.10-1_amd64.deb")
	if err != nil || byName == nil || byName.ID != pkg.ID || byName.SHA256 != sha || byName.PackageSize != 100 {
		t.Errorf("GetDebianPackageByFilename = %+v, %v", byName, err)
	}

	if miss, err := pg.GetDebianPackageByFilename(ctx, repoID, "nope.deb"); miss != nil || err != nil {
		t.Errorf("missing filename = %v, %v", miss, err)
	}

	byDist, err := pg.GetDebianPackagesByDistComp(ctx, repoID, "stable", "main", "amd64")
	if err != nil || len(byDist) != 1 || byDist[0].ID != pkg.ID {
		t.Errorf("GetDebianPackagesByDistComp = %v, %v", byDist, err)
	}

	if other, _ := pg.GetDebianPackagesByDistComp(ctx, repoID, "stable", "contrib", "amd64"); len(other) != 0 {
		t.Errorf("other component = %v", other)
	}

	entries, err := pg.ListDebPackageEntries(ctx, repoID, "", "", "")
	if err != nil || len(entries) != 1 {
		t.Fatalf("ListDebPackageEntries = %v, %v", entries, err)
	}

	e := entries[0]
	if e.ID != pkg.ID || e.Distribution != "stable" || e.ComponentName != "main" || e.SHA256 != sha || e.Size != 100 ||
		e.InstalledSize != 4096 || e.DistributionID != dist.ID || e.ControlFields["Section"] != "utils" {
		t.Errorf("entry = %+v", e)
	}

	// Duplicates: same coordinates, then same file name.
	dup := *pkg
	dup.ID = ""

	if err := pg.CreateDebPackage(ctx, &dup, dist.ID, comp.ID); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate create = %v, want ErrDuplicate", err)
	}

	// An empty filename is rendered from the coordinates.
	noName := &models.Component{RepositoryID: repoID, ArtifactID: blobID, Name: "lib", Version: "1.0", Arch: "all"}
	reserveOS(t, pg, repoID, 100)

	if err := pg.CreateDebPackage(ctx, noName, dist.ID, comp.ID); err != nil {
		t.Fatal(err)
	}

	var path string

	err = pg.GetDB().QueryRowContext(ctx, `SELECT path FROM package_files WHERE id = $1`, noName.ID).Scan(&path)
	if err != nil || path != "pool/main/l/lib/lib_1.0_all.deb" {
		t.Errorf("derived path = %q, %v", path, err)
	}

	requireOSUsage(t, pg, repoID, 200, 2)
	requireOSPending(t, pg, repoID, 0)

	// Removing both files leaves no generic rows behind.
	if n, err := pg.deleteLiveFiles(ctx, repoID, []string{"deb"}, []string{pkg.ID, noName.ID}); err != nil || n != 2 {
		t.Fatalf("deleteLiveFiles = %d, %v", n, err)
	}

	if p, v, f := osCounts(t, pg, repoID); p != 0 || v != 0 || f != 0 {
		t.Errorf("rows after delete = %d/%d/%d", p, v, f)
	}

	var sides int
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM deb_files`).Scan(&sides); err != nil || sides != 0 {
		t.Errorf("deb_files after delete = %d, %v", sides, err)
	}

	requireOSPending(t, pg, repoID, 2)

	if gone, err := pg.GetDebPackageByID(ctx, pkg.ID); gone != nil || err != nil {
		t.Errorf("deleted package = %v, %v", gone, err)
	}
}

// TestRpmStoreRoundTrip writes through the typed store and reads every typed read and the
// generic rows back.
//
//nolint:gocyclo,cyclop // one scenario: create, typed reads, duplicates, delete
func TestRpmStoreRoundTrip(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	repoID := newOSRepo(t, pg, "rpm-rt", "rpm")

	const sha = "00000000000000000000000000000000000000000000000000000000000000e1"

	blobID, err := pg.UpsertBlob(ctx, sha, sha, 100)
	if err != nil {
		t.Fatal(err)
	}

	pkg := &models.Component{
		RepositoryID: repoID, ArtifactID: blobID, Name: "tool", Version: "2.0", Release: "3.el9", Epoch: 1, Arch: "x86_64",
		Summary: "s", Description: "d", License: "MIT", URL: "https://u", Vendor: "v", Group: "g", Packager: "p",
		SourceRPM: "tool-2.0-3.el9.src.rpm", BuildHost: "h", BuildTime: 1700000000, PackageSize: 100, InstalledSize: 400,
		ArchiveSize: 300, Filename: "tool-2.0-3.el9.x86_64.rpm", HeaderStart: 96, HeaderEnd: 5000,
		RPMExtra: []byte(`{"weak_deps":true}`),
	}
	reserveOS(t, pg, repoID, 100)

	if err := pg.CreateRpmPackage(ctx, pkg); err != nil {
		t.Fatalf("CreateRpmPackage: %v", err)
	}

	if pkg.ID == "" || pkg.CreatedAt.IsZero() {
		t.Fatalf("create did not stamp the package: %+v", pkg)
	}

	if p, v, f := osCounts(t, pg, repoID); p != 1 || v != 1 || f != 1 {
		t.Fatalf("rows = %d/%d/%d, want 1/1/1", p, v, f)
	}

	row := readOSFile(t, pg, repoID)
	if row.Path != "1:tool-2.0-3.el9.x86_64.rpm" || row.Filename != "tool-2.0-3.el9.x86_64.rpm" || row.Arch != "x86_64" ||
		row.Part != "" || row.UnitKey != nil || row.NameKey != "tool" || row.VersionText != "2.0" ||
		row.VersionKey != "2.0-3.el9:1|x86_64" || row.Size == nil || *row.Size != 100 {
		t.Errorf("unexpected file row: %+v", row)
	}

	jsonEq(t, "attrs", row.Attrs, `{"release":"3.el9","epoch":1,"build_time":1700000000,"archive_size":300,"installed_size":400}`)
	jsonEq(t, "metadata", row.Meta, `{"summary":"s","description":"d","license":"MIT","url":"https://u","vendor":"v",
		"group_name":"g","packager":"p","source_rpm":"tool-2.0-3.el9.src.rpm","build_host":"h"}`)

	var hs, he int64

	var extra string
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT header_start, header_end, rpm_extra::text FROM rpm_files WHERE id = $1`, pkg.ID).
		Scan(&hs, &he, &extra); err != nil {
		t.Fatal(err)
	}

	if hs != 96 || he != 5000 {
		t.Errorf("header range = %d-%d", hs, he)
	}

	jsonEq(t, "rpm_extra", extra, `{"weak_deps":true}`)

	requireOSUsage(t, pg, repoID, 100, 1)
	requireOSPending(t, pg, repoID, 0)

	got, err := pg.GetRpmPackageByID(ctx, pkg.ID)
	if err != nil || got == nil {
		t.Fatalf("GetRpmPackageByID = %v, %v", got, err)
	}

	if got.RepositoryID != repoID || got.BlobID != blobID || got.Name != "tool" || got.Version != "2.0" || got.Release != "3.el9" ||
		got.Epoch != 1 || got.Arch != "x86_64" || got.Summary != "s" || got.Description != "d" || got.License != "MIT" ||
		got.URL != "https://u" || got.Vendor != "v" || got.GroupName != "g" || got.Packager != "p" ||
		got.SourceRPM != "tool-2.0-3.el9.src.rpm" || got.BuildHost != "h" || got.Filename != "tool-2.0-3.el9.x86_64.rpm" ||
		got.PackageSize != 100 || got.InstalledSize != 400 || got.ArchiveSize != 300 ||
		got.BuildTime == nil || got.BuildTime.Unix() != 1700000000 {
		t.Errorf("GetRpmPackageByID = %+v", got)
	}

	if ok, err := pg.RpmPackageExists(ctx, repoID, "tool", "2.0", "3.el9", 1, "x86_64"); err != nil || !ok {
		t.Errorf("RpmPackageExists = %v, %v, want true", ok, err)
	}

	if ok, _ := pg.RpmPackageExists(ctx, repoID, "tool", "2.0", "3.el9", 0, "x86_64"); ok {
		t.Error("RpmPackageExists ignores the epoch")
	}

	list, err := pg.ListRpmPackagesForMetadata(ctx, repoID)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListRpmPackagesForMetadata = %v, %v", list, err)
	}

	m := list[0]
	if m.Name != "tool" || m.Version != "2.0" || m.Release != "3.el9" || m.Epoch != 1 || m.Arch != "x86_64" || m.Summary != "s" ||
		m.URL != "https://u" || m.Vendor != "v" || m.Group != "g" || m.SourceRPM != "tool-2.0-3.el9.src.rpm" ||
		m.BuildTime != 1700000000 || m.FileTime < pkg.CreatedAt.Unix() || m.FileTime > pkg.CreatedAt.Unix()+1 || m.PackageSize != 100 || m.InstalledSize != 400 ||
		m.ArchiveSize != 300 || m.SHA256 != sha || m.HeaderStart != 96 || m.HeaderEnd != 5000 {
		t.Errorf("metadata entry = %+v", m)
	}

	// Duplicates, then a second package with empty extra and zero sizes.
	dup := *pkg
	dup.ID = ""

	if err := pg.CreateRpmPackage(ctx, &dup); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate create = %v, want ErrDuplicate", err)
	}

	bare := &models.Component{RepositoryID: repoID, ArtifactID: blobID, Name: "bare", Version: "1", Release: "1", Arch: "noarch"}
	if err := pg.CreateRpmPackage(ctx, bare); err != nil {
		t.Fatal(err)
	}

	var bareExtra, bareAttrs, barePath string
	if err := pg.GetDB().QueryRowContext(ctx, `
		SELECT r.rpm_extra::text, f.attrs::text, f.path FROM rpm_files r JOIN package_files f ON f.id = r.id WHERE r.id = $1`, bare.ID).
		Scan(&bareExtra, &bareAttrs, &barePath); err != nil {
		t.Fatal(err)
	}

	jsonEq(t, "empty rpm_extra", bareExtra, `{}`)
	jsonEq(t, "bare attrs", bareAttrs, `{"release":"1"}`)

	if barePath != "bare-1-1.noarch.rpm" {
		t.Errorf("derived path = %q", barePath)
	}

	if g, _ := pg.GetRpmPackageByID(ctx, bare.ID); g == nil || g.BuildTime != nil || g.PackageSize != 0 {
		t.Errorf("bare package = %+v", g)
	}

	if n, err := pg.deleteLiveFiles(ctx, repoID, []string{"rpm"}, []string{pkg.ID, bare.ID}); err != nil || n != 2 {
		t.Fatalf("deleteLiveFiles = %d, %v", n, err)
	}

	if p, v, f := osCounts(t, pg, repoID); p != 0 || v != 0 || f != 0 {
		t.Errorf("rows after delete = %d/%d/%d", p, v, f)
	}

	requireOSPending(t, pg, repoID, 2)
}

// TestApkStoreRoundTrip writes through the typed store and reads every typed read and the
// generic rows back.
//
//nolint:gocyclo,cyclop // one scenario: create, typed reads, duplicate, delete
func TestApkStoreRoundTrip(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	repoID := newOSRepo(t, pg, "apk-rt", "apk")

	const sha = "00000000000000000000000000000000000000000000000000000000000000a1"

	pkg := &models.ApkPackage{
		RepositoryID: repoID, Branch: "v3.20", Repo: "main", Arch: "x86_64", PkgArch: "noarch", Name: "zlib", Version: "1.3.1-r0",
		Checksum: "Q1abc=", DataHash: "00ff", Description: "d", URL: "https://z", License: "Zlib", Origin: "zlib",
		Maintainer: "m", Commit: "c0ffee", BuildTime: 1700000000, InstalledSize: 4096,
		Depends: []string{"so:libc.musl-x86_64.so.1", "b"}, ProviderPriority: 10,
		BlobKey: "apk/zlib", BlobSHA256: sha, SizeBytes: 123,
	}
	reserveOS(t, pg, repoID, 123)

	if err := pg.CreateApkPackage(ctx, pkg); err != nil {
		t.Fatalf("CreateApkPackage: %v", err)
	}

	if p, v, f := osCounts(t, pg, repoID); p != 1 || v != 1 || f != 1 {
		t.Fatalf("rows = %d/%d/%d, want 1/1/1", p, v, f)
	}

	row := readOSFile(t, pg, repoID)
	if row.Path != "v3.20/main/x86_64/zlib-1.3.1-r0.apk" || row.Filename != "zlib-1.3.1-r0.apk" || row.Arch != "x86_64" ||
		row.UnitKey != nil || row.NameKey != "v3.20/main/zlib" || row.VersionKey != "1.3.1-r0|x86_64" || row.Size != nil {
		t.Errorf("unexpected file row: %+v", row)
	}

	jsonEq(t, "attrs", row.Attrs, `{"build_time":1700000000,"installed_size":4096}`)
	jsonEq(t, "metadata", row.Meta, `{"pkg_arch":"noarch","checksum":"Q1abc=","data_sha256":"00ff","description":"d",
		"url":"https://z","license":"Zlib","origin":"zlib","maintainer":"m","commit":"c0ffee",
		"depends":["so:libc.musl-x86_64.so.1","b"],"provides":[],"install_if":[],"provider_priority":10}`)

	var pkgAttrs string
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT attrs::text FROM packages WHERE repository_id = $1`, repoID).
		Scan(&pkgAttrs); err != nil {
		t.Fatal(err)
	}

	jsonEq(t, "package attrs", pkgAttrs, `{"branch":"v3.20","repo":"main","name":"zlib"}`)
	requireOSUsage(t, pg, repoID, 123, 1)
	requireOSPending(t, pg, repoID, 0)

	got, err := pg.GetApkPackageFile(ctx, repoID, "v3.20", "main", "x86_64", "zlib", "1.3.1-r0")
	if err != nil || got == nil {
		t.Fatalf("GetApkPackageFile = %v, %v", got, err)
	}

	if got.ID != pkg.ID || got.PkgArch != "noarch" || got.Checksum != "Q1abc=" || got.DataHash != "00ff" || got.URL != "https://z" ||
		got.License != "Zlib" || got.Origin != "zlib" || got.Maintainer != "m" || got.Commit != "c0ffee" ||
		got.BuildTime != 1700000000 || got.InstalledSize != 4096 || got.ProviderPriority != 10 ||
		!slices.Equal(got.Depends, []string{"so:libc.musl-x86_64.so.1", "b"}) || got.Provides == nil || got.InstallIf == nil ||
		got.BlobSHA256 != sha || got.BlobKey != "apk/zlib" || got.SizeBytes != 123 || got.Name != "zlib" || got.Branch != "v3.20" ||
		got.Repo != "main" {
		t.Errorf("GetApkPackageFile = %+v", got)
	}

	if err := pg.CreateApkPackage(ctx, &models.ApkPackage{
		RepositoryID: repoID, Branch: "v3.20", Repo: "main", Arch: "x86_64", Name: "zlib", Version: "1.3.1-r0",
		BlobKey: "apk/zlib2", BlobSHA256: sha, SizeBytes: 123,
	}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate create = %v, want ErrDuplicate", err)
	}

	if err := pg.IncrementApkDownloadCount(ctx, pkg.ID, repoID); err != nil {
		t.Fatal(err)
	}

	list, err := pg.ListApkPackages(ctx, repoID)
	if err != nil || len(list) != 1 || list[0].DownloadCount != 1 {
		t.Fatalf("ListApkPackages = %v, %v", list, err)
	}

	if err := pg.DeleteApkPackage(ctx, pkg.ID, repoID); err != nil {
		t.Fatal(err)
	}

	if p, v, f := osCounts(t, pg, repoID); p != 0 || v != 0 || f != 0 {
		t.Errorf("rows after delete = %d/%d/%d", p, v, f)
	}

	requireOSPending(t, pg, repoID, 1)
}

// TestPacmanStoreRoundTrip writes through the typed store and reads every typed read and the
// generic rows back.
//
//nolint:gocyclo,cyclop // one scenario: create, file list, signature, duplicate, delete
func TestPacmanStoreRoundTrip(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	repoID := newOSRepo(t, pg, "pacman-rt", "pacman")

	const sha = "00000000000000000000000000000000000000000000000000000000000000b1"

	pkg := &models.PacmanPackage{
		RepositoryID: repoID, Name: "tool", Base: "toolbase", Version: "1:2.0-1", Arch: "x86_64", RepoArch: "x86_64",
		Filename: "tool-1:2.0-1-x86_64.pkg.tar.zst", MD5: "d41d8cd98f00b204e9800998ecf8427e", SHA256: sha, BlobKey: "pacman/tool",
		SizeBytes: 100, Description: "d", URL: "https://t", Packager: "p", BuildDate: 1700000000, InstalledSize: 4096,
		Licenses: []string{"MIT"}, Groups: []string{"base"}, Depends: []string{"glibc", "zlib"}, OptDepends: []string{"x: y"},
	}
	reserveOS(t, pg, repoID, 100)

	if err := pg.CreatePacmanPackage(ctx, pkg); err != nil {
		t.Fatalf("CreatePacmanPackage: %v", err)
	}

	if p, v, f := osCounts(t, pg, repoID); p != 1 || v != 1 || f != 1 {
		t.Fatalf("rows = %d/%d/%d, want 1/1/1", p, v, f)
	}

	row := readOSFile(t, pg, repoID)
	if row.Path != "tool-1:2.0-1-x86_64.pkg.tar.zst" || row.Filename != row.Path || row.Arch != "x86_64" || row.Part != "" ||
		row.UnitKey != nil || row.NameKey != "tool" || row.VersionText != "1:2.0-1" || row.VersionKey != "1:2.0-1|x86_64" ||
		row.Size != nil {
		t.Errorf("unexpected file row: %+v", row)
	}

	jsonEq(t, "attrs", row.Attrs, `{"build_time":1700000000,"installed_size":4096,"md5":"d41d8cd98f00b204e9800998ecf8427e"}`)
	jsonEq(t, "metadata", row.Meta, `{"base":"toolbase","repo_arch":"x86_64","description":"d","url":"https://t","packager":"p",
		"licenses":["MIT"],"package_groups":["base"],"replaces":[],"conflicts":[],"provides":[],"depends":["glibc","zlib"],
		"optdepends":["x: y"],"makedepends":[],"checkdepends":[]}`)
	requireOSUsage(t, pg, repoID, 100, 1)
	requireOSPending(t, pg, repoID, 0)

	// file_list stays NULL until it is read from the package.
	var isNull bool

	err := pg.GetDB().QueryRowContext(ctx, `SELECT file_list IS NULL FROM pacman_files WHERE id = $1`, pkg.ID).Scan(&isNull)
	if err != nil || !isNull {
		t.Fatalf("file_list IS NULL = %v, %v, want true", isNull, err)
	}

	lists, err := pg.ListPacmanFileLists(ctx, repoID)
	if err != nil || len(lists) != 0 {
		t.Errorf("ListPacmanFileLists before = %v, %v", lists, err)
	}

	pending, err := pg.ListPacmanPackagesWithoutFileList(ctx, repoID)
	if err != nil || len(pending) != 1 || pending[0].ID != pkg.ID {
		t.Fatalf("ListPacmanPackagesWithoutFileList = %v, %v", pending, err)
	}

	if err := pg.SetPacmanFileList(ctx, repoID, pkg.ID, "usr/bin/tool\n"); err != nil {
		t.Fatal(err)
	}

	// Only a NULL list is written: a later call must not overwrite it.
	if err := pg.SetPacmanFileList(ctx, repoID, pkg.ID, ""); err != nil {
		t.Fatal(err)
	}

	lists, err = pg.ListPacmanFileLists(ctx, repoID)
	if err != nil || lists[pkg.ID] != "usr/bin/tool\n" {
		t.Errorf("ListPacmanFileLists = %v, %v (a set list must not be overwritten)", lists, err)
	}

	if pending, _ := pg.ListPacmanPackagesWithoutFileList(ctx, repoID); len(pending) != 0 {
		t.Errorf("still pending: %v", pending)
	}

	if err := pg.SetPacmanSignature(ctx, repoID, pkg.ID, "c2ln", "server", "ABCD"); err != nil {
		t.Fatal(err)
	}

	got, err := pg.GetPacmanPackageByFilename(ctx, repoID, pkg.Filename)
	if err != nil || got == nil {
		t.Fatalf("GetPacmanPackageByFilename = %v, %v", got, err)
	}

	if got.ID != pkg.ID || got.Base != "toolbase" || got.RepoArch != "x86_64" || got.Arch != "x86_64" || got.SHA256 != sha ||
		got.MD5 != "d41d8cd98f00b204e9800998ecf8427e" || got.BlobKey != "pacman/tool" || got.SizeBytes != 100 || got.URL != "https://t" ||
		got.Packager != "p" || got.BuildDate != 1700000000 || got.InstalledSize != 4096 || !slices.Equal(got.Licenses, []string{"MIT"}) ||
		!slices.Equal(got.Groups, []string{"base"}) || !slices.Equal(got.Depends, []string{"glibc", "zlib"}) ||
		!slices.Equal(got.OptDepends, []string{"x: y"}) || got.Signature != "c2ln" || got.SignatureSource != "server" ||
		got.SignatureKey != "ABCD" {
		t.Errorf("GetPacmanPackageByFilename = %+v", got)
	}

	if miss, err := pg.GetPacmanPackageByFilename(ctx, repoID, "nope"); miss != nil || err != nil {
		t.Errorf("missing = %v, %v", miss, err)
	}

	// Ordered by name, then upload time; a duplicate coordinate is ErrDuplicate.
	second := *pkg
	second.ID, second.Filename, second.SHA256, second.BlobKey = "", "tool-1:2.0-1-x86_64.pkg.tar.xz", fmt.Sprintf(
		"%064x",
		0xb2,
	), "pacman/tool2"

	if err := pg.CreatePacmanPackage(ctx, &second); !errors.Is(err, ErrDuplicate) {
		t.Errorf("same coordinates = %v, want ErrDuplicate", err)
	}

	list, err := pg.ListPacmanPackages(ctx, repoID)
	if err != nil || len(list) != 1 || list[0].ID != pkg.ID {
		t.Fatalf("ListPacmanPackages = %v, %v", list, err)
	}

	if err := pg.DeletePacmanPackage(ctx, repoID, pkg.ID); err != nil {
		t.Fatal(err)
	}

	if p, v, f := osCounts(t, pg, repoID); p != 0 || v != 0 || f != 0 {
		t.Errorf("rows after delete = %d/%d/%d", p, v, f)
	}

	var sides int
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pacman_files`).Scan(&sides); err != nil || sides != 0 {
		t.Errorf("pacman_files after delete = %d, %v", sides, err)
	}

	requireOSPending(t, pg, repoID, 1)
}
