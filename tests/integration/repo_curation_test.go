// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"errors"
	"testing"

	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

func newCurationProxy(t *testing.T, name, mode string) *models.Repository {
	t.Helper()

	orgID := systemOrgID
	repo := &models.Repository{
		Name: name, Format: "npm", RepoType: "proxy", UpstreamURL: "https://registry.example.com",
		CacheTTLSeconds: 3600, NegativeCacheTTLSeconds: 60, MaxStaleSeconds: 60, CurationMode: mode, ProjectID: &orgID,
	}

	if err := harness.Database.CreateRepository(t.Context(), repo); err != nil {
		t.Fatalf("CreateRepository %s: %v", name, err)
	}

	return repo
}

// TestRepositoryCurationModeRoundTrip checks curation_mode survives create, read
// and update, defaults to off, and that the CHECK constraint rejects an unknown
// mode.
func TestRepositoryCurationModeRoundTrip(t *testing.T) {
	ctx := t.Context()

	repo := newCurationProxy(t, "curation-mode-px", curation.ModeDenylist)

	got, err := harness.Database.GetRepositoryByName(ctx, repo.Name)
	if err != nil || got == nil || got.CurationMode != curation.ModeDenylist {
		t.Fatalf("after create: %+v, %v; want mode denylist", got, err)
	}

	got.CurationMode = curation.ModeAllowlist
	if err := harness.Database.UpdateRepository(ctx, got); err != nil {
		t.Fatalf("UpdateRepository: %v", err)
	}

	got, err = harness.Database.GetRepositoryByName(ctx, repo.Name)
	if err != nil || got == nil || got.CurationMode != curation.ModeAllowlist {
		t.Fatalf("after update: %+v, %v; want mode allowlist", got, err)
	}

	got.CurationMode = "greylist"
	if err := harness.Database.UpdateRepository(ctx, got); err == nil {
		t.Error("UpdateRepository accepted an unknown curation_mode")
	}

	unset := newCurationProxy(t, "curation-default-px", "")
	if got, err = harness.Database.GetRepositoryByName(ctx, unset.Name); err != nil || got == nil || got.CurationMode != curation.ModeOff {
		t.Fatalf("repository created without a mode: %+v, %v; want off", got, err)
	}
}

// curationFixture is two proxy repositories and two rules in the first one.
type curationFixture struct {
	repo, other   *models.Repository
	first, second *curation.Rule
}

func newCurationFixture(t *testing.T, prefix string) *curationFixture {
	t.Helper()

	ctx := t.Context()
	fx := &curationFixture{
		repo:  newCurationProxy(t, prefix+"-px", curation.ModeDenylist),
		other: newCurationProxy(t, prefix+"-other", curation.ModeDenylist),
	}

	var err error

	fx.first, err = harness.Database.CreateCurationRule(ctx, &curation.Rule{
		RepositoryID: fx.repo.ID, Action: curation.ActionBlock, Package: "left-pad", Version: "1.*", Reason: "malware", CreatedBy: "alice",
	})
	if err != nil {
		t.Fatalf("CreateCurationRule: %v", err)
	}

	fx.second, err = harness.Database.CreateCurationRule(ctx, &curation.Rule{
		RepositoryID: fx.repo.ID, Action: curation.ActionAllow, Package: "@acme/*",
	})
	if err != nil {
		t.Fatalf("CreateCurationRule second: %v", err)
	}

	return fx
}

// Rules come back in creation order and only for their own repository.
func TestCurationRuleCreateAndList(t *testing.T) {
	ctx := t.Context()
	fx := newCurationFixture(t, "curation-list")

	if fx.first.ID == "" || fx.first.CreatedAt.IsZero() || fx.first.CreatedBy != "alice" || fx.first.Reason != "malware" {
		t.Fatalf("created rule = %+v", fx.first)
	}

	rules, err := harness.Database.ListCurationRules(ctx, fx.repo.ID)
	if err != nil || len(rules) != 2 || rules[0].ID != fx.first.ID || rules[1].ID != fx.second.ID {
		t.Fatalf("ListCurationRules = %+v, %v; want the two rules in creation order", rules, err)
	}

	if rules, err = harness.Database.ListCurationRules(ctx, fx.other.ID); err != nil || len(rules) != 0 {
		t.Fatalf("other repository lists %+v, %v; want none", rules, err)
	}

	for _, bad := range []curation.Rule{
		{RepositoryID: fx.repo.ID, Action: "hide", Package: "x"},
		{RepositoryID: fx.repo.ID, Action: curation.ActionBlock, Package: ""},
	} {
		if _, err := harness.Database.CreateCurationRule(ctx, &bad); err == nil {
			t.Errorf("CreateCurationRule accepted %+v", bad)
		}
	}
}

// Every operation by id is scoped to the repository it is asked in.
func TestCurationRuleIDsAreScopedToTheirRepository(t *testing.T) {
	ctx := t.Context()
	db := harness.Database
	fx := newCurationFixture(t, "curation-scope")

	if _, err := db.GetCurationRule(ctx, fx.other.ID, fx.first.ID); !errors.Is(err, database.ErrCurationRuleNotFound) {
		t.Errorf("GetCurationRule of another repository: %v, want ErrCurationRuleNotFound", err)
	}

	moved := *fx.first
	moved.RepositoryID, moved.Reason = fx.other.ID, "stolen"

	if _, err := db.UpdateCurationRule(ctx, &moved); !errors.Is(err, database.ErrCurationRuleNotFound) {
		t.Errorf("UpdateCurationRule of another repository: %v, want ErrCurationRuleNotFound", err)
	}

	if err := db.DeleteCurationRule(ctx, fx.other.ID, fx.first.ID); !errors.Is(err, database.ErrCurationRuleNotFound) {
		t.Errorf("DeleteCurationRule of another repository: %v, want ErrCurationRuleNotFound", err)
	}

	if got, err := db.GetCurationRule(ctx, fx.repo.ID, fx.first.ID); err != nil || got.Reason != "malware" {
		t.Errorf("rule after the refused writes = %+v, %v", got, err)
	}
}

func TestCurationRuleUpdateAndDelete(t *testing.T) {
	ctx := t.Context()
	db := harness.Database
	fx := newCurationFixture(t, "curation-write")

	changed := *fx.first
	changed.Action, changed.Package, changed.Version, changed.Reason = curation.ActionAllow, "left-pad", "", "reviewed"

	updated, err := db.UpdateCurationRule(ctx, &changed)
	if err != nil {
		t.Fatalf("UpdateCurationRule: %v", err)
	}

	if updated.Action != curation.ActionAllow || updated.Version != "" || updated.Reason != "reviewed" ||
		updated.CreatedBy != "alice" || !updated.CreatedAt.Equal(fx.first.CreatedAt) {
		t.Errorf("updated = %+v, want the new fields and the original creator and time", updated)
	}

	if err := db.DeleteCurationRule(ctx, fx.repo.ID, fx.first.ID); err != nil {
		t.Fatalf("DeleteCurationRule: %v", err)
	}

	if err := db.DeleteCurationRule(ctx, fx.repo.ID, fx.first.ID); !errors.Is(err, database.ErrCurationRuleNotFound) {
		t.Errorf("second delete: %v, want ErrCurationRuleNotFound", err)
	}

	if rules, err := db.ListCurationRules(ctx, fx.repo.ID); err != nil || len(rules) != 1 || rules[0].ID != fx.second.ID {
		t.Errorf("ListCurationRules = %+v, %v; want only the second rule", rules, err)
	}
}

// A repository holds at most curation.MaxRules rules, and a full one does not
// affect another; the rules go with their repository.
func TestCurationRuleLimitAndCascade(t *testing.T) {
	ctx := t.Context()
	db := harness.Database
	fx := newCurationFixture(t, "curation-limit")

	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO curation_rules (repository_id, action, package_pattern)
		SELECT $1, 'block', 'pkg-' || g FROM generate_series(1, $2::int) g`,
		fx.other.ID, curation.MaxRules); err != nil {
		t.Fatalf("fill the repository with rules: %v", err)
	}

	if _, err := db.CreateCurationRule(ctx, &curation.Rule{
		RepositoryID: fx.other.ID, Action: curation.ActionBlock, Package: "one-too-many",
	}); !errors.Is(err, database.ErrCurationRuleLimit) {
		t.Errorf("rule number %d: %v, want ErrCurationRuleLimit", curation.MaxRules+1, err)
	}

	if _, err := db.CreateCurationRule(ctx, &curation.Rule{
		RepositoryID: fx.repo.ID, Action: curation.ActionBlock, Package: "unaffected",
	}); err != nil {
		t.Errorf("a full repository blocked another one: %v", err)
	}

	if _, err := harness.DB.ExecContext(ctx, `DELETE FROM repositories WHERE id = $1`, fx.other.ID); err != nil {
		t.Fatalf("delete repository: %v", err)
	}

	var n int

	row := harness.DB.QueryRowContext(ctx, `SELECT count(*) FROM curation_rules WHERE repository_id = $1`, fx.other.ID)
	if err := row.Scan(&n); err != nil || n != 0 {
		t.Errorf("rules left after the repository was deleted: %d, %v", n, err)
	}
}

// A write through a database that caches its rule lists is visible to the next
// read at once: a rule that was just blocked must not be served around for the
// cache TTL.
func TestCurationRulesCacheInvalidatedByWrites(t *testing.T) {
	ctx := t.Context()

	host, err := harness.Container.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}

	port, err := harness.Container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("container port: %v", err)
	}

	cached, err := database.NewPostgresDB(database.DatabaseConfig{
		Driver: "postgres", Host: host, Port: int(port.Num()), User: "testuser", Password: "testpass", Database: "testdb",
		SSLMode: "disable", MaxOpenConns: 4, MaxIdleConns: 2, ConnMaxLifetimeMinutes: 5, AuthCacheTTLMillis: 600000,
	}, harness.Log)
	if err != nil {
		t.Fatalf("open a database with the auth cache on: %v", err)
	}

	t.Cleanup(func() { _ = cached.Close() })

	repo := newCurationProxy(t, "curation-cache-px", curation.ModeDenylist)

	list := func(want int) []curation.Rule {
		t.Helper()

		rules, err := cached.ListCurationRules(ctx, repo.ID)
		if err != nil || len(rules) != want {
			t.Fatalf("ListCurationRules = %+v, %v; want %d rules", rules, err, want)
		}

		return rules
	}

	list(0) // primes the cache

	created, err := cached.CreateCurationRule(ctx, &curation.Rule{RepositoryID: repo.ID, Action: curation.ActionBlock, Package: "evil"})
	if err != nil {
		t.Fatalf("CreateCurationRule: %v", err)
	}

	if got := list(1); got[0].Package != "evil" {
		t.Fatalf("rule after create = %+v", got[0])
	}

	created.Package = "worse"
	if _, err := cached.UpdateCurationRule(ctx, created); err != nil {
		t.Fatalf("UpdateCurationRule: %v", err)
	}

	if got := list(1); got[0].Package != "worse" {
		t.Fatalf("rule after update = %+v", got[0])
	}

	if err := cached.DeleteCurationRule(ctx, repo.ID, created.ID); err != nil {
		t.Fatalf("DeleteCurationRule: %v", err)
	}

	list(0)
}

// seedVersionWithScan stores one package version with one file and a completed
// scan of the file, keyed by the file's path the way a download is scanned.
func seedVersionWithScan(t *testing.T, repoID, pkg, version, scanType string, flagged bool) {
	t.Helper()

	ctx := t.Context()
	db := harness.DB

	var pkgID, verID string

	if err := db.QueryRowContext(ctx,
		`INSERT INTO packages (repository_id, format, name, name_key) VALUES ($1, 'npm', $2, lower($2)) RETURNING id`,
		repoID, pkg).Scan(&pkgID); err != nil {
		t.Fatalf("insert package: %v", err)
	}

	if err := db.QueryRowContext(ctx,
		`INSERT INTO package_versions (package_id, repository_id, version, version_key) VALUES ($1, $2, $3, $3) RETURNING id`,
		pkgID, repoID, version).Scan(&verID); err != nil {
		t.Fatalf("insert version: %v", err)
	}

	path := pkg + "/-/" + pkg + "-" + version + ".tgz"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO package_files (version_id, repository_id, path, filename) VALUES ($1, $2, $3, $4)`,
		verID, repoID, path, pkg+"-"+version+".tgz"); err != nil {
		t.Fatalf("insert file: %v", err)
	}

	var refID, jobID string

	if err := db.QueryRowContext(ctx,
		`INSERT INTO artifact_refs (repository_id, artifact_path, format) VALUES ($1, $2, 'npm') RETURNING id`,
		repoID, "/"+path).Scan(&refID); err != nil {
		t.Fatalf("insert artifact ref: %v", err)
	}

	if err := db.QueryRowContext(ctx,
		`INSERT INTO scan_jobs (artifact_ref_id, scan_type, status, completed_at) VALUES ($1, $2, 'completed', now()) RETURNING id`,
		refID, scanType).Scan(&jobID); err != nil {
		t.Fatalf("insert scan job: %v", err)
	}

	var query string

	switch scanType {
	case "vuln":
		query = `INSERT INTO vuln_results (scan_job_id, quarantined) VALUES ($1, $2)`
	case "secret":
		query = `INSERT INTO secret_results (scan_job_id, quarantined) VALUES ($1, $2)`
	case "license":
		query = `INSERT INTO license_results (scan_job_id, quarantined) VALUES ($1, $2)`
	default:
		t.Fatalf("unsupported scan type %q", scanType)
	}

	if _, err := db.ExecContext(ctx, query, jobID, flagged); err != nil {
		t.Fatalf("insert %s result: %v", scanType, err)
	}
}

// TestPackageVersionHolds checks the explain endpoint's view of a hosted
// version: vulnerability and secret quarantine, the license policy, a clean
// scan, an unscanned version, and that another repository's scan never leaks in.
func TestPackageVersionHolds(t *testing.T) {
	ctx := t.Context()
	orgID := systemOrgID
	repo := &models.Repository{Name: "holds-hosted", Format: "npm", RepoType: "hosted", ProjectID: &orgID}
	other := &models.Repository{Name: "holds-other", Format: "npm", RepoType: "hosted", ProjectID: &orgID}

	for _, r := range []*models.Repository{repo, other} {
		if err := harness.Database.CreateRepository(ctx, r); err != nil {
			t.Fatalf("CreateRepository %s: %v", r.Name, err)
		}
	}

	seedVersionWithScan(t, repo.ID, "vulnerable", "1.0.0", "vuln", true)
	seedVersionWithScan(t, repo.ID, "leaky", "1.0.0", "secret", true)
	seedVersionWithScan(t, repo.ID, "gpl-thing", "2.0.0", "license", true)
	seedVersionWithScan(t, repo.ID, "clean", "1.0.0", "vuln", false)
	seedVersionWithScan(t, repo.ID, "Mixed-Case", "3.0.0", "vuln", true)
	seedVersionWithScan(t, other.ID, "clean", "1.0.0", "vuln", true)

	tests := []struct {
		name     string
		repoID   string
		pkg, ver string
		want     models.VersionHolds
	}{
		{"vulnerability", repo.ID, "vulnerable", "1.0.0", models.VersionHolds{Quarantined: true}},
		{"secret", repo.ID, "leaky", "1.0.0", models.VersionHolds{Quarantined: true}},
		{"license policy", repo.ID, "gpl-thing", "2.0.0", models.VersionHolds{LicenseBlocked: true}},
		{"clean scan", repo.ID, "clean", "1.0.0", models.VersionHolds{}},
		{"other version of a flagged package", repo.ID, "vulnerable", "2.0.0", models.VersionHolds{}},
		{"unknown package", repo.ID, "nothing", "1.0.0", models.VersionHolds{}},
		{"package matched by its identity key", repo.ID, "mixed-case", "3.0.0", models.VersionHolds{Quarantined: true}},
		{"package matched by its display name", repo.ID, "Mixed-Case", "3.0.0", models.VersionHolds{Quarantined: true}},
		{"scan of another repository", repo.ID, "clean", "1.0.0", models.VersionHolds{}},
		{"flagged in the repository that owns the scan", other.ID, "clean", "1.0.0", models.VersionHolds{Quarantined: true}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := harness.Database.PackageVersionHolds(ctx, tc.repoID, tc.pkg, tc.ver)
			if err != nil {
				t.Fatalf("PackageVersionHolds: %v", err)
			}

			if got != tc.want {
				t.Errorf("holds = %+v, want %+v", got, tc.want)
			}
		})
	}
}
