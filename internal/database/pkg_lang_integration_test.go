// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Round trips of the four language stores (gomod, cargo, composer, rubygems)
// over the generic package tables: write through the typed store, read back
// through every typed read, and check the generic rows (packages, versions,
// files, keys, unit_key, checksums) and the quota triggers.

// langSHA is a deterministic 64-hex digest of seed, usable as sha256 and blob key.
func langSHA(seed string) string {
	sum := sha256.Sum256([]byte(seed))

	return hex.EncodeToString(sum[:])
}

// genericCounts returns the number of packages, live versions and live files of a repository.
func langGenericCounts(tb testing.TB, pg *PostgresDB, repo string) (int, int, int) {
	tb.Helper()

	var pkgs, vers, files int

	err := pg.GetDB().QueryRowContext(context.Background(), `
		SELECT (SELECT count(*) FROM packages WHERE repository_id = $1),
		       (SELECT count(*) FROM package_versions WHERE repository_id = $1 AND deleted_at IS NULL),
		       (SELECT count(*) FROM package_files WHERE repository_id = $1 AND deleted_at IS NULL)`,
		repo).Scan(&pkgs, &vers, &files)
	if err != nil {
		tb.Fatalf("count generic rows: %v", err)
	}

	return pkgs, vers, files
}

// fileRow is the generic columns of a package_files row the tests compare.
type fileRow struct {
	Path, Filename, Role, Arch, Part, Attrs, Checksums string
	UnitKey                                            string
	Listed                                             bool
	SizeBytes                                          int64
	Downloads                                          int64
}

// fileRowOf reads a file by id; a NULL unit_key reads as "", a NULL size as -1.
func fileRowOf(tb testing.TB, pg *PostgresDB, id string) fileRow {
	tb.Helper()

	var r fileRow

	err := pg.GetDB().QueryRowContext(context.Background(), `
		SELECT path, filename, role, arch, part, attrs::text, checksums::text, COALESCE(unit_key, ''), listed,
		       COALESCE(size_bytes, -1), download_count
		FROM package_files WHERE id = $1`, id).
		Scan(&r.Path, &r.Filename, &r.Role, &r.Arch, &r.Part, &r.Attrs, &r.Checksums, &r.UnitKey, &r.Listed, &r.SizeBytes, &r.Downloads)
	if err != nil {
		tb.Fatalf("read package file %s: %v", id, err)
	}

	return r
}

// packageKeys returns name, name_key, version, version_key and metadata (as text) of the file's version.
func packageKeys(tb testing.TB, pg *PostgresDB, fileID string) (string, string, string, string, string) {
	tb.Helper()

	var name, nameKey, version, versionKey, meta string

	err := pg.GetDB().QueryRowContext(context.Background(), `
		SELECT p.name, p.name_key, v.version, v.version_key, v.metadata::text
		FROM package_files f JOIN package_versions v ON v.id = f.version_id JOIN packages p ON p.id = v.package_id
		WHERE f.id = $1`, fileID).Scan(&name, &nameKey, &version, &versionKey, &meta)
	if err != nil {
		tb.Fatalf("read keys of file %s: %v", fileID, err)
	}

	return name, nameKey, version, versionKey, meta
}

func requirePending(tb testing.TB, pg *PostgresDB, repo string, want int) {
	tb.Helper()

	if n := pendingEntries(tb, pg, repo); n != want {
		tb.Fatalf("%d quota_pending entries, want %d", n, want)
	}
}

func requireJSONEq(tb testing.TB, what, got, want string) {
	tb.Helper()

	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		tb.Fatalf("%s: got %q: %v", what, got, err)
	}

	if err := json.Unmarshal([]byte(want), &w); err != nil {
		tb.Fatalf("%s: want %q: %v", what, want, err)
	}

	if !reflect.DeepEqual(g, w) {
		tb.Errorf("%s = %s, want %s", what, got, want)
	}
}

//nolint:gocyclo,cyclop // one sequential scenario per format reads best top to bottom
func TestGoModStoreRoundTrip(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "rt-go", "go")

	created := time.Date(2021, 5, 6, 7, 8, 9, 0, time.UTC)
	mod := &models.GoModule{
		RepositoryID: s.repo, ModulePath: "example.com/Mod", Version: "v1.2.3", GoModContent: "module example.com/Mod\n\ngo 1.21\n",
		ZipHash: langSHA("gozip"), ZipBlobKey: langSHA("gozip"), ZipSizeBytes: 100, ModBlobKey: langSHA("gomod"), CreatedAt: created,
	}
	modSize := int64(len(mod.GoModContent))

	// The handler reserves the zip as (size, 1) and the go.mod as (size, 0); both
	// are consumed by the two file inserts.
	reserveN(t, pg, s.repo, mod.ZipSizeBytes, 1)
	reserveN(t, pg, s.repo, modSize, 0)

	if err := pg.CreateGoModule(ctx, mod); err != nil {
		t.Fatal(err)
	}

	requirePending(t, pg, s.repo, 0)
	requireUsage(t, pg, s.repo, mod.ZipSizeBytes+modSize, 1)

	if mod.ID == "" || !mod.CreatedAt.Equal(created) || mod.UpdatedAt.IsZero() {
		t.Fatalf("created ids/times not copied back: %+v", mod)
	}

	if p, v, f := langGenericCounts(t, pg, s.repo); p != 1 || v != 1 || f != 2 {
		t.Fatalf("P/V/F = %d/%d/%d, want 1/1/2", p, v, f)
	}

	name, nameKey, version, versionKey, meta := packageKeys(t, pg, mod.ID)
	if name != "example.com/Mod" || nameKey != name || version != "v1.2.3" || versionKey != version {
		t.Errorf("keys = %q %q %q %q", name, nameKey, version, versionKey)
	}

	requireJSONEq(t, "version metadata", meta, `{}`)

	zip := fileRowOf(t, pg, mod.ID)
	wantZip := fileRow{
		Path: "example.com/Mod/@v/v1.2.3.zip", Filename: "v1.2.3.zip", Role: "primary", Listed: true,
		Attrs: "{}", Checksums: "{}", UnitKey: "example.com/Mod@v1.2.3", SizeBytes: -1,
	}

	if zip != wantZip {
		t.Errorf("zip file = %+v, want %+v", zip, wantZip)
	}

	var modID string
	if err := pg.GetDB().QueryRowContext(ctx, `
		SELECT id FROM package_files
		WHERE version_id = (SELECT version_id FROM package_files WHERE id = $1) AND role = 'mod'`,
		mod.ID).Scan(&modID); err != nil {
		t.Fatal(err)
	}

	m := fileRowOf(t, pg, modID)
	if m.Path != "example.com/Mod/@v/v1.2.3.mod" || m.Filename != "v1.2.3.mod" || m.Listed || m.UnitKey != "" || m.Role != "mod" {
		t.Errorf("mod file = %+v", m)
	}

	requireJSONEq(t, "mod attrs", m.Attrs, `{"go_mod_content": "module example.com/Mod\n\ngo 1.21\n"}`)

	got, err := pg.GetGoModule(ctx, s.repo, "example.com/Mod", "v1.2.3")
	if err != nil || got == nil {
		t.Fatalf("GetGoModule = %v, %v", got, err)
	}

	if !got.CreatedAt.Equal(created) {
		t.Errorf("created_at = %v, want %v", got.CreatedAt, created)
	}

	want := *mod
	want.CreatedAt, want.UpdatedAt = got.CreatedAt, got.UpdatedAt

	if *got != want {
		t.Errorf("GetGoModule = %+v, want %+v", *got, want)
	}

	if other, err := pg.GetGoModule(ctx, s.repo, "example.com/mod", "v1.2.3"); err != nil || other != nil {
		t.Errorf("module paths are case-sensitive: got %v, %v", other, err)
	}

	// A second publish of the version is a duplicate and writes nothing.
	dup := *mod
	dup.ZipBlobKey, dup.ZipHash, dup.ModBlobKey = langSHA("gozip2"), langSHA("gozip2"), langSHA("gomod2")

	if err := pg.CreateGoModule(ctx, &dup); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate CreateGoModule = %v, want ErrDuplicate", err)
	}

	if p, v, f := langGenericCounts(t, pg, s.repo); p != 1 || v != 1 || f != 2 {
		t.Fatalf("after duplicate P/V/F = %d/%d/%d, want 1/1/2", p, v, f)
	}

	second := &models.GoModule{
		RepositoryID: s.repo,
		ModulePath:   "example.com/Mod",
		Version:      "v1.3.0",
		GoModContent: "module example.com/Mod\n",
		ZipHash: langSHA(
			"gozip3",
		),
		ZipBlobKey:   langSHA("gozip3"),
		ZipSizeBytes: 50,
		ModBlobKey:   langSHA("gomod3"),
		CreatedAt:    created.Add(time.Hour),
	}
	if err := pg.CreateGoModule(ctx, second); err != nil {
		t.Fatal(err)
	}

	versions, err := pg.ListGoModuleVersions(ctx, s.repo, "example.com/Mod")
	if err != nil || len(versions) != 2 || versions[0].Version != "v1.2.3" || versions[1].Version != "v1.3.0" {
		t.Fatalf("ListGoModuleVersions = %v, %v", versions, err)
	}

	if p, v, f := langGenericCounts(t, pg, s.repo); p != 1 || v != 2 || f != 4 {
		t.Fatalf("P/V/F = %d/%d/%d, want 1/2/4", p, v, f)
	}

	// Hard delete of both files of one version releases both quota parts and
	// prunes the version, not the package.
	if n, err := pg.deleteFiles(ctx, s.repo, []string{mod.ID, modID}); err != nil || n != 2 {
		t.Fatalf("deleteFiles = %d, %v", n, err)
	}

	requirePending(t, pg, s.repo, 2)
	releaseN(t, pg, s.repo, mod.ZipSizeBytes, 1)
	releaseN(t, pg, s.repo, modSize, 0)
	requirePending(t, pg, s.repo, 0)

	if p, v, f := langGenericCounts(t, pg, s.repo); p != 1 || v != 1 || f != 2 {
		t.Fatalf("after delete P/V/F = %d/%d/%d, want 1/1/2", p, v, f)
	}

	if g, err := pg.GetGoModule(ctx, s.repo, "example.com/Mod", "v1.2.3"); err != nil || g != nil {
		t.Errorf("deleted version still served: %v, %v", g, err)
	}
}

//nolint:gocyclo,cyclop // one sequential scenario per format reads best top to bottom
func TestCargoStoreRoundTrip(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "rt-cargo", "cargo")

	created := time.Date(2022, 1, 2, 3, 4, 5, 0, time.UTC)
	crate := &models.CargoCrate{
		RepositoryID: s.repo, Name: "Serde-Json", Version: "1.0.0", Description: "json", Homepage: "https://h", Documentation: "https://d",
		Repository: "https://r", License: "MIT", Keywords: []string{"a", "b"}, Categories: []string{"c"}, Authors: []string{"x"},
		Readme: "# readme", MetadataJSON: []byte(`{"deps":[]}`), BlobKey: langSHA("crate1"), Checksum: langSHA("crate1"),
		SizeBytes: 40, DownloadCount: 2, CreatedAt: created,
	}

	reserveN(t, pg, s.repo, crate.SizeBytes, 1)

	if err := pg.CreateCargoCrate(ctx, crate); err != nil {
		t.Fatal(err)
	}

	requirePending(t, pg, s.repo, 0)
	requireUsage(t, pg, s.repo, 40, 1)

	if p, v, f := langGenericCounts(t, pg, s.repo); p != 1 || v != 1 || f != 1 {
		t.Fatalf("P/V/F = %d/%d/%d, want 1/1/1", p, v, f)
	}

	name, nameKey, version, versionKey, meta := packageKeys(t, pg, crate.ID)
	if name != "Serde-Json" || nameKey != "serde-json" || version != "1.0.0" || versionKey != "1.0.0" {
		t.Errorf("keys = %q %q %q %q", name, nameKey, version, versionKey)
	}

	requireJSONEq(
		t,
		"version metadata",
		meta,
		`{"name":"Serde-Json","description":"json","homepage":"https://h","documentation":"https://d",
		"repository":"https://r","license":"MIT","readme":"# readme","keywords":["a","b"],"categories":["c"],"authors":["x"],
		"metadata_json":{"deps":[]},"yanked":false}`,
	)

	f := fileRowOf(t, pg, crate.ID)
	wantFile := fileRow{
		Path: "Serde-Json/1.0.0/Serde-Json-1.0.0.crate", Filename: "Serde-Json-1.0.0.crate", Role: "primary", Listed: true,
		Attrs: "{}", Checksums: "{}", UnitKey: "Serde-Json@1.0.0", SizeBytes: -1, Downloads: 2,
	}

	if f != wantFile {
		t.Errorf("file = %+v, want %+v", f, wantFile)
	}

	// Names compare case-insensitively.
	got, err := pg.GetCargoCrate(ctx, s.repo, "SERDE-json", "1.0.0")
	if err != nil || got == nil {
		t.Fatalf("GetCargoCrate = %v, %v", got, err)
	}

	if !got.CreatedAt.Equal(created) {
		t.Errorf("created_at = %v, want %v", got.CreatedAt, created)
	}

	requireJSONEq(t, "metadata_json", string(got.MetadataJSON), string(crate.MetadataJSON))

	want := *crate
	want.CreatedAt, want.UpdatedAt, want.MetadataJSON = got.CreatedAt, got.UpdatedAt, got.MetadataJSON

	if !reflect.DeepEqual(*got, want) {
		t.Errorf("GetCargoCrate = %+v, want %+v", *got, want)
	}

	// A second publish of the version under another spelling of the name is a duplicate.
	dup := *crate
	dup.Name, dup.BlobKey, dup.Checksum = "serde-json", langSHA("crate2"), langSHA("crate2")

	if err := pg.CreateCargoCrate(ctx, &dup); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate CreateCargoCrate = %v, want ErrDuplicate", err)
	}

	next := &models.CargoCrate{
		RepositoryID: s.repo, Name: "serde-json", Version: "1.1.0", Description: "newer", BlobKey: langSHA("crate3"),
		Checksum: langSHA("crate3"), SizeBytes: 10, MetadataJSON: []byte(`{}`), CreatedAt: created.Add(time.Hour),
	}
	if err := pg.CreateCargoCrate(ctx, next); err != nil {
		t.Fatal(err)
	}

	if p, v, fl := langGenericCounts(t, pg, s.repo); p != 1 || v != 2 || fl != 2 {
		t.Fatalf("P/V/F = %d/%d/%d, want 1/2/2", p, v, fl)
	}

	list, err := pg.ListCargoCrateVersions(ctx, s.repo, "Serde-JSON")
	if err != nil || len(list) != 2 || list[0].Version != "1.0.0" || list[1].Version != "1.1.0" || list[1].Name != "serde-json" {
		t.Fatalf("ListCargoCrateVersions = %+v, %v", list, err)
	}

	if err := pg.IncrementCargoDownloadCount(ctx, s.repo, "serde-json", "1.0.0"); err != nil {
		t.Fatal(err)
	}

	if err := pg.YankCargoCrate(ctx, s.repo, "SERDE-JSON", "1.0.0", true); err != nil {
		t.Fatal(err)
	}

	yanked, err := pg.GetCargoCrate(ctx, s.repo, "serde-json", "1.0.0")
	if err != nil || yanked == nil || !yanked.Yanked || yanked.DownloadCount != 3 {
		t.Fatalf("after yank/download: %+v, %v", yanked, err)
	}

	sums, total, err := pg.SearchCargoCrateSummaries(ctx, s.repo, "json", 10)
	if err != nil || total != 1 || len(sums) != 1 {
		t.Fatalf("SearchCargoCrateSummaries = %+v, %d, %v", sums, total, err)
	}

	wantSum := models.CargoCrateSummary{
		Name: "Serde-Json", Description: "newer", Downloads: 3, Versions: []string{"1.0.0", "1.1.0"}, LiveVersions: []string{"1.1.0"},
	}
	if !reflect.DeepEqual(*sums[0], wantSum) {
		t.Errorf("summary = %+v, want %+v", *sums[0], wantSum)
	}

	idx, err := pg.ListCargoIndexVersions(ctx, s.repo, "serde-json")
	if err != nil || len(idx) != 2 || idx[0].Checksum != langSHA("crate1") || !idx[0].Yanked || idx[1].Yanked ||
		string(idx[0].MetadataJSON) != `{"deps": []}` {
		t.Fatalf("ListCargoIndexVersions = %+v, %v", idx, err)
	}

	if stamp, err := pg.CargoIndexStamp(ctx, s.repo, "SERDE-JSON"); err != nil || stamp == "" || stamp[:2] != "2:" {
		t.Errorf("CargoIndexStamp = %q, %v", stamp, err)
	}
}

//nolint:gocyclo,cyclop // one sequential scenario per format reads best top to bottom
func TestComposerStoreRoundTrip(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "rt-composer", "composer")

	created := time.Date(2023, 2, 3, 4, 5, 6, 0, time.UTC)
	pkg := &models.ComposerPackage{
		RepositoryID:      s.repo,
		Name:              "acme/lib",
		Version:           "v1.0.0",
		VersionNormalized: "1.0.0.0",
		BlobKey:           langSHA("zip1"),
		SizeBytes:         30,
		SHA256: langSHA(
			"zip1",
		),
		SHA1:         "da39a3ee5e6b4b0d3255bfef95601890afd80709",
		ComposerJSON: []byte(`{"name":"acme/lib","description":"d1"}`),
		CreatedAt:    created,
	}

	reserveN(t, pg, s.repo, 30, 1)

	if err := pg.CreateComposerPackage(ctx, pkg); err != nil {
		t.Fatal(err)
	}

	requirePending(t, pg, s.repo, 0)
	requireUsage(t, pg, s.repo, 30, 1)

	name, nameKey, version, versionKey, meta := packageKeys(t, pg, pkg.ID)
	if name != "acme/lib" || nameKey != name || version != "v1.0.0" || versionKey != "1.0.0.0" {
		t.Errorf("keys = %q %q %q %q", name, nameKey, version, versionKey)
	}

	requireJSONEq(t, "version metadata", meta, `{"composer_json":{"name":"acme/lib","description":"d1"}}`)

	f := fileRowOf(t, pg, pkg.ID)
	wantFile := fileRow{
		Path: "acme/lib/1.0.0.0/acme-lib-v1.0.0.zip", Filename: "acme-lib-v1.0.0.zip", Role: "primary", Listed: true,
		Attrs: `{"sha1": "da39a3ee5e6b4b0d3255bfef95601890afd80709"}`, Checksums: "{}", SizeBytes: -1,
	}

	if f != wantFile {
		t.Errorf("file = %+v, want %+v", f, wantFile)
	}

	got, err := pg.GetComposerPackage(ctx, s.repo, "acme/lib", "1.0.0.0")
	if err != nil || got == nil {
		t.Fatalf("GetComposerPackage = %v, %v", got, err)
	}

	if !got.CreatedAt.Equal(created) {
		t.Errorf("created_at = %v, want %v", got.CreatedAt, created)
	}

	want := *pkg
	want.CreatedAt, want.UpdatedAt = got.CreatedAt, got.UpdatedAt
	want.ComposerJSON = got.ComposerJSON

	if !reflect.DeepEqual(*got, want) {
		t.Errorf("GetComposerPackage = %+v, want %+v", *got, want)
	}

	requireJSONEq(t, "composer_json", string(got.ComposerJSON), string(pkg.ComposerJSON))

	byID, err := pg.GetComposerPackageByID(ctx, s.repo, pkg.ID)
	if err != nil || byID == nil || byID.Name != "acme/lib" {
		t.Fatalf("GetComposerPackageByID = %+v, %v", byID, err)
	}

	dup := *pkg
	dup.Version = "1.0.0"
	dup.BlobKey, dup.SHA256 = langSHA("zip2"), langSHA("zip2")

	if err := pg.CreateComposerPackage(ctx, &dup); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate CreateComposerPackage = %v, want ErrDuplicate", err)
	}

	second := &models.ComposerPackage{
		RepositoryID: s.repo, Name: "acme/lib", Version: "v1.1.0", VersionNormalized: "1.1.0.0", BlobKey: langSHA("zip3"), SizeBytes: 20,
		SHA256: langSHA("zip3"), ComposerJSON: []byte(`{"name":"acme/lib","description":"d2"}`), CreatedAt: created.Add(time.Hour),
	}
	if err := pg.CreateComposerPackage(ctx, second); err != nil {
		t.Fatal(err)
	}

	if f := fileRowOf(t, pg, second.ID); f.Attrs != "{}" {
		t.Errorf("empty sha1 must be omitted from attrs, got %s", f.Attrs)
	}

	versions, err := pg.ListComposerPackageVersions(ctx, s.repo, "acme/lib")
	if err != nil || len(versions) != 2 || versions[0].VersionNormalized != "1.0.0.0" || versions[1].VersionNormalized != "1.1.0.0" {
		t.Fatalf("ListComposerPackageVersions = %+v, %v", versions, err)
	}

	names, err := pg.ListComposerPackageNames(ctx, s.repo)
	if err != nil || len(names) != 1 || names[0].Name != "acme/lib" || names[0].Description != "d2" {
		t.Fatalf("ListComposerPackageNames = %+v, %v", names, err)
	}

	if err := pg.DeleteComposerPackage(ctx, s.repo, second.ID); err != nil {
		t.Fatal(err)
	}

	if p, v, fl := langGenericCounts(t, pg, s.repo); p != 1 || v != 1 || fl != 1 {
		t.Fatalf("after delete P/V/F = %d/%d/%d, want 1/1/1", p, v, fl)
	}

	if err := pg.DeleteComposerPackage(ctx, s.repo, pkg.ID); err != nil {
		t.Fatal(err)
	}

	if p, v, fl := langGenericCounts(t, pg, s.repo); p != 0 || v != 0 || fl != 0 {
		t.Fatalf("after last delete P/V/F = %d/%d/%d, want 0/0/0", p, v, fl)
	}
}

//nolint:gocyclo,cyclop // one sequential scenario per format reads best top to bottom
func TestRubyGemsStoreRoundTrip(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "rt-gems", "rubygems")

	created := time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC)
	gem := &models.RubyGem{
		RepositoryID: s.repo,
		Name:         "nokogiri",
		Version:      "1.0.0",
		Platform:     "ruby",
		Description:  "desc",
		Summary:      "sum",
		Homepage:     "https://h",
		Authors:      []string{"a"},
		Licenses:     []string{"MIT"},
		Dependencies: []byte(`[{"name":"x","requirements":">= 0"}]`),
		MetadataJSON: []byte(
			`{"k":"v"}`,
		),
		BlobKey:        langSHA("gem1"),
		ChecksumSHA256: langSHA("gem1"),
		Size:           25,
		DownloadCount:  4,
		CreatedAt:      created,
	}

	reserveN(t, pg, s.repo, 25, 1)

	if err := pg.CreateRubyGem(ctx, gem); err != nil {
		t.Fatal(err)
	}

	requirePending(t, pg, s.repo, 0)

	name, nameKey, version, versionKey, meta := packageKeys(t, pg, gem.ID)
	if name != "nokogiri" || nameKey != name || version != "1.0.0" || versionKey != "1.0.0" {
		t.Errorf("keys = %q %q %q %q", name, nameKey, version, versionKey)
	}

	requireJSONEq(
		t,
		"version metadata",
		meta,
		`{"description":"desc","summary":"sum","homepage":"https://h","authors":["a"],"licenses":["MIT"],
		"dependencies":[{"name":"x","requirements":">= 0"}],"metadata_json":{"k":"v"},"yanked":false}`,
	)

	f := fileRowOf(t, pg, gem.ID)
	wantFile := fileRow{
		Path: "nokogiri/1.0.0/nokogiri-1.0.0.gem", Filename: "nokogiri-1.0.0.gem", Role: "primary", Arch: "ruby", Listed: true,
		Attrs: "{}", Checksums: "{}", SizeBytes: 25, Downloads: 4,
	}

	if f != wantFile {
		t.Errorf("file = %+v, want %+v", f, wantFile)
	}

	// The same version on another platform is a version of its own.
	java := &models.RubyGem{
		RepositoryID:   s.repo,
		Name:           "nokogiri",
		Version:        "1.0.0",
		Platform:       "java",
		BlobKey:        langSHA("gem2"),
		ChecksumSHA256: langSHA("gem2"),
		CreatedAt:      created.Add(time.Hour),
	}
	if err := pg.CreateRubyGem(ctx, java); err != nil {
		t.Fatal(err)
	}

	if _, _, _, vk, _ := packageKeys(t, pg, java.ID); vk != "1.0.0|java" {
		t.Errorf("java version_key = %q", vk)
	}

	if jf := fileRowOf(t, pg, java.ID); jf.Filename != "nokogiri-1.0.0-java.gem" || jf.Arch != "java" || jf.SizeBytes != -1 {
		t.Errorf("java file = %+v", jf)
	}

	if p, v, fl := langGenericCounts(t, pg, s.repo); p != 1 || v != 2 || fl != 2 {
		t.Fatalf("P/V/F = %d/%d/%d, want 1/2/2", p, v, fl)
	}

	dup := *gem
	dup.BlobKey, dup.ChecksumSHA256 = langSHA("gem3"), langSHA("gem3")

	if err := pg.CreateRubyGem(ctx, &dup); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate CreateRubyGem = %v, want ErrDuplicate", err)
	}

	got, err := pg.GetRubyGem(ctx, s.repo, "nokogiri", "1.0.0", "ruby")
	if err != nil || got == nil {
		t.Fatalf("GetRubyGem = %v, %v", got, err)
	}

	if !got.CreatedAt.Equal(created) {
		t.Errorf("created_at = %v, want %v", got.CreatedAt, created)
	}

	want := *gem
	want.CreatedAt, want.UpdatedAt = got.CreatedAt, got.UpdatedAt
	want.Dependencies, want.MetadataJSON = got.Dependencies, got.MetadataJSON

	if !reflect.DeepEqual(*got, want) {
		t.Errorf("GetRubyGem = %+v, want %+v", *got, want)
	}

	requireJSONEq(t, "dependencies", string(got.Dependencies), string(gem.Dependencies))

	if g, err := pg.GetRubyGem(ctx, s.repo, "nokogiri", "1.0.0", "java"); err != nil || g == nil || g.Platform != "java" || g.Size != 0 {
		t.Fatalf("GetRubyGem java = %+v, %v", g, err)
	}

	all, err := pg.ListRubyGems(ctx, s.repo)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListRubyGems = %+v, %v", all, err)
	}

	byName, err := pg.ListRubyGemVersions(ctx, s.repo, "nokogiri")
	if err != nil || len(byName) != 2 {
		t.Fatalf("ListRubyGemVersions = %+v, %v", byName, err)
	}

	found, err := pg.SearchRubyGems(ctx, s.repo, "noko")
	if err != nil || len(found) != 2 {
		t.Fatalf("SearchRubyGems = %+v, %v", found, err)
	}

	// A yank with a platform hits that platform's build only.
	if err := pg.YankRubyGem(ctx, s.repo, "nokogiri", "1.0.0", "java", true); err != nil {
		t.Fatal(err)
	}

	specs, err := pg.ListRubyGemSpecs(ctx, s.repo)
	if err != nil || len(specs) != 1 || specs[0].Platform != "ruby" {
		t.Fatalf("ListRubyGemSpecs after platform yank = %+v, %v", specs, err)
	}

	if g, _ := pg.GetRubyGem(ctx, s.repo, "nokogiri", "1.0.0", "java"); g == nil || !g.Yanked {
		t.Errorf("java build not yanked: %+v", g)
	}

	if g, _ := pg.GetRubyGem(ctx, s.repo, "nokogiri", "1.0.0", "ruby"); g == nil || g.Yanked {
		t.Errorf("ruby build yanked: %+v", g)
	}
}
