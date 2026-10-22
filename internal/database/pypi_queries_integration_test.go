// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func addPyPIFile(t *testing.T, pg *PostgresDB, repoID, name, version string) {
	t.Helper()

	filename := name + "-" + version + ".tar.gz"
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(repoID+filename)))

	pkg := &models.PyPIPackage{
		RepositoryID: repoID, Name: name, Version: version, Filename: filename, PackageType: "sdist",
		SHA256Digest: sum, BlobKey: sum, SizeBytes: 10,
	}
	if err := pg.CreatePyPIPackage(context.Background(), pkg); err != nil {
		t.Fatalf("CreatePyPIPackage %s %s: %v", name, version, err)
	}
}

// The simple root lists distinct stored names (sorted, soft-deleted files
// ignored) and the per-project lookup still finds rows stored under a legacy
// "--" name through the normalised name.
func TestPyPIListNamesAndLegacyLookup(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	org := systemOrgID

	repo := &models.Repository{Name: "py-names", Format: "pypi", RepoType: "hosted", ProjectID: &org}
	if err := pg.CreateRepository(ctx, repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	other := &models.Repository{Name: "py-other", Format: "pypi", RepoType: "hosted", ProjectID: &org}
	if err := pg.CreateRepository(ctx, other); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	addPyPIFile(t, pg, repo.ID, "zeta", "1.0")
	addPyPIFile(t, pg, repo.ID, "zeta", "2.0")
	addPyPIFile(t, pg, repo.ID, "alpha", "1.0")
	addPyPIFile(t, pg, repo.ID, "my--legacy", "0.1")
	addPyPIFile(t, pg, repo.ID, "my-legacy", "0.2")
	addPyPIFile(t, pg, other.ID, "elsewhere", "1.0")

	names, err := pg.ListPyPIPackageNames(ctx, repo.ID)
	if err != nil {
		t.Fatalf("ListPyPIPackageNames: %v", err)
	}

	want := []string{"alpha", "my--legacy", "my-legacy", "zeta"}
	if !slices.Equal(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}

	versions, err := pg.ListPyPIPackageVersions(ctx, repo.ID, "my-legacy")
	if err != nil {
		t.Fatalf("ListPyPIPackageVersions: %v", err)
	}

	got := make([]string, 0, len(versions))
	for _, v := range versions {
		got = append(got, v.Name+"@"+v.Version)
	}

	if want := []string{"my-legacy@0.2", "my--legacy@0.1"}; !slices.Equal(got, want) {
		t.Fatalf("versions of my-legacy = %v, want %v (legacy row must be found, newest first)", got, want)
	}

	if v, _ := pg.ListPyPIPackageVersions(ctx, repo.ID, "elsewhere"); len(v) != 0 {
		t.Fatalf("lookup leaked rows from another repository: %d", len(v))
	}

	byName, err := pg.GetPyPIPackageByFilename(ctx, repo.ID, "alpha-1.0.tar.gz")
	if err != nil || byName == nil || byName.Name != "alpha" {
		t.Fatalf("GetPyPIPackageByFilename = %+v, %v", byName, err)
	}
}

// Every index the performance migration promises exists and is valid, and the
// duplicates it replaced are gone.
func TestPerfIndexesBuilt(t *testing.T) {
	pg := newQuotaTestDB(t, 2)
	ctx := context.Background()

	want := []string{
		"idx_pypi_packages_repo_filename", "idx_deb_packages_repo_filename", "idx_rpm_packages_repo_filename",
		"idx_pypi_packages_legacy_name", "idx_pypi_packages_name_trgm", "idx_maven_artifacts_name_trgm",
		"idx_deb_packages_component_id", "idx_auth_user_groups_group_id", "idx_repository_group_members_member",
	}

	for _, name := range want {
		var valid bool

		err := pg.db.QueryRowContext(ctx,
			`SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = $1`, name).Scan(&valid)
		if err != nil || !valid {
			t.Errorf("index %s: valid=%v err=%v", name, valid, err)
		}
	}

	for _, name := range []string{"idx_blobs_sha256", "idx_proxy_cache_repository_key", "idx_vuln_results_scan_job"} {
		var n int

		if err := pg.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_class WHERE relname = $1`, name).Scan(&n); err != nil || n != 0 {
			t.Errorf("duplicate index %s still present (n=%d err=%v)", name, n, err)
		}
	}
}
