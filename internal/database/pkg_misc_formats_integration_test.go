// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Round trips of the helm, nuget, ansible, terraform and nix stores over the
// generic package tables: write through the typed store, read back through every
// typed read, then check the generic rows and the quota reservation.

// genericRows counts the packages, versions and files of a repository.
type genericRows struct{ packages, versions, files int }

func countGeneric(t *testing.T, pg *PostgresDB, repo string) genericRows {
	t.Helper()

	var g genericRows

	err := pg.GetDB().QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM packages WHERE repository_id = $1),
		(SELECT count(*) FROM package_versions WHERE repository_id = $1),
		(SELECT count(*) FROM package_files WHERE repository_id = $1)`, repo).Scan(&g.packages, &g.versions, &g.files)
	if err != nil {
		t.Fatalf("count generic rows: %v", err)
	}

	return g
}

func requireGeneric(t *testing.T, pg *PostgresDB, repo string, want genericRows) {
	t.Helper()

	if got := countGeneric(t, pg, repo); got != want {
		t.Fatalf("generic rows (packages, versions, files) = %+v, want %+v", got, want)
	}
}

// miscFileRow is the generic row of the file with the id.
type miscFileRow struct {
	path, filename, arch, part, role, unitKey, attrs, checksums, verMeta, pkgName, pkgKey, pkgAttrs, format, version, versionKey string
	downloads                                                                                                                    int64
	listed                                                                                                                       bool
	blobSHA                                                                                                                      string
}

func readFile(t *testing.T, pg *PostgresDB, id string) miscFileRow {
	t.Helper()

	var r miscFileRow

	err := pg.GetDB().QueryRowContext(context.Background(), `
		SELECT f.path, f.filename, f.arch, f.part, f.role, COALESCE(f.unit_key, ''), f.attrs::text, f.checksums::text,
		       v.metadata::text, p.name, p.name_key, p.attrs::text, p.format, v.version, v.version_key,
		       f.download_count, f.listed, b.sha256
		FROM package_files f
		JOIN package_versions v ON v.id = f.version_id
		JOIN packages p ON p.id = v.package_id
		JOIN blobs b ON b.id = f.blob_id
		WHERE f.id = $1`, id).Scan(&r.path, &r.filename, &r.arch, &r.part, &r.role, &r.unitKey, &r.attrs, &r.checksums,
		&r.verMeta, &r.pkgName, &r.pkgKey, &r.pkgAttrs, &r.format, &r.version, &r.versionKey, &r.downloads, &r.listed, &r.blobSHA)
	if err != nil {
		t.Fatalf("read file %s: %v", id, err)
	}

	return r
}

func requireEq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()

	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// digest returns a stable 64 hex string for a label.
func digest(label string) string {
	sum := sha256.Sum256([]byte(label))

	return hex.EncodeToString(sum[:])
}

// reservedWrite reserves (size, 1) as an upload does, runs the write and
// requires the insert to consume exactly that reservation.
func reservedWrite(t *testing.T, pg *PostgresDB, repo string, size int64, write func()) {
	t.Helper()

	reserveN(t, pg, repo, size, 1)

	if n := pendingEntries(t, pg, repo); n != 1 {
		t.Fatalf("pending entries after reserve = %d, want 1", n)
	}

	write()

	if n := pendingEntries(t, pg, repo); n != 0 {
		t.Fatalf("pending entries after the write = %d, want 0 (one primary file consumes one reservation)", n)
	}
}

// requireReleased requires a delete to have returned one reservation entry.
func requireReleased(t *testing.T, pg *PostgresDB, repo string, want int) {
	t.Helper()

	if n := pendingEntries(t, pg, repo); n != want {
		t.Fatalf("pending entries after the delete = %d, want %d", n, want)
	}
}

func TestHelmStoreRoundTrip(t *testing.T) { //nolint:gocyclo,cyclop // one round trip checks every typed read of the store
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	repo := newQuotaSeeder(t, pg, "rt-helm", "helm").repo

	chart := func(version string) *models.HelmChart {
		return &models.HelmChart{
			RepositoryID: repo, Name: "web", Version: version, AppVersion: "9." + version, Description: "d", Home: "https://h",
			Keywords: []string{"a", "b"}, Sources: []string{"https://s"}, Icon: "https://i",
			Maintainers: []map[string]string{{"name": "n", "email": "e@x"}}, APIVersion: "v2", Type: "library",
			BlobKey: "helm/" + version, Digest: digest("helm" + version), SizeBytes: 7,
		}
	}

	c1 := chart("1.0.0")
	reservedWrite(t, pg, repo, 7, func() {
		if err := pg.CreateHelmChart(ctx, c1); err != nil {
			t.Fatal(err)
		}
	})

	if c1.ID == "" || c1.CreatedAt.IsZero() || c1.UpdatedAt.IsZero() {
		t.Fatalf("create did not fill id and times: %+v", c1)
	}

	c2 := chart("1.1.0")
	if err := pg.CreateHelmChart(ctx, c2); err != nil {
		t.Fatal(err)
	}

	if err := pg.CreateHelmChart(ctx, chart("1.0.0")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate publish = %v, want ErrDuplicate", err)
	}

	requireGeneric(t, pg, repo, genericRows{1, 2, 2})

	got, err := pg.GetHelmChart(ctx, repo, "web", "1.0.0")
	if err != nil || got == nil {
		t.Fatalf("GetHelmChart = %v, %v", got, err)
	}

	want := *c1
	want.CreatedAt, want.UpdatedAt = got.CreatedAt, got.UpdatedAt

	requireEq(t, "ID", got.ID, want.ID)
	requireEq(t, "AppVersion", got.AppVersion, want.AppVersion)
	requireEq(t, "Description", got.Description, "d")
	requireEq(t, "Home", got.Home, "https://h")
	requireEq(t, "Icon", got.Icon, "https://i")
	requireEq(t, "APIVersion", got.APIVersion, "v2")
	requireEq(t, "Type", got.Type, "library")
	requireEq(t, "BlobKey", got.BlobKey, "helm/1.0.0")
	requireEq(t, "Digest", got.Digest, digest("helm1.0.0"))
	requireEq(t, "SizeBytes", got.SizeBytes, int64(7))
	requireEq(t, "Keywords", fmt.Sprint(got.Keywords), "[a b]")
	requireEq(t, "Sources", fmt.Sprint(got.Sources), "[https://s]")
	requireEq(t, "Maintainers", fmt.Sprint(got.Maintainers), "[map[email:e@x name:n]]")

	list, err := pg.ListHelmCharts(ctx, repo)
	if err != nil || len(list) != 2 || list[0].Version != "1.1.0" || list[1].Version != "1.0.0" {
		t.Fatalf("ListHelmCharts = %v, %v (want name, version DESC)", list, err)
	}

	f := readFile(t, pg, c1.ID)
	requireEq(t, "path", f.path, "web/1.0.0/web-1.0.0.tgz")
	requireEq(t, "filename", f.filename, "web-1.0.0.tgz")
	requireEq(t, "role", f.role, "primary")
	requireEq(t, "unit_key", f.unitKey, "web@1.0.0")
	requireEq(t, "listed", f.listed, true)
	requireEq(t, "blob sha256", f.blobSHA, digest("helm1.0.0"))
	requireEq(t, "package name", f.pkgName, "web")
	requireEq(t, "package name_key", f.pkgKey, "web")
	requireEq(t, "format", f.format, "helm")
	requireEq(t, "version_key", f.versionKey, "1.0.0")

	// A chart without descriptive fields reads the documented defaults.
	bare := &models.HelmChart{
		RepositoryID: repo, Name: "bare", Version: "0.1.0", BlobKey: "helm/bare", Digest: digest("bare"), SizeBytes: 1,
	}
	if err := pg.CreateHelmChart(ctx, bare); err != nil {
		t.Fatal(err)
	}

	if g, _ := pg.GetHelmChart(ctx, repo, "bare", "0.1.0"); g == nil || g.APIVersion != "v2" || g.Type != "application" {
		t.Errorf("bare chart = %+v, want api_version v2 and type application", g)
	}

	// Delete drops the file, then the version, then the package.
	if err := pg.DeleteHelmChart(ctx, repo, "web", "1.1.0"); err != nil {
		t.Fatal(err)
	}

	requireGeneric(t, pg, repo, genericRows{2, 2, 2})
	requireReleased(t, pg, repo, 1)

	if err := pg.DeleteHelmChart(ctx, repo, "web", "1.0.0"); err != nil {
		t.Fatal(err)
	}

	requireGeneric(t, pg, repo, genericRows{1, 1, 1})

	if g, err := pg.GetHelmChart(ctx, repo, "web", "1.0.0"); err != nil || g != nil {
		t.Errorf("deleted chart = %v, %v, want nil", g, err)
	}

	// The same version is publishable again.
	if err := pg.CreateHelmChart(ctx, chart("1.0.0")); err != nil {
		t.Fatalf("republish after delete: %v", err)
	}
}

func TestNuGetStoreRoundTrip(t *testing.T) { //nolint:gocyclo,cyclop // one round trip checks every typed read of the store
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	repo := newQuotaSeeder(t, pg, "rt-nuget", "nuget").repo

	pkg := func(id, version string) *models.NuGetPackage {
		return &models.NuGetPackage{
			RepositoryID: repo, PackageID: id, Version: version, Title: "Demo Title", Description: "a demo library", Summary: "sum",
			Authors: "au", Owners: "ow", Tags: "t1 t2", ProjectURL: "https://p", LicenseURL: "https://l", IconURL: "https://i",
			RequireLicenseAcceptance: true, IsPrerelease: version == "2.0.0-beta", MinClientVersion: "5.0",
			BlobKey: digest("nuget" + id + version), PackageSize: 11, DependencyGroups: []byte(`[{"targetFramework":"net8.0"}]`),
		}
	}

	p1 := pkg("demo", "1.0.0")
	reservedWrite(t, pg, repo, 11, func() {
		if err := pg.CreateNuGetPackage(ctx, p1); err != nil {
			t.Fatal(err)
		}
	})

	if p1.ID == "" || p1.CreatedAt.IsZero() {
		t.Fatalf("create did not fill id and times: %+v", p1)
	}

	if err := pg.CreateNuGetPackage(ctx, pkg("demo", "2.0.0-beta")); err != nil {
		t.Fatal(err)
	}

	if err := pg.CreateNuGetPackage(ctx, pkg("other", "1.0.0")); err != nil {
		t.Fatal(err)
	}

	// A duplicate is ErrDuplicate and leaves the stored content alone, also
	// when the id differs only in case.
	dup := pkg("DEMO", "1.0.0")
	dup.Title = "changed"

	if err := pg.CreateNuGetPackage(ctx, dup); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate publish = %v, want ErrDuplicate", err)
	}

	requireGeneric(t, pg, repo, genericRows{2, 3, 3})

	got, err := pg.GetNuGetPackage(ctx, repo, "Demo", "1.0.0")
	if err != nil || got == nil {
		t.Fatalf("GetNuGetPackage = %v, %v", got, err)
	}

	requireEq(t, "ID", got.ID, p1.ID)
	requireEq(t, "PackageID", got.PackageID, "demo")
	requireEq(t, "Title", got.Title, "Demo Title")
	requireEq(t, "Description", got.Description, "a demo library")
	requireEq(t, "Summary", got.Summary, "sum")
	requireEq(t, "Authors", got.Authors, "au")
	requireEq(t, "Owners", got.Owners, "ow")
	requireEq(t, "Tags", got.Tags, "t1 t2")
	requireEq(t, "ProjectURL", got.ProjectURL, "https://p")
	requireEq(t, "LicenseURL", got.LicenseURL, "https://l")
	requireEq(t, "IconURL", got.IconURL, "https://i")
	requireEq(t, "MinClientVersion", got.MinClientVersion, "5.0")
	requireEq(t, "RequireLicenseAcceptance", got.RequireLicenseAcceptance, true)
	requireEq(t, "IsPrerelease", got.IsPrerelease, false)
	requireEq(t, "BlobKey", got.BlobKey, digest("nugetdemo1.0.0"))
	requireEq(t, "PackageSize", got.PackageSize, int64(11))
	requireEq(t, "DependencyGroups", string(got.DependencyGroups), `[{"targetFramework": "net8.0"}]`) // jsonb normalises the spacing

	versions, err := pg.ListNuGetPackageVersions(ctx, repo, "demo")
	if err != nil || len(versions) != 2 || versions[0].Version != "2.0.0-beta" || !versions[0].IsPrerelease {
		t.Fatalf("ListNuGetPackageVersions = %v, %v", versions, err)
	}

	all, err := pg.ListNuGetPackages(ctx, repo)
	if err != nil || len(all) != 3 || all[0].PackageID != "demo" || all[2].PackageID != "other" {
		t.Fatalf("ListNuGetPackages = %v, %v", all, err)
	}

	// Search: by id, by title, by description, paged over packages.
	for _, q := range []string{"dem", "TITLE", "LIBRARY"} {
		res, total, err := pg.SearchNuGetPackages(ctx, repo, q, 0, 10)
		if err != nil || total != 2 || len(res) != 3 {
			t.Fatalf("Search(%q) = %d versions, total %d, %v; want 3 versions of 2 packages", q, len(res), total, err)
		}
	}

	res, total, err := pg.SearchNuGetPackages(ctx, repo, "dem", 0, 1)
	if err != nil || total != 2 || len(res) != 2 || res[0].PackageID != "demo" {
		t.Fatalf("Search page 1 = %v, total %d, %v; want the 2 versions of demo", res, total, err)
	}

	res, _, err = pg.SearchNuGetPackages(ctx, repo, "dem", 1, 1)
	if err != nil || len(res) != 1 || res[0].PackageID != "other" {
		t.Fatalf("Search page 2 = %v, %v; want other", res, err)
	}

	if err := pg.IncrementNuGetDownloadCount(ctx, repo, "Demo", "1.0.0"); err != nil {
		t.Fatal(err)
	}

	if g, _ := pg.GetNuGetPackage(ctx, repo, "demo", "1.0.0"); g == nil || g.DownloadCount != 1 {
		t.Errorf("download count = %+v, want 1", g)
	}

	f := readFile(t, pg, p1.ID)
	requireEq(t, "path", f.path, "demo/1.0.0/demo.1.0.0.nupkg")
	requireEq(t, "filename", f.filename, "demo.1.0.0.nupkg")
	requireEq(t, "unit_key", f.unitKey, "demo@1.0.0")
	requireEq(t, "downloads", f.downloads, int64(1))
	requireEq(t, "package name_key", f.pkgKey, "demo")
	requireEq(t, "format", f.format, "nuget")

	// A package published without groups has none recorded until backfilled.
	nodeps := pkg("nodeps", "1.0.0")
	nodeps.DependencyGroups = nil

	if err := pg.CreateNuGetPackage(ctx, nodeps); err != nil {
		t.Fatal(err)
	}

	if g, _ := pg.GetNuGetPackage(ctx, repo, "nodeps", "1.0.0"); g == nil || g.DependencyGroups != nil {
		t.Fatalf("package without groups = %+v, want nil groups", g)
	}

	if err := pg.DeleteNuGetPackage(ctx, repo, "demo", "2.0.0-beta"); err != nil {
		t.Fatal(err)
	}

	requireGeneric(t, pg, repo, genericRows{3, 3, 3})

	if err := pg.DeleteNuGetPackage(ctx, repo, "Demo", "1.0.0"); err != nil {
		t.Fatal(err)
	}

	requireGeneric(t, pg, repo, genericRows{2, 2, 2})
	requireReleased(t, pg, repo, 2) // each delete returns its reservation
}

func TestAnsibleStoreRoundTrip(t *testing.T) { //nolint:gocyclo,cyclop // one round trip checks every typed read of the store
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	repo := newQuotaSeeder(t, pg, "rt-ansible", "ansible").repo

	col := func(ns, name, version string) *models.AnsibleCollection {
		return &models.AnsibleCollection{
			RepositoryID: repo, Namespace: ns, Name: name, Version: version,
			Filename: fmt.Sprintf("%s-%s-%s.tar.gz", ns, name, version),
			SHA256:   digest("ans" + ns + name + version), BlobKey: "ansible/" + ns + name + version, SizeBytes: 13,
			Dependencies:    map[string]string{"community.general": ">=1.0.0"},
			Metadata:        []byte(`{"namespace":"` + ns + `","name":"` + name + `"}`),
			RequiresAnsible: ">=2.14",
		}
	}

	c1 := col("acme", "web", "1.0.0")
	reservedWrite(t, pg, repo, 13, func() {
		if err := pg.CreateAnsibleCollection(ctx, c1); err != nil {
			t.Fatal(err)
		}
	})

	c2 := col("acme", "web", "1.1.0")
	if err := pg.CreateAnsibleCollection(ctx, c2); err != nil {
		t.Fatal(err)
	}

	c3 := col("beta", "db", "0.1.0")
	c3.Dependencies, c3.Metadata, c3.RequiresAnsible = nil, nil, ""

	if err := pg.CreateAnsibleCollection(ctx, c3); err != nil {
		t.Fatal(err)
	}

	if err := pg.CreateAnsibleCollection(ctx, col("acme", "web", "1.0.0")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate publish = %v, want ErrDuplicate", err)
	}

	requireGeneric(t, pg, repo, genericRows{2, 3, 3})

	got, err := pg.GetAnsibleCollection(ctx, repo, "acme", "web", "1.0.0")
	if err != nil || got == nil {
		t.Fatalf("GetAnsibleCollection = %v, %v", got, err)
	}

	requireEq(t, "ID", got.ID, c1.ID)
	requireEq(t, "Namespace", got.Namespace, "acme")
	requireEq(t, "Name", got.Name, "web")
	requireEq(t, "Filename", got.Filename, "acme-web-1.0.0.tar.gz")
	requireEq(t, "BlobKey", got.BlobKey, "ansible/acmeweb1.0.0")
	requireEq(t, "SizeBytes", got.SizeBytes, int64(13))
	requireEq(t, "SHA256", got.SHA256, digest("ansacmeweb1.0.0"))
	requireEq(t, "Dependencies", fmt.Sprint(got.Dependencies), "map[community.general:>=1.0.0]")
	requireEq(t, "Metadata", string(got.Metadata), `{"name": "web", "namespace": "acme"}`)
	requireEq(t, "RequiresAnsible", got.RequiresAnsible, ">=2.14")

	byID, err := pg.GetAnsibleCollectionByID(ctx, repo, c2.ID)
	if err != nil || byID == nil || byID.Version != "1.1.0" {
		t.Fatalf("GetAnsibleCollectionByID = %v, %v", byID, err)
	}

	if other, _ := pg.GetAnsibleCollectionByID(ctx, "00000000-0000-0000-0000-000000000000", c2.ID); other != nil {
		t.Errorf("ByID in another repository = %+v, want nil", other)
	}

	bare, _ := pg.GetAnsibleCollection(ctx, repo, "beta", "db", "0.1.0")
	if bare == nil || len(bare.Dependencies) != 0 || bare.Dependencies == nil || bare.RequiresAnsible != "" {
		t.Errorf("collection without extras = %+v", bare)
	}

	versions, err := pg.ListAnsibleCollectionVersions(ctx, repo, "acme", "web")
	if err != nil || len(versions) != 2 || versions[0].Version != "1.0.0" {
		t.Fatalf("ListAnsibleCollectionVersions = %v, %v", versions, err)
	}

	all, err := pg.ListAnsibleCollections(ctx, repo)
	if err != nil || len(all) != 3 || all[2].Namespace != "beta" {
		t.Fatalf("ListAnsibleCollections = %v, %v", all, err)
	}

	names, total, err := pg.ListAnsibleCollectionNames(ctx, repo, 10, 0)
	if err != nil || total != 2 || len(names) != 2 ||
		names[0].Namespace != "acme" || names[0].Name != "web" || names[1].Namespace != "beta" {
		t.Fatalf("ListAnsibleCollectionNames = %v, %d, %v", names, total, err)
	}

	if !names[0].UpdatedAt.After(names[0].CreatedAt) && !names[0].UpdatedAt.Equal(names[0].CreatedAt) {
		t.Errorf("name times = %v .. %v", names[0].CreatedAt, names[0].UpdatedAt)
	}

	page, total, err := pg.ListAnsibleCollectionNames(ctx, repo, 1, 1)
	if err != nil || total != 2 || len(page) != 1 || page[0].Namespace != "beta" {
		t.Fatalf("names page 2 = %v, %d, %v", page, total, err)
	}

	f := readFile(t, pg, c1.ID)
	requireEq(t, "path", f.path, "acme/web/1.0.0/acme-web-1.0.0.tar.gz")
	requireEq(t, "filename", f.filename, "acme-web-1.0.0.tar.gz")
	requireEq(t, "unit_key", f.unitKey, "")
	requireEq(t, "package name", f.pkgName, "acme.web")
	requireEq(t, "package name_key", f.pkgKey, "acme.web")
	requireEq(t, "package attrs", f.pkgAttrs, `{"name": "web", "namespace": "acme"}`)
	requireEq(t, "format", f.format, "ansible")

	if err := pg.DeleteAnsibleCollection(ctx, repo, c2.ID); err != nil {
		t.Fatal(err)
	}

	requireGeneric(t, pg, repo, genericRows{2, 2, 2})
	requireReleased(t, pg, repo, 1)

	if err := pg.DeleteAnsibleCollection(ctx, repo, c1.ID); err != nil {
		t.Fatal(err)
	}

	if err := pg.DeleteAnsibleCollection(ctx, repo, c3.ID); err != nil {
		t.Fatal(err)
	}

	requireGeneric(t, pg, repo, genericRows{0, 0, 0})
}

func TestTerraformStoreRoundTrip(t *testing.T) { //nolint:gocyclo,cyclop // one round trip checks every typed read of the store
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	repo := newQuotaSeeder(t, pg, "rt-terraform", "terraform").repo

	mod := func(version, ext string) *models.TerraformModule {
		return &models.TerraformModule{
			RepositoryID: repo, Name: "vpc", System: "aws", Version: version, ArchiveExt: ext,
			BlobKey: "tf/mod/" + version, Checksum: digest("mod" + version), SizeBytes: 17,
		}
	}

	m1 := mod("1.0.0", "zip")
	reservedWrite(t, pg, repo, 17, func() {
		if err := pg.CreateTerraformModule(ctx, m1); err != nil {
			t.Fatal(err)
		}
	})

	m2 := mod("1.1.0", "tar.gz")
	if err := pg.CreateTerraformModule(ctx, m2); err != nil {
		t.Fatal(err)
	}

	if err := pg.CreateTerraformModule(ctx, mod("1.0.0", "zip")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate module = %v, want ErrDuplicate", err)
	}

	prov := func(version, osName, arch string, protocols []string) *models.TerraformProvider {
		return &models.TerraformProvider{
			RepositoryID: repo, Type: "random", Version: version, OS: osName, Arch: arch, Protocols: protocols,
			BlobKey: "tf/prov/" + version + osName + arch, Checksum: digest("prov" + version + osName + arch), SizeBytes: 19,
		}
	}

	pa := prov("3.5.1", "linux", "amd64", []string{"5.0"})
	reservedWrite(t, pg, repo, 19, func() {
		if err := pg.CreateTerraformProvider(ctx, pa); err != nil {
			t.Fatal(err)
		}
	})

	pb := prov("3.5.1", "darwin", "arm64", []string{"5.0"})
	if err := pg.CreateTerraformProvider(ctx, pb); err != nil {
		t.Fatal(err)
	}

	pc := prov("3.6.0", "linux", "amd64", nil)
	if err := pg.CreateTerraformProvider(ctx, pc); err != nil {
		t.Fatal(err)
	}

	if err := pg.CreateTerraformProvider(ctx, prov("3.5.1", "linux", "amd64", []string{"5.0"})); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate platform = %v, want ErrDuplicate", err)
	}

	// Two packages (module, provider), four versions (two module versions, two
	// provider versions), five files (two archives, three platform zips).
	requireGeneric(t, pg, repo, genericRows{2, 4, 5})

	gm, err := pg.GetTerraformModule(ctx, repo, "vpc", "aws", "1.1.0")
	if err != nil || gm == nil {
		t.Fatalf("GetTerraformModule = %v, %v", gm, err)
	}

	requireEq(t, "module ID", gm.ID, m2.ID)
	requireEq(t, "module Name", gm.Name, "vpc")
	requireEq(t, "module System", gm.System, "aws")
	requireEq(t, "module ArchiveExt", gm.ArchiveExt, "tar.gz")
	requireEq(t, "module Checksum", gm.Checksum, digest("mod1.1.0"))
	requireEq(t, "module BlobKey", gm.BlobKey, "tf/mod/1.1.0")
	requireEq(t, "module SizeBytes", gm.SizeBytes, int64(17))

	mv, err := pg.ListTerraformModuleVersions(ctx, repo, "vpc", "aws")
	if err != nil || len(mv) != 2 || mv[0].Version != "1.0.0" || mv[1].Version != "1.1.0" {
		t.Fatalf("ListTerraformModuleVersions = %v, %v", mv, err)
	}

	ml, err := pg.ListTerraformModules(ctx, repo)
	if err != nil || len(ml) != 2 {
		t.Fatalf("ListTerraformModules = %v, %v (providers must not show up)", ml, err)
	}

	if err := pg.IncrementTerraformModuleDownloadCount(ctx, repo, m1.ID); err != nil {
		t.Fatal(err)
	}

	if g, _ := pg.GetTerraformModule(ctx, repo, "vpc", "aws", "1.0.0"); g == nil || g.DownloadCount != 1 {
		t.Errorf("module download count = %+v, want 1", g)
	}

	mf := readFile(t, pg, m1.ID)
	requireEq(t, "module path", mf.path, "modules/vpc/aws/1.0.0/vpc-aws-1.0.0.zip")
	requireEq(t, "module filename", mf.filename, "vpc-aws-1.0.0.zip")
	requireEq(t, "module file attrs", mf.attrs, `{"archive_ext": "zip"}`)
	requireEq(t, "module package name", mf.pkgName, "modules/vpc/aws")
	requireEq(t, "module package attrs", mf.pkgAttrs, `{"kind": "module", "name": "vpc", "system": "aws"}`)
	requireEq(t, "module unit_key", mf.unitKey, "")
	requireEq(t, "module downloads", mf.downloads, int64(1))
	requireEq(t, "module format", mf.format, "terraform")

	stamp := func() string {
		s, err := pg.TerraformModuleStamp(ctx, repo, "vpc", "aws")
		if err != nil {
			t.Fatal(err)
		}

		return s
	}

	before := stamp()

	if err := pg.IncrementTerraformModuleDownloadCount(ctx, repo, m1.ID); err != nil {
		t.Fatal(err)
	}

	requireEq(t, "module stamp after a download", stamp(), before)

	gp, err := pg.GetTerraformProvider(ctx, repo, "random", "3.5.1", "darwin", "arm64")
	if err != nil || gp == nil {
		t.Fatalf("GetTerraformProvider = %v, %v", gp, err)
	}

	requireEq(t, "provider ID", gp.ID, pb.ID)
	requireEq(t, "provider Type", gp.Type, "random")
	requireEq(t, "provider OS", gp.OS, "darwin")
	requireEq(t, "provider Arch", gp.Arch, "arm64")
	requireEq(t, "provider Protocols", fmt.Sprint(gp.Protocols), "[5.0]")
	requireEq(t, "provider Checksum", gp.Checksum, digest("prov3.5.1darwinarm64"))
	requireEq(t, "provider SizeBytes", gp.SizeBytes, int64(19))

	if none, err := pg.GetTerraformProvider(ctx, repo, "random", "3.5.1", "windows", "amd64"); err != nil || none != nil {
		t.Errorf("missing platform = %v, %v, want nil", none, err)
	}

	plat, err := pg.ListTerraformProviderPlatforms(ctx, repo, "random")
	if err != nil || len(plat) != 3 || plat[0].OS != "darwin" || plat[1].OS != "linux" || plat[2].Version != "3.6.0" {
		t.Fatalf("ListTerraformProviderPlatforms = %v, %v", plat, err)
	}

	if len(plat[2].Protocols) != 0 {
		t.Errorf("provider without protocols reads %v", plat[2].Protocols)
	}

	vp, err := pg.ListTerraformProviderVersionPlatforms(ctx, repo, "random", "3.5.1")
	if err != nil || len(vp) != 2 || vp[0].OS != "darwin" || vp[1].OS != "linux" {
		t.Fatalf("ListTerraformProviderVersionPlatforms = %v, %v", vp, err)
	}

	pl, err := pg.ListTerraformProviders(ctx, repo)
	if err != nil || len(pl) != 3 {
		t.Fatalf("ListTerraformProviders = %v, %v (modules must not show up)", pl, err)
	}

	if err := pg.IncrementTerraformProviderDownloadCount(ctx, repo, pa.ID); err != nil {
		t.Fatal(err)
	}

	pf := readFile(t, pg, pa.ID)
	requireEq(t, "provider path", pf.path, "providers/random/3.5.1/linux/amd64/terraform-provider-random_3.5.1_linux_amd64.zip")
	requireEq(t, "provider filename", pf.filename, "terraform-provider-random_3.5.1_linux_amd64.zip")
	requireEq(t, "provider arch", pf.arch, "linux_amd64")
	requireEq(t, "provider file attrs", pf.attrs, `{"os": "linux", "arch": "amd64"}`)
	requireEq(t, "provider package name", pf.pkgName, "providers/random")
	requireEq(t, "provider package attrs", pf.pkgAttrs, `{"kind": "provider", "type": "random"}`)
	requireEq(t, "provider version metadata", pf.verMeta, `{"protocols": ["5.0"]}`)
	requireEq(t, "provider downloads", pf.downloads, int64(1))

	pstamp := func() string {
		s, err := pg.TerraformProviderStamp(ctx, repo, "random")
		if err != nil {
			t.Fatal(err)
		}

		return s
	}

	psBefore := pstamp()

	// Deleting a platform keeps the version while another platform is live and
	// moves the stamp; deleting the last one prunes the version.
	if err := pg.DeleteTerraformProvider(ctx, repo, pb.ID); err != nil {
		t.Fatal(err)
	}

	if pstamp() == psBefore {
		t.Error("provider stamp did not move after a platform delete")
	}

	requireGeneric(t, pg, repo, genericRows{2, 4, 4})

	if err := pg.DeleteTerraformProvider(ctx, repo, pa.ID); err != nil {
		t.Fatal(err)
	}

	requireGeneric(t, pg, repo, genericRows{2, 3, 3})

	if err := pg.DeleteTerraformProvider(ctx, repo, pc.ID); err != nil {
		t.Fatal(err)
	}

	if err := pg.DeleteTerraformModule(ctx, repo, m1.ID); err != nil {
		t.Fatal(err)
	}

	if err := pg.DeleteTerraformModule(ctx, repo, m2.ID); err != nil {
		t.Fatal(err)
	}

	requireGeneric(t, pg, repo, genericRows{0, 0, 0})
}

func TestNixStoreRoundTrip(t *testing.T) { //nolint:gocyclo,cyclop // one round trip checks every typed read of the store
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	repo := newQuotaSeeder(t, pg, "rt-nix", "nix").repo

	hash := func(label string) string { return digest(label)[:32] }

	path := func(label, name, narFile string) *models.NixPath {
		return &models.NixPath{
			RepositoryID: repo, StoreHash: hash(label), Name: name, NarHash: "sha256:abc" + label, NarSize: 100, NarFile: narFile,
			Compression: "xz", References: []string{hash("ref") + "-glibc"}, Deriver: "x.drv", CA: "fixed:r:sha256:zz",
			Signatures: []string{"cache-1:sig"}, SHA256: digest("nar" + narFile), BlobKey: "nix/" + narFile, SizeBytes: 23,
		}
	}

	stage := func(np *models.NixPath) {
		t.Helper()

		if err := pg.PutNixPendingNar(ctx, &models.NixPendingNar{
			RepositoryID: repo, SHA256: np.SHA256, NarFile: np.NarFile, BlobKey: np.BlobKey, SizeBytes: np.SizeBytes,
		}); err != nil {
			t.Fatal(err)
		}
	}

	pending := func() int {
		t.Helper()

		var n int
		err := pg.GetDB().QueryRowContext(ctx, `SELECT count(*) FROM nix_pending_nars WHERE repository_id = $1`, repo).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}

		return n
	}

	a := path("a", "hello-2.12.1", "aaa.nar.xz")
	stage(a)

	if pending() != 1 {
		t.Fatal("NAR not staged")
	}

	reservedWrite(t, pg, repo, 23, func() {
		if err := pg.CreateNixPath(ctx, a); err != nil {
			t.Fatal(err)
		}
	})

	if a.ID == "" || a.CreatedAt.IsZero() {
		t.Fatalf("create did not fill id and times: %+v", a)
	}

	if pending() != 0 {
		t.Error("the staging row of the NAR survived the narinfo")
	}

	// Another store path of the same name (other hash) is another version of
	// one package; a third shares the NAR file and blob of the first.
	b := path("b", "hello-2.12.1", "bbb.nar.xz")
	c := path("c", "world-1.0", "aaa.nar.xz")
	b.References, c.Signatures, c.Deriver, c.CA = nil, nil, "", ""

	for _, np := range []*models.NixPath{b, c} {
		if err := pg.CreateNixPath(ctx, np); err != nil {
			t.Fatal(err)
		}
	}

	if err := pg.CreateNixPath(ctx, path("a", "other-name", "ddd.nar.xz")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate store hash = %v, want ErrDuplicate", err)
	}

	requireGeneric(t, pg, repo, genericRows{2, 3, 3})

	got, err := pg.GetNixPathByHash(ctx, repo, a.StoreHash)
	if err != nil || got == nil {
		t.Fatalf("GetNixPathByHash = %v, %v", got, err)
	}

	requireEq(t, "ID", got.ID, a.ID)
	requireEq(t, "StoreHash", got.StoreHash, a.StoreHash)
	requireEq(t, "Name", got.Name, "hello-2.12.1")
	requireEq(t, "NarHash", got.NarHash, "sha256:abca")
	requireEq(t, "NarSize", got.NarSize, int64(100))
	requireEq(t, "NarFile", got.NarFile, "aaa.nar.xz")
	requireEq(t, "Compression", got.Compression, "xz")
	requireEq(t, "References", fmt.Sprint(got.References), fmt.Sprint(a.References))
	requireEq(t, "Deriver", got.Deriver, "x.drv")
	requireEq(t, "CA", got.CA, "fixed:r:sha256:zz")
	requireEq(t, "Signatures", fmt.Sprint(got.Signatures), "[cache-1:sig]")
	requireEq(t, "BlobKey", got.BlobKey, "nix/aaa.nar.xz")
	requireEq(t, "SHA256", got.SHA256, digest("naraaa.nar.xz"))
	requireEq(t, "SizeBytes", got.SizeBytes, int64(23))

	if none, err := pg.GetNixPathByHash(ctx, repo, hash("missing")); err != nil || none != nil {
		t.Errorf("missing hash = %v, %v, want nil", none, err)
	}

	gc, _ := pg.GetNixPathByHash(ctx, repo, c.StoreHash)
	if gc == nil || len(gc.References) != 1 || gc.Signatures == nil || len(gc.Signatures) != 0 || gc.Deriver != "" || gc.CA != "" {
		t.Errorf("path with empty lists = %+v", gc)
	}

	// The NAR lookup answers with the oldest live path that points at the file.
	byNar, err := pg.GetNixPathByNarFile(ctx, repo, "aaa.nar.xz")
	if err != nil || byNar == nil || byNar.ID != a.ID {
		t.Fatalf("GetNixPathByNarFile = %v, %v, want the first path", byNar, err)
	}

	if none, err := pg.GetNixPathByNarFile(ctx, repo, "nope.nar.xz"); err != nil || none != nil {
		t.Errorf("missing NAR file = %v, %v, want nil", none, err)
	}

	list, err := pg.ListNixPaths(ctx, repo)
	if err != nil || len(list) != 3 {
		t.Fatalf("ListNixPaths = %v, %v", list, err)
	}

	for i := 1; i < len(list); i++ {
		if list[i-1].StoreHash >= list[i].StoreHash {
			t.Errorf("ListNixPaths not ordered by store hash: %s before %s", list[i-1].StoreHash, list[i].StoreHash)
		}
	}

	if err := pg.IncrementNixDownloadCount(ctx, a.ID, repo); err != nil {
		t.Fatal(err)
	}

	f := readFile(t, pg, a.ID)
	requireEq(t, "path", f.path, a.StoreHash+".narinfo")
	requireEq(t, "filename", f.filename, a.StoreHash+".narinfo")
	requireEq(t, "package name_key", f.pkgKey, "hello-2.12.1")
	requireEq(t, "version", f.version, a.StoreHash)
	requireEq(t, "version_key", f.versionKey, a.StoreHash)
	requireEq(t, "downloads", f.downloads, int64(1))
	requireEq(t, "unit_key", f.unitKey, "")
	requireEq(t, "format", f.format, "nix")
	requireEq(t, "blob sha256", f.blobSHA, digest("naraaa.nar.xz"))

	if got, _ := pg.GetNixPathByHash(ctx, repo, a.StoreHash); got == nil || got.DownloadCount != 1 {
		t.Errorf("download count = %+v, want 1", got)
	}

	if err := pg.DeleteNixPath(ctx, repo, a.ID); err != nil {
		t.Fatal(err)
	}

	requireGeneric(t, pg, repo, genericRows{2, 2, 2})

	// After deleting the first path the NAR lookup finds the one sharing the file.
	if byNar, _ := pg.GetNixPathByNarFile(ctx, repo, "aaa.nar.xz"); byNar == nil || byNar.ID != c.ID {
		t.Errorf("NAR lookup after delete = %+v, want the second path", byNar)
	}

	for _, np := range []*models.NixPath{b, c} {
		if err := pg.DeleteNixPath(ctx, repo, np.ID); err != nil {
			t.Fatal(err)
		}
	}

	requireGeneric(t, pg, repo, genericRows{0, 0, 0})

	// The signing key table is untouched by the port.
	key := &models.NixSigningKey{
		RepositoryID: repo, KeyName: "cache-1", PublicKey: "cache-1:pub", PrivateKeyEnc: []byte("sealed"), CreatedBy: "t",
	}
	if created, err := pg.CreateNixSigningKeyIfAbsent(ctx, key); err != nil || !created {
		t.Fatalf("CreateNixSigningKeyIfAbsent = %v, %v", created, err)
	}
}
