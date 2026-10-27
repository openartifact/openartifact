// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

func nugetPkg(repoID, id, version string, blob int) *models.NuGetPackage {
	sha := fmt.Sprintf("%064x", blob)

	return &models.NuGetPackage{
		RepositoryID: repoID, PackageID: id, Version: version, Description: "d " + version,
		BlobKey: sha, PackageSize: 10,
	}
}

// NU-4: creating a live (repository, id, version) twice reports
// database.ErrDuplicate and leaves the first row (and its blob) untouched.
func TestNuGetCreateDuplicateKeepsFirst(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repoID := newRepo(t, pg, "nuget", "nuget")

	if err := pg.CreateNuGetPackage(ctx, nugetPkg(repoID, "contoso.lib", "1.0.0", 1)); err != nil {
		t.Fatalf("first create: %v", err)
	}

	second := nugetPkg(repoID, "contoso.lib", "1.0.0", 2)
	second.Description = "replacement"

	if err := pg.CreateNuGetPackage(ctx, second); !errors.Is(err, database.ErrDuplicate) {
		t.Fatalf("second create = %v, want ErrDuplicate", err)
	}

	got, err := pg.GetNuGetPackage(ctx, repoID, "contoso.lib", "1.0.0")
	if err != nil || got == nil || got.Description != "d 1.0.0" || got.BlobKey != fmt.Sprintf("%064x", 1) {
		t.Fatalf("stored row = %+v, %v; want the first push untouched", got, err)
	}

	// A trashed row does not block publishing the version again.
	if err := pg.DeleteNuGetPackage(ctx, repoID, "contoso.lib", "1.0.0"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if err := pg.CreateNuGetPackage(ctx, second); err != nil {
		t.Fatalf("create after delete: %v", err)
	}
}

// searchNuGet runs a search and returns "id@version" per returned row.
func searchNuGet(t *testing.T, pg *database.PostgresDB, repoID, query string, skip, take int) ([]string, int) {
	t.Helper()

	rows, total, err := pg.SearchNuGetPackages(context.Background(), repoID, query, skip, take)
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}

	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.PackageID+"@"+r.Version)
	}

	return ids, total
}

// NU-5: the search pages over distinct packages, returns every live version of
// the packages on the page and totals packages, not versions.
func TestNuGetSearchGroupsPackages(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repoID := newRepo(t, pg, "nuget", "nuget")
	other := newRepo(t, pg, "nuget2", "nuget")

	for i, c := range []struct{ id, version string }{
		{"alpha.lib", "1.0.0"}, {"alpha.lib", "1.1.0"}, {"alpha.lib", "2.0.0-beta"},
		{"beta.lib", "0.1.0"},
		{"gamma.lib", "3.0.0"}, {"gamma.lib", "3.1.0"},
	} {
		if err := pg.CreateNuGetPackage(ctx, nugetPkg(repoID, c.id, c.version, i+1)); err != nil {
			t.Fatal(err)
		}
	}

	// Same id in another repository never leaks into this one.
	if err := pg.CreateNuGetPackage(ctx, nugetPkg(other, "alpha.lib", "9.9.9", 50)); err != nil {
		t.Fatal(err)
	}

	ids, total := searchNuGet(t, pg, repoID, "", 0, 20)
	if total != 3 || len(ids) != 6 {
		t.Fatalf("total = %d, rows = %d; want 3 packages / 6 versions", total, len(ids))
	}

	// skip/take count packages: the second page is beta.lib with its one version.
	ids, total = searchNuGet(t, pg, repoID, "", 1, 1)
	if total != 3 || !slices.Equal(ids, []string{"beta.lib@0.1.0"}) {
		t.Fatalf("page = %v (total %d); want beta.lib only", ids, total)
	}

	// take=2 over alpha and beta returns alpha's three versions plus beta's.
	ids, total = searchNuGet(t, pg, repoID, "", 0, 2)
	if total != 3 || !slices.Equal(ids, []string{"alpha.lib@1.0.0", "alpha.lib@1.1.0", "alpha.lib@2.0.0-beta", "beta.lib@0.1.0"}) {
		t.Fatalf("total = %d, rows = %v", total, ids)
	}

	// A query counts matching packages, not matching versions.
	ids, total = searchNuGet(t, pg, repoID, "gamma", 0, 20)
	if total != 1 || len(ids) != 2 {
		t.Fatalf("query gamma: total %d rows %v; want 1 package / 2 versions", total, ids)
	}

	// A deleted version drops out of the group.
	if err := pg.DeleteNuGetPackage(ctx, repoID, "gamma.lib", "3.1.0"); err != nil {
		t.Fatal(err)
	}

	ids, total = searchNuGet(t, pg, repoID, "gamma", 0, 20)
	if total != 1 || !slices.Equal(ids, []string{"gamma.lib@3.0.0"}) {
		t.Fatalf("after delete: total %d rows %v", total, ids)
	}
}

// condaFixture is one stored file of scikit-learn 1.0 py39_0 on linux-64.
func condaFixture(repoID, ext string, blob int) *models.CondaPackage {
	sha := fmt.Sprintf("%064x", blob)

	return &models.CondaPackage{
		RepositoryID: repoID, Name: "scikit-learn", Version: "1.0", BuildString: "py39_0", Platform: "linux-64",
		Extension: ext, BuildNumber: 7, Timestamp: 1700000000123, MD5: fmt.Sprintf("%032x", blob),
		Depends: []string{"python >=3.9", "numpy"}, Extra: []byte(`{"license":"BSD-3-Clause","noarch":"python"}`),
		SHA256: sha, BlobKey: sha, SizeBytes: 100,
	}
}

// checkCondaRow verifies the .conda row (blob 2) round-tripped every field.
func checkCondaRow(t *testing.T, got *models.CondaPackage) {
	t.Helper()

	if got.Extension != models.CondaExtConda || got.BuildNumber != 7 || got.Timestamp != 1700000000123 ||
		got.MD5 != fmt.Sprintf("%032x", 2) || !slices.Equal(got.Depends, []string{"python >=3.9", "numpy"}) ||
		got.SHA256 != fmt.Sprintf("%064x", 2) {
		t.Errorf("round-tripped .conda row = %+v", got)
	}

	var extra map[string]string
	if err := json.Unmarshal(
		got.Extra,
		&extra,
	); err != nil || extra["license"] != "BSD-3-Clause" || extra["noarch"] != "python" ||
		len(extra) != 2 {
		t.Errorf("extra = %s (%v)", got.Extra, err)
	}
}

// CONDA-2/3: a .tar.bz2 and a .conda of one build are separate rows; the file
// name, build number, md5, timestamp, depends and extra fields round-trip.
func TestCondaFilesRoundTripAndCoexist(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repoID := newRepo(t, pg, "conda", "conda")

	for i, ext := range []string{models.CondaExtTarBz2, models.CondaExtConda} {
		if err := pg.CreateCondaPackage(ctx, condaFixture(repoID, ext, i+1)); err != nil {
			t.Fatalf("create %s: %v", ext, err)
		}
	}

	if err := pg.CreateCondaPackage(ctx, condaFixture(repoID, models.CondaExtConda, 3)); !errors.Is(err, database.ErrDuplicate) {
		t.Fatalf("duplicate .conda = %v, want ErrDuplicate", err)
	}

	list, err := pg.ListCondaPackages(ctx, repoID, "linux-64")
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d, %v; want 2", len(list), err)
	}

	var names []string
	for _, p := range list {
		names = append(names, p.Filename())
	}

	slices.Sort(names)

	if !slices.Equal(names, []string{"scikit-learn-1.0-py39_0.conda", "scikit-learn-1.0-py39_0.tar.bz2"}) {
		t.Errorf("filenames = %v", names)
	}

	got, err := pg.GetCondaPackageFile(ctx, repoID, "scikit-learn", "1.0", "py39_0", "linux-64", models.CondaExtConda)
	if err != nil || got == nil {
		t.Fatalf("GetCondaPackageFile = %v, %v", got, err)
	}

	checkCondaRow(t, got)

	if missing, err := pg.GetCondaPackageFile(
		ctx,
		repoID,
		"scikit-learn",
		"1.0",
		"py39_0",
		"osx-64",
		models.CondaExtConda,
	); err != nil ||
		missing != nil {
		t.Errorf("lookup on another platform = %v, %v; want nil", missing, err)
	}

	// Deleting one file keeps the other.
	if err := pg.DeleteCondaPackage(ctx, got.ID); err != nil {
		t.Fatal(err)
	}

	if still, _ := pg.GetCondaPackageFile(ctx, repoID, "scikit-learn", "1.0", "py39_0", "linux-64", models.CondaExtTarBz2); still == nil {
		t.Error("deleting the .conda removed the .tar.bz2")
	}

	// The repository component listing names each file by its extension.
	demo := condaFixture(repoID, models.CondaExtConda, 4)
	demo.Name, demo.Version, demo.BuildString = "demo", "2.0", "h1_0"

	if err := pg.CreateCondaPackage(ctx, demo); err != nil {
		t.Fatal(err)
	}

	comps, _, err := pg.ListComponentsByRepo(ctx, repoID, "conda", 1, 50)
	if err != nil {
		t.Fatal(err)
	}

	var files []string
	for _, c := range comps {
		files = append(files, c.Filename)
	}

	slices.Sort(files)

	if !slices.Equal(files, []string{"demo-2.0-h1_0.conda", "scikit-learn-1.0-py39_0.tar.bz2"}) {
		t.Errorf("component filenames = %v", files)
	}
}
