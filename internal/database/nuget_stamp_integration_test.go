// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"fmt"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// seedNuGetBench stores 500 one-version packages and one package of 300
// versions, the sizes the registration and flat-container routes are measured
// at.
func seedNuGetBench(b *testing.B) (*PostgresDB, string) {
	b.Helper()

	pg := newQuotaTestDB(b, 8)
	repo := newQuotaSeeder(b, pg, "bench-nuget", "nuget").repo
	ctx := context.Background()

	add := func(id, version string) {
		p := &models.NuGetPackage{
			RepositoryID: repo, PackageID: id, Version: version, Description: "a benchmark library", Authors: "au",
			Tags: "t1 t2", BlobKey: langSHA("bench" + id + version), PackageSize: 5, DependencyGroups: []byte(`[{"targetFramework":"net8.0"}]`),
		}
		if err := pg.CreateNuGetPackage(ctx, p); err != nil {
			b.Fatal(err)
		}
	}

	for i := range 500 {
		add(fmt.Sprintf("Pkg.%d", i), "1.0.0")
	}

	for i := range 300 {
		add("Big.Package", fmt.Sprintf("1.0.%d", i))
	}

	return pg, repo
}

// BenchmarkNuGetVersionsStamp is the cost of revalidating a 300-version
// package; BenchmarkNuGetListVersions is what the stamp replaces on a hit.
func BenchmarkNuGetVersionsStamp(b *testing.B) {
	pg, repo := seedNuGetBench(b)
	ctx := context.Background()

	for b.Loop() {
		if _, err := pg.NuGetVersionsStamp(ctx, repo, "Big.Package"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNuGetListVersions(b *testing.B) {
	pg, repo := seedNuGetBench(b)
	ctx := context.Background()

	for b.Loop() {
		if v, err := pg.ListNuGetPackageVersions(ctx, repo, "Big.Package"); err != nil || len(v) != 300 {
			b.Fatal(len(v), err)
		}
	}
}
