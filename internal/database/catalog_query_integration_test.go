// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// seedBulkVersions writes n live versions (one file each) of one npm package
// straight into the generic tables, a bulk the write path would take long to
// produce; q matches all of them through the package name.
func seedBulkVersions(t *testing.T, f *catalogFixture, name string, n int, created time.Time) {
	t.Helper()

	_, err := f.db.ExecContext(context.Background(), `
		WITH p AS (
			INSERT INTO packages (repository_id, format, name, name_key) VALUES ($1, 'npm', $2, $2) RETURNING id
		), v AS (
			INSERT INTO package_versions (package_id, repository_id, version, version_key, created_at, updated_at)
			SELECT p.id, $1, '9.' || g, '9.' || g, $4::timestamptz + g * interval '1 second', $4::timestamptz + g * interval '1 second'
			FROM p, generate_series(1, $3::int) g
			RETURNING id, version, created_at
		)
		INSERT INTO package_files (version_id, repository_id, path, filename, created_at, updated_at)
		SELECT id, $1, $2 || '/' || version, $2 || '-' || version || '.tgz', created_at, created_at FROM v`,
		f.repo, name, n, created)
	if err != nil {
		t.Fatalf("seed bulk versions: %v", err)
	}
}

// TestCatalogQueryIsLiteral: the q filter of the package listing and of the
// rollup is a literal, case-insensitive substring match: the LIKE wildcards and
// the escape character of the search term match themselves.
func TestCatalogQueryIsLiteral(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)

	for i, name := range []string{"a_b", "axb", "50%off", "fifty", `back\slash`, "backslash"} {
		f.seedNPMVersion(t, name, "1.0.0", 1, base.Add(time.Duration(i)*time.Minute))
	}

	for _, tc := range []struct {
		q    string
		want string
	}{
		{"a_b", "a_b"}, {"A_B", "a_b"}, {"_", "a_b"}, {"%", "50%off"}, {"0%o", "50%off"}, {`\`, `back\slash`}, {`k\s`, `back\slash`},
	} {
		for _, byName := range []bool{false, true} {
			rows, total, err := f.pg.SearchComponents(ctx, f.repo, ComponentListOptions{Query: tc.q, SortByName: byName, Page: 1, Limit: 100})
			if err != nil || total != 1 || len(rows) != 1 || rows[0].Name != tc.want {
				t.Errorf("listing q=%q byName=%v: total %d rows %d err %v, want only %q", tc.q, byName, total, len(rows), err, tc.want)
			}
		}

		rolled, total, err := f.pg.ListRepoPackageRollup(ctx, models.PackageRollupOptions{
			RepoID: f.repo, RepoFormat: "npm", Query: tc.q, Page: 1, Limit: 100,
		})
		if err != nil || total != 1 || len(rolled) != 1 || rolled[0].Name != tc.want {
			t.Errorf("rollup q=%q: total %d rows %d err %v, want only %q", tc.q, total, len(rolled), err, tc.want)
		}
	}
}

// TestCatalogQueryNarrowAndBroad: the listing and its count answer the same
// whether q matches few versions (read through the trigram indexes) or more
// than the narrow bound of the query (a walk of the listing index), and for a
// format the repository has no package of.
func TestCatalogQueryNarrowAndBroad(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-24 * time.Hour)

	f.seedNPMVersion(t, "alpha", "1.0.0", 10, base)
	f.seedNPMVersion(t, "needle-pkg", "3.1.4", 10, base.Add(time.Minute))
	seedBulkVersions(t, f, "bulk", 2100, base.Add(time.Hour))

	for _, byName := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			opts ComponentListOptions
			want int
		}{
			{"narrow, by name", ComponentListOptions{Query: "needle"}, 1},
			{"narrow, by version", ComponentListOptions{Query: "3.1.4"}, 1},
			{"narrow with format", ComponentListOptions{Query: "needle", Format: "npm"}, 1},
			{"broad", ComponentListOptions{Query: "bulk"}, 2100},
			{"broad, by version", ComponentListOptions{Query: "9."}, 2100},
			{"broad with format", ComponentListOptions{Query: "bulk", Format: "npm"}, 2100},
			{"every file", ComponentListOptions{Query: "."}, 2102},
			{"format only", ComponentListOptions{Format: "npm"}, 2102},
			{"format the repository has none of", ComponentListOptions{Format: "maven"}, 0},
			{"query and a format it has none of", ComponentListOptions{Query: "bulk", Format: "maven"}, 0},
			{"no match", ComponentListOptions{Query: "nomatchxyz"}, 0},
		} {
			tc.opts.SortByName, tc.opts.Page, tc.opts.Limit = byName, 1, 50

			rows, total, err := f.pg.SearchComponents(ctx, f.repo, tc.opts)
			if err != nil || total != tc.want || len(rows) != min(tc.want, 50) {
				t.Errorf("%s (byName=%v): total %d rows %d err %v, want total %d", tc.name, byName, total, len(rows), err, tc.want)
			}
		}
	}

	// The newest-first page of a broad q is the newest files.
	rows, _, err := f.pg.SearchComponents(ctx, f.repo, ComponentListOptions{Query: "bulk", Page: 1, Limit: 2})
	if err != nil || len(rows) != 2 || rows[0].Version != "9.2100" || rows[1].Version != "9.2099" {
		t.Fatalf("broad page %+v err %v", rows, err)
	}
}

// TestCatalogRollupMatchingVersions: with q, the rollup counts and names the
// matching versions of a package only, a package whose name matches counts all
// of them, and the sort key and the timestamps follow the live versions.
func TestCatalogRollupMatchingVersions(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-24 * time.Hour)

	f.seedNPMVersion(t, "alpha", "1.0.0", 10, base)
	f.seedNPMVersion(t, "alpha", "2.0.0", 20, base.Add(time.Hour))
	f.seedNPMVersion(t, "alpha", "2.1.0", 30, base.Add(2*time.Hour))
	f.seedNPMVersion(t, "beta", "1.0.0", 5, base.Add(90*time.Minute))

	roll := func(opts models.PackageRollupOptions) []models.PackageRollup {
		t.Helper()

		opts.RepoID, opts.RepoFormat, opts.Page, opts.Limit = f.repo, "npm", 1, 10

		rows, _, err := f.pg.ListRepoPackageRollup(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}

		return rows
	}

	// Newest first: alpha's newest version is 2.1.0 (+2h), beta's +1.5h.
	if rows := roll(models.PackageRollupOptions{}); len(rows) != 2 || rows[0].Name != "alpha" || rows[0].LatestVersion != "2.1.0" ||
		rows[0].VersionCount != 3 || rows[0].SizeBytes != 30 || !rows[0].CreatedAt.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("default rollup %+v", rows)
	}

	// q on the version: only the matching versions count, and the newest of them is the latest.
	rows := roll(models.PackageRollupOptions{Query: "2.0"})
	if len(rows) != 1 || rows[0].Name != "alpha" || rows[0].LatestVersion != "2.0.0" || rows[0].VersionCount != 1 || rows[0].SizeBytes != 20 {
		t.Fatalf("version q rollup %+v", rows)
	}

	// q on the name: every version of the package matches.
	if rows := roll(models.PackageRollupOptions{Query: "alp"}); len(rows) != 1 || rows[0].VersionCount != 3 || rows[0].LatestVersion != "2.1.0" {
		t.Fatalf("name q rollup %+v", rows)
	}

	// Exact name and format filters.
	if rows := roll(models.PackageRollupOptions{Name: "beta", Format: "npm"}); len(rows) != 1 || rows[0].Name != "beta" {
		t.Fatalf("name filter %+v", rows)
	}

	if rows := roll(models.PackageRollupOptions{Format: "maven"}); len(rows) != 0 {
		t.Fatalf("format without packages %+v", rows)
	}

	// A file added to the latest version later moves updated_at (the version row is not touched).
	touched := time.Now().UTC().Add(-time.Second)

	if _, err := f.db.ExecContext(ctx, `UPDATE package_files SET attrs = '{"touched":true}' WHERE id = $1`, f.ids["alpha@2.1.0"]); err != nil {
		t.Fatal(err)
	}

	if rows := roll(models.PackageRollupOptions{Name: "alpha"}); len(rows) != 1 || rows[0].UpdatedAt.Before(touched) {
		t.Fatalf("updated_at is not the file's after %v: %+v", touched, rows)
	}

	// Trashed versions drop out of the count, the latest and the package count.
	if _, err := f.db.ExecContext(ctx, `UPDATE package_files SET deleted_at = now() WHERE id = $1`, f.ids["alpha@2.1.0"]); err != nil {
		t.Fatal(err)
	}

	if _, err := f.db.ExecContext(ctx, `UPDATE package_versions SET deleted_at = now()
		WHERE id = (SELECT version_id FROM package_files WHERE id = $1)`, f.ids["alpha@2.1.0"]); err != nil {
		t.Fatal(err)
	}

	if rows := roll(models.PackageRollupOptions{Name: "alpha"}); len(rows) != 1 || rows[0].LatestVersion != "2.0.0" || rows[0].VersionCount != 2 {
		t.Fatalf("after trash %+v", rows)
	}

	for _, id := range []string{"beta@1.0.0"} {
		if _, err := f.db.ExecContext(ctx, `UPDATE package_files SET deleted_at = now() WHERE id = $1`, f.ids[id]); err != nil {
			t.Fatal(err)
		}

		if _, err := f.db.ExecContext(ctx, `UPDATE package_versions SET deleted_at = now()
			WHERE id = (SELECT version_id FROM package_files WHERE id = $1)`, f.ids[id]); err != nil {
			t.Fatal(err)
		}
	}

	counts, err := f.pg.CountRepoPackageNames(ctx, []string{f.repo})
	if err != nil || counts[f.repo] != 1 {
		t.Fatalf("package count %v err %v, want 1 (a package without a live version is not listed)", counts, err)
	}

	if rows := roll(models.PackageRollupOptions{}); len(rows) != 1 || rows[0].Name != "alpha" {
		t.Fatalf("rollup after trashing beta %+v", rows)
	}
}

// TestCatalogPackageCountPerRepository: the count is per repository and ignores
// the packages of the others, a repository without packages is 0.
func TestCatalogPackageCountPerRepository(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)

	f.seedNPMVersion(t, "alpha", "1.0.0", 1, base)
	f.seedNPMVersion(t, "alpha", "1.1.0", 1, base)
	f.seedNPMVersion(t, "beta", "1.0.0", 1, base)

	other := seedRepo(t, f.db, "catalog-other", "npm")
	empty := seedRepo(t, f.db, "catalog-empty", "npm")

	seedPackageFile(t, f.pg, packageSeed{
		repo: other, format: "npm", name: "gamma", version: "1.0.0", filename: "gamma.tgz", blobSeed: "gamma", blobSize: 1, created: base,
	})

	counts, err := f.pg.CountRepoPackageNames(ctx, []string{f.repo, other, empty})
	if err != nil {
		t.Fatal(err)
	}

	if counts[f.repo] != 2 || counts[other] != 1 || counts[empty] != 0 {
		t.Fatalf("counts %v", counts)
	}
}

// TestCatalogRollupFlaggedLatestOnly: a package is flagged by its latest
// version only, an older flagged version does not flag it, and q applies to the
// versions the latest is picked from.
func TestCatalogRollupFlaggedLatestOnly(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-24 * time.Hour)

	f.seedNPMVersion(t, "alpha", "1.0.0", 10, base)
	f.seedNPMVersion(t, "alpha", "2.0.0", 20, base.Add(time.Hour))
	f.seedNPMVersion(t, "beta", "1.0.0", 5, base.Add(2*time.Hour))

	// alpha 1.0.0 and beta 1.0.0 are flagged; alpha's latest (2.0.0) is clean.
	for _, key := range []string{"alpha@1.0.0", "beta@1.0.0"} {
		job := seedScanJob(t, f.db, f.repo, f.ids[key], "vuln", "grype", "completed", base.Add(3*time.Hour))
		seedVulnResult(t, f.db, job, `[]`, 1, 0, 0, 0, false)
	}

	roll := func(opts models.PackageRollupOptions) ([]models.PackageRollup, int) {
		t.Helper()

		opts.RepoID, opts.RepoFormat, opts.Flagged, opts.Page, opts.Limit = f.repo, "npm", true, 1, 10

		rows, total, err := f.pg.ListRepoPackageRollup(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}

		return rows, total
	}

	if rows, total := roll(models.PackageRollupOptions{}); total != 1 || len(rows) != 1 || rows[0].Name != "beta" {
		t.Fatalf("flagged: total %d rows %+v, want beta only", total, rows)
	}

	// Under q=1.0, alpha's latest matching version is the flagged 1.0.0.
	if rows, total := roll(models.PackageRollupOptions{Query: "1.0"}); total != 2 || len(rows) != 2 {
		t.Fatalf("flagged with q=1.0: total %d rows %+v, want alpha and beta", total, rows)
	}
}
