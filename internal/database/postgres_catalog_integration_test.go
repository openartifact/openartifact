// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

type catalogFixture struct {
	pg   *PostgresDB
	db   *sql.DB
	repo string
	seed *quotaSeeder
	// ids maps "name@version" to the package file id.
	ids map[string]string
}

// seedNPMVersion stores one npm version with a blob of the given size.
func (f *catalogFixture) seedNPMVersion(t *testing.T, name, version string, size int64, created time.Time) {
	t.Helper()

	f.ids[name+"@"+version] = seedPackageFile(t, f.pg, packageSeed{
		repo: f.repo, format: "npm", name: name, version: version, filename: name + "-" + version + ".tgz",
		blobSeed: fmt.Sprintf("%s/%s/%s", f.repo, name, version), blobSize: size, created: created,
	})
}

func newCatalogFixture(t *testing.T) *catalogFixture {
	t.Helper()

	pg := newQuotaTestDB(t, 4)
	db := pg.GetDB()

	repo := seedRepo(t, db, "catalog-npm", "npm")

	return &catalogFixture{pg: pg, db: db, repo: repo, seed: &quotaSeeder{tb: t, pg: pg, repo: repo}, ids: map[string]string{}}
}

// seedCatalog stores alpha 1.0.0 and 2.0.0 and beta 1.0.0, the latest alpha
// scanned with 1 critical, 2 high, 3 medium and 4 low findings.
func seedCatalog(t *testing.T) (*catalogFixture, time.Time) {
	t.Helper()

	f := newCatalogFixture(t)
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-24 * time.Hour)

	f.seedNPMVersion(t, "alpha", "1.0.0", 10, base)
	f.seedNPMVersion(t, "alpha", "2.0.0", 20, base.Add(time.Hour))
	f.seedNPMVersion(t, "beta", "1.0.0", 5, base.Add(2*time.Hour))

	job := seedScanJob(t, f.db, f.repo, f.ids["alpha@2.0.0"], "vuln", "grype", "completed", base.Add(3*time.Hour))
	seedVulnResult(t, f.db, job, `[]`, 1, 2, 3, 4, false)

	return f, base
}

func TestCatalogRollupGroupsVersions(t *testing.T) {
	f, _ := seedCatalog(t)
	ctx := context.Background()

	rows, total, err := f.pg.ListRepoPackageRollup(ctx, models.PackageRollupOptions{
		RepoID: f.repo, RepoFormat: "npm", SortByName: true, Page: 1, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}

	if total != 2 || len(rows) != 2 {
		t.Fatalf("total %d rows %d, want 2", total, len(rows))
	}

	a := rows[0]
	if a.Name != "alpha" || a.LatestVersion != "2.0.0" || a.VersionCount != 2 || a.SizeBytes != 20 ||
		!slices.Equal(a.LatestIDs, []string{f.ids["alpha@2.0.0"]}) {
		t.Fatalf("alpha rollup %+v", a)
	}
}

func TestCatalogRollupFlagged(t *testing.T) {
	f, _ := seedCatalog(t)
	ctx := context.Background()

	rows, total, err := f.pg.ListRepoPackageRollup(ctx, models.PackageRollupOptions{
		RepoID: f.repo, RepoFormat: "npm", Flagged: true, Page: 1, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}

	if total != 1 || len(rows) != 1 || rows[0].Name != "alpha" {
		t.Fatalf("flagged rollup total %d rows %+v", total, rows)
	}
}

func TestCatalogRollupPagePastTheEnd(t *testing.T) {
	f, _ := seedCatalog(t)
	ctx := context.Background()

	rows, total, err := f.pg.ListRepoPackageRollup(ctx, models.PackageRollupOptions{RepoID: f.repo, RepoFormat: "npm", Page: 5, Limit: 10})
	if err != nil || len(rows) != 0 || total != 2 {
		t.Fatalf("rows %d total %d err %v", len(rows), total, err)
	}
}

func TestCatalogArtifactSecurity(t *testing.T) {
	f, _ := seedCatalog(t)
	ctx := context.Background()

	sec, err := f.pg.ArtifactSecurity(ctx, f.repo, []string{f.ids["alpha@2.0.0"], f.ids["alpha@1.0.0"]})
	if err != nil {
		t.Fatal(err)
	}

	got, ok := sec[f.ids["alpha@2.0.0"]]
	if !ok || got.Critical != 1 || got.High != 2 || got.Medium != 3 || got.Low != 4 || got.ScannedAt == nil || got.Quarantined {
		t.Fatalf("scanned row %+v ok=%v", got, ok)
	}

	if _, ok := sec[f.ids["alpha@1.0.0"]]; ok {
		t.Fatal("an unscanned row must have no entry")
	}
}

func TestCatalogRepositoryStats(t *testing.T) {
	f, base := seedCatalog(t)
	ctx := context.Background()

	stats, err := f.pg.RepoCatalogStats(ctx, []string{f.repo}, true)
	if err != nil {
		t.Fatal(err)
	}

	s := stats[f.repo]
	if s.Security.Critical != 1 || s.Security.High != 2 || s.Security.ScannedAt == nil || !s.Security.Flagged() {
		t.Fatalf("security %+v", s.Security)
	}

	if s.LastPublishedAt == nil || s.LastPublishedAt.Before(base) {
		t.Fatalf("last published %v", s.LastPublishedAt)
	}

	counts, err := f.pg.CountRepoPackageNames(ctx, []string{f.repo})
	if err != nil || counts[f.repo] != 2 {
		t.Fatalf("counts %v err %v", counts, err)
	}
}

func TestCatalogRecentPackages(t *testing.T) {
	f, _ := seedCatalog(t)
	ctx := context.Background()

	recent, err := f.pg.ListRecentPackages(ctx, []string{f.repo}, 2)
	if err != nil {
		t.Fatal(err)
	}

	if len(recent) != 2 || recent[0].Name != "beta" || recent[1].Name != "alpha" || recent[1].Version != "2.0.0" ||
		recent[0].Repository != "catalog-npm" {
		t.Fatalf("recent %+v", recent)
	}

	if none, err := f.pg.ListRecentPackages(ctx, nil, 5); err != nil || len(none) != 0 {
		t.Fatalf("no repositories: %v %v", none, err)
	}
}

func TestCatalogComponentDetail(t *testing.T) {
	f, _ := seedCatalog(t)
	ctx := context.Background()

	d, err := f.pg.GetRepoComponentDetail(ctx, f.repo, f.ids["alpha@2.0.0"])
	if err != nil || d == nil || d.Name != "alpha" || d.Version != "2.0.0" || d.Format != "npm" {
		t.Fatalf("detail %+v err %v", d, err)
	}

	other := seedRepo(t, f.db, "catalog-other", "npm")
	if d, err := f.pg.GetRepoComponentDetail(ctx, other, f.ids["alpha@2.0.0"]); err != nil || d != nil {
		t.Fatalf("foreign repository must not resolve the row: %+v %v", d, err)
	}

	if d, err := f.pg.GetRepoComponentDetail(ctx, f.repo, "not-a-uuid"); err != nil || d != nil {
		t.Fatalf("malformed id: %+v %v", d, err)
	}
}
