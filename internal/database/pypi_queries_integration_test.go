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

// The simple root lists the stored names (sorted, projects without a live
// release ignored) and the per-project lookup is scoped to its repository.
func TestPyPIListNamesAndVersions(t *testing.T) {
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
	addPyPIFile(t, pg, repo.ID, "my-pkg", "0.2")
	addPyPIFile(t, pg, repo.ID, "my-pkg", "0.1")
	addPyPIFile(t, pg, other.ID, "elsewhere", "1.0")

	names, err := pg.ListPyPIPackageNames(ctx, repo.ID)
	if err != nil {
		t.Fatalf("ListPyPIPackageNames: %v", err)
	}

	want := []string{"alpha", "my-pkg", "zeta"}
	if !slices.Equal(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}

	versions, err := pg.ListPyPIPackageVersions(ctx, repo.ID, "my-pkg")
	if err != nil {
		t.Fatalf("ListPyPIPackageVersions: %v", err)
	}

	got := make([]string, 0, len(versions))
	for _, v := range versions {
		got = append(got, v.Name+"@"+v.Version)
	}

	if want := []string{"my-pkg@0.2", "my-pkg@0.1"}; !slices.Equal(got, want) {
		t.Fatalf("versions of my-pkg = %v, want %v (newest first)", got, want)
	}

	if v, _ := pg.ListPyPIPackageVersions(ctx, repo.ID, "elsewhere"); len(v) != 0 {
		t.Fatalf("lookup leaked rows from another repository: %d", len(v))
	}

	byName, err := pg.GetPyPIPackageByFilename(ctx, repo.ID, "alpha-1.0.tar.gz")
	if err != nil || byName == nil || byName.Name != "alpha" {
		t.Fatalf("GetPyPIPackageByFilename = %+v, %v", byName, err)
	}
}
