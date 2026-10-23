// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	sq "github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

// benchWorkerDB serves just the lookups the worker bodies benchmarked here make.
type benchWorkerDB struct {
	sq.BaseMockDB

	components    []models.DebianComponent
	architectures []models.DebianArchitecture
	artifacts     []*models.MavenArtifact
	refs          *models.MavenGARefs
	refsFor       int

	meta    map[string]*models.RepoMetadata
	upserts int
}

func (d *benchWorkerDB) GetDebianComponentsByDist(context.Context, string) ([]models.DebianComponent, error) {
	return d.components, nil
}

func (d *benchWorkerDB) GetDebianArchitecturesByDist(context.Context, string) ([]models.DebianArchitecture, error) {
	return d.architectures, nil
}

func (d *benchWorkerDB) ListMavenGARefs(context.Context, string, string, string) (*models.MavenGARefs, error) {
	// Derived once per artifact list so benchmarks measure the worker, not the mock.
	if d.refs == nil || d.refsFor != len(d.artifacts) {
		d.refs, d.refsFor = sq.MavenGARefsFromArtifacts(d.artifacts), len(d.artifacts)
	}

	return d.refs, nil
}

func (d *benchWorkerDB) GetRepoMetadataByPath(_ context.Context, _, path string) (*models.RepoMetadata, error) {
	return d.meta[path], nil
}

func (d *benchWorkerDB) UpsertRepoMetadata(_ context.Context, m *models.RepoMetadata) error {
	if d.meta == nil {
		d.meta = make(map[string]*models.RepoMetadata)
	}

	cp := *m
	d.meta[m.Path] = &cp
	d.upserts++

	return nil
}

func benchDebWorkerEntries(n int) []models.DebPackageEntry {
	entries := make([]models.DebPackageEntry, n)
	comps := []string{"main", "contrib"}
	archs := []string{"amd64", "arm64", "all"}

	for i := range entries {
		name := "libexample" + strconv.Itoa(i)

		entries[i] = models.DebPackageEntry{
			ID:            strconv.Itoa(i),
			Name:          name,
			Version:       "1.2." + strconv.Itoa(i%50) + "-1",
			Arch:          archs[i%len(archs)],
			Filename:      name + "_1.2.0-1_amd64.deb",
			Distribution:  "bookworm",
			ComponentName: comps[i%len(comps)],
			SHA256:        fmt.Sprintf("%064x", i),
			Size:          123456,
			InstalledSize: 1234,
			ControlFields: map[string]string{
				"Maintainer":  "Jane <jane@example.org>",
				"Depends":     "libc6 (>= 2.34), zlib1g",
				"Section":     "libs",
				"Priority":    "optional",
				"Description": "example " + name + "\n long description",
			},
		}
	}

	return entries
}

// BenchmarkDebGenerateDistribution renders every Packages/.gz/.xz index plus
// Release for one distribution (2 components x 2 real architectures + all),
// the pure part of DebMetadataWorker.regenerate.
func BenchmarkDebGenerateDistribution(b *testing.B) {
	db := &benchWorkerDB{
		components:    []models.DebianComponent{{Name: "main"}, {Name: "contrib"}},
		architectures: []models.DebianArchitecture{{Name: "amd64"}, {Name: "arm64"}, {Name: "all"}},
	}
	w := &DebMetadataWorker{db: db}
	dist := models.DebianDistribution{ID: "dist-1", Name: "bookworm"}

	for _, n := range []int{100, 1000, 10000} {
		entries := benchDebWorkerEntries(n)

		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				blobs := make(map[string][]byte)
				if err := w.generateDistribution(context.Background(), dist, entries, nil, blobs); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchRPMWorkerPackages(n int) []pkgrpm.Package {
	pkgs := make([]pkgrpm.Package, n)

	for i := range pkgs {
		name := "libexample" + strconv.Itoa(i)
		ver := "1." + strconv.Itoa(i%50) + ".0"

		p := pkgrpm.Package{
			Name: name, Version: ver, Release: "3.el9", Arch: "x86_64",
			Summary:     "Example library " + strconv.Itoa(i),
			Description: "Example library providing a fairly long description.",
			License:     "MIT", URL: "https://example.org/" + name,
			BuildTime: 1_700_000_000, PackageSize: 123456, InstalledSize: 654321, ArchiveSize: 234567,
			SHA256: fmt.Sprintf("%064x", i), HeaderStart: 280, HeaderEnd: 9000,
		}

		p.Provides = []pkgrpm.Dependency{
			{Name: name, Flags: pkgrpm.SENSE_EQUAL, Version: ver + "-3.el9"},
			{Name: name + "(x86-64)", Flags: pkgrpm.SENSE_EQUAL, Version: ver + "-3.el9"},
		}
		p.Requires = []pkgrpm.Dependency{
			{Name: "rpmlib(CompressedFileNames)", Flags: pkgrpm.SENSE_LESSEQUAL | pkgrpm.SENSE_RPMLIB, Version: "3.0.4-1"},
			{Name: "libc.so.6()(64bit)"},
			{Name: "libc.so.6(GLIBC_2.2.5)(64bit)"},
			{Name: "libc.so.6(GLIBC_2.34)(64bit)"},
			{Name: "openssl-libs", Flags: pkgrpm.SENSE_GREATEREQUAL, Version: "3.0.0-1"},
		}

		for j := range 30 {
			p.Files = append(p.Files, pkgrpm.FileEntry{Path: fmt.Sprintf("/usr/lib64/%s/module%d.so", name, j)})
		}

		for j := range 3 {
			p.Changelogs = append(p.Changelogs, pkgrpm.ChangelogEntry{
				Time: 1_690_000_000 - int64(j)*86400, Author: "Packager <p@example.org>", Description: "- update",
			})
		}

		pkgs[i] = p
	}

	return pkgs
}

// BenchmarkRPMBuildMetadata is the pure core of RPMetadataWorker.Regenerate:
// primary/filelists/other XML, gzip+xz compression, repomd.xml (unsigned).
func BenchmarkRPMBuildMetadata(b *testing.B) {
	w := &RPMetadataWorker{db: &benchWorkerDB{}}

	for _, n := range []int{100, 1000} {
		pkgs := benchRPMWorkerPackages(n)

		b.Run(fmt.Sprintf("packages=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				if _, err := w.buildMetadata(context.Background(), "repo-1", pkgs); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchMavenArtifacts(versions int) []*models.MavenArtifact {
	arts := make([]*models.MavenArtifact, 0, versions*2)

	for i := range versions {
		v := fmt.Sprintf("%d.%d.%d", i/100, (i/10)%10, i%10)
		if i%7 == 0 {
			v += "-SNAPSHOT"
		}

		snapshot := i%7 == 0

		for _, ext := range []string{"jar", "pom"} {
			a := &models.MavenArtifact{Version: v, Extension: ext, IsSnapshot: snapshot}
			if snapshot {
				a.SnapshotTimestamp = "20260102.030405"
				a.SnapshotBuildNumber = i%5 + 1
			}

			arts = append(arts, a)
		}
	}

	return arts
}

// BenchmarkMavenRegenerate runs MavenMetadataWorker.regenerate (version
// collection + Maven-order sort + group and snapshot metadata rendering +
// checksums) against an in-memory database and blob store.
func BenchmarkMavenRegenerate(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		db := &benchWorkerDB{artifacts: benchMavenArtifacts(n)}
		w := &MavenMetadataWorker{db: db, blobStore: formattest.NewBlobStore()}

		b.Run(fmt.Sprintf("versions=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				if err := w.regenerate(context.Background(), "repo-1", "com.example", "app"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchWorkerNPMVersions(n int) []models.NPMVersionState {
	versions := make([]models.NPMVersionState, n)

	for i := range versions {
		versions[i] = models.NPMVersionState{
			Version:    fmt.Sprintf("%d.%d.%d", i/10000, (i/100)%100, i%100),
			Deprecated: i%7 == 0,
		}
	}

	return versions
}

func BenchmarkComputeLatestVersion(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		versions := benchWorkerNPMVersions(n)

		b.Run(fmt.Sprintf("versions=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				_ = computeLatestVersion(versions)
			}
		})
	}
}

func BenchmarkCompareSemver(b *testing.B) {
	b.ReportAllocs()

	for b.Loop() {
		_ = compareSemver("12.4.17-beta.1+build.7", "12.4.9")
	}
}
