// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// stampMoves runs mutate and requires the stamp to differ afterwards.
func stampMoves(t *testing.T, what string, stamp func() string, mutate func()) {
	t.Helper()

	before := stamp()

	mutate()

	if after := stamp(); after == before {
		t.Errorf("%s: stamp stayed %q", what, before)
	}
}

// A stamp is the validator of a rendered metadata document, so it has to move
// whenever the document would change (rows added, removed, yanked) and stay
// put when only download counters move.
//
//nolint:gocyclo,cyclop // one subtest per format
func TestMetadataStamps(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()

	t.Run("helm", func(t *testing.T) {
		s := newQuotaSeeder(t, pg, "stamp-helm", "helm")
		stamp := func() string {
			v, err := pg.HelmIndexStamp(ctx, s.repo)
			if err != nil {
				t.Fatal(err)
			}

			return v
		}

		empty := stamp()
		add := func(version string) {
			c := &models.HelmChart{
				RepositoryID: s.repo, Name: "web", Version: version, Digest: fmt.Sprintf("%064x", len(version)*100+int(version[2])),
				BlobKey: "helm/" + version, SizeBytes: 5, APIVersion: "v2", Type: "application",
			}
			if err := pg.CreateHelmChart(ctx, c); err != nil {
				t.Fatal(err)
			}
		}

		stampMoves(t, "first chart", stamp, func() { add("1.0.0") })

		one := stamp()
		stampMoves(t, "second chart", stamp, func() { add("1.1.0") })
		stampMoves(t, "trash a chart", stamp, func() {
			s.exec(`UPDATE package_files SET deleted_at = now() WHERE repository_id = $1 AND filename = 'web-1.1.0.tgz'`, s.repo)
		})

		if got := stamp(); got != one {
			t.Errorf("stamp after trash = %q, want the single-chart stamp %q", got, one)
		}

		if empty == one {
			t.Error("empty and non-empty repositories share a stamp")
		}
	})

	t.Run("nuget", func(t *testing.T) {
		s := newQuotaSeeder(t, pg, "stamp-nuget", "nuget")
		stamp := func(id string) func() string {
			return func() string {
				v, err := pg.NuGetVersionsStamp(ctx, s.repo, id)
				if err != nil {
					t.Fatal(err)
				}

				return v
			}
		}
		add := func(id, version string) {
			p := &models.NuGetPackage{
				RepositoryID: s.repo, PackageID: id, Version: version, BlobKey: langSHA("nuget" + id + version),
				PackageSize: 5, DependencyGroups: []byte("[]"),
			}
			if err := pg.CreateNuGetPackage(ctx, p); err != nil {
				t.Fatal(err)
			}
		}

		demo := stamp("Demo")
		other := stamp("other")()

		stampMoves(t, "first version", demo, func() { add("demo", "1.0.0") })

		one := demo()
		stampMoves(t, "second version", demo, func() { add("demo", "1.1.0") })

		if got := stamp("other")(); got != other {
			t.Errorf("another package's stamp moved: %q -> %q", other, got)
		}

		if err := pg.IncrementNuGetDownloadCount(ctx, s.repo, "demo", "1.1.0"); err != nil {
			t.Fatal(err)
		}

		two := demo()

		stampMoves(t, "delete a version", demo, func() {
			if err := pg.DeleteNuGetPackage(ctx, s.repo, "demo", "1.1.0"); err != nil {
				t.Fatal(err)
			}
		})

		if got := demo(); got != one {
			t.Errorf("stamp after delete = %q, want the single-version stamp %q", got, one)
		}

		if one == two {
			t.Error("one and two versions share a stamp")
		}

		if empty := stamp("missing")(); !strings.HasPrefix(empty, "0:") {
			t.Errorf("stamp of an unknown package = %q, want a zero count", empty)
		}
	})

	t.Run("conda", func(t *testing.T) {
		s := newQuotaSeeder(t, pg, "stamp-conda", "conda")
		stamp := func(platform string) func() string {
			return func() string {
				v, err := pg.CondaRepodataStamp(ctx, s.repo, platform)
				if err != nil {
					t.Fatal(err)
				}

				return v
			}
		}

		other := stamp("noarch")()

		stampMoves(t, "package on platform", stamp("linux-64"), func() {
			addCondaBuild(t, pg, s.repo, "numpy", "1.0", "h1", "linux-64", models.CondaExtConda)
		})

		if got := stamp("noarch")(); got != other {
			t.Errorf("another platform's stamp moved: %q -> %q", other, got)
		}
	})

	t.Run("rubygems", func(t *testing.T) {
		s := newQuotaSeeder(t, pg, "stamp-gems", "rubygems")
		stamp := func() string {
			v, err := pg.RubyGemsSpecsStamp(ctx, s.repo)
			if err != nil {
				t.Fatal(err)
			}

			return v
		}

		stampMoves(t, "publish", stamp, func() {
			gem := &models.RubyGem{
				RepositoryID: s.repo, Name: "rake", Version: "1.0", Platform: "ruby",
				ChecksumSHA256: langSHA("stamp-gem"), BlobKey: langSHA("stamp-gem"), Size: 5,
			}
			if err := pg.CreateRubyGem(ctx, gem); err != nil {
				t.Fatal(err)
			}
		})
		stampMoves(t, "yank", stamp, func() {
			if err := pg.YankRubyGem(ctx, s.repo, "rake", "1.0", "", true); err != nil {
				t.Fatal(err)
			}
		})

		specs, err := pg.ListRubyGemSpecs(ctx, s.repo)
		if err != nil || len(specs) != 0 {
			t.Errorf("ListRubyGemSpecs after yank = %v, %v; want none", specs, err)
		}
	})

	t.Run("pypi", func(t *testing.T) {
		s := newQuotaSeeder(t, pg, "stamp-pypi", "pypi")

		index := func() string {
			v, err := pg.PyPIIndexStamp(ctx, s.repo)
			if err != nil {
				t.Fatal(err)
			}

			return v
		}
		project := func(name string) (string, int64) {
			v, n, err := pg.PyPIProjectStamp(ctx, s.repo, name)
			if err != nil {
				t.Fatal(err)
			}

			return v, n
		}
		page := func() string { v, _ := project("demo"); return v }
		add := func(name, version, filename string) *models.PyPIPackage {
			p := &models.PyPIPackage{
				RepositoryID: s.repo, Name: name, Version: version, Filename: filename, PackageType: "sdist",
				BlobKey: langSHA("pypi-" + filename), SHA256Digest: langSHA("pypi-" + filename), SizeBytes: 5, Summary: "first",
			}
			if err := pg.CreatePyPIPackage(ctx, p); err != nil {
				t.Fatal(err)
			}

			return p
		}

		if _, n := project("demo"); n != 0 {
			t.Fatalf("files of an unknown project = %d, want 0", n)
		}

		stampMoves(t, "index: first release of a project", index, func() { add("demo", "1.0", "demo-1.0.tar.gz") })
		stampMoves(t, "index: a second project", index, func() { add("other", "1.0", "other-1.0.tar.gz") })

		if _, n := project("demo"); n != 1 {
			t.Fatalf("files of demo = %d, want 1", n)
		}

		// A new release of a known project changes its page, not the project list.
		listed := index()

		var second *models.PyPIPackage

		stampMoves(t, "page: a further release", page, func() { second = add("demo", "2.0", "demo-2.0.tar.gz") })

		if got := index(); got != listed {
			t.Errorf("a new release of a known project moved the index stamp: %q -> %q", listed, got)
		}

		if _, n := project("demo"); n != 2 {
			t.Fatalf("files of demo = %d, want 2", n)
		}

		// Another project's files are not part of this page's stamp.
		stable := page()

		add("other", "2.0", "other-2.0.tar.gz")

		if got := page(); got != stable {
			t.Errorf("a file of another project moved the page stamp: %q -> %q", stable, got)
		}

		// Counting a download is not a change of any document.
		if err := pg.q.IncrementFileDownloads(ctx, second.ID); err != nil {
			t.Fatal(err)
		}

		if got := page(); got != stable {
			t.Errorf("a download moved the page stamp: %q -> %q", stable, got)
		}

		stampMoves(t, "page: release metadata edit", page, func() {
			s.exec(`UPDATE package_versions SET metadata = metadata || '{"summary":"edited"}'::jsonb, updated_at = now()
				WHERE package_id = (SELECT id FROM packages WHERE repository_id = $1 AND name_key = 'demo') AND version = '2.0'`, s.repo)
		})

		// Trashing a file moves the page stamp; trashing the last file of every
		// release removes the project from the list.
		stampMoves(t, "page: trash a file", page, func() {
			s.exec(`UPDATE package_files SET deleted_at = now() WHERE repository_id = $1 AND filename = 'demo-2.0.tar.gz'`, s.repo)
		})

		if _, n := project("demo"); n != 1 {
			t.Fatalf("files of demo after trash = %d, want 1", n)
		}

		before := index()

		stampMoves(t, "index: a project loses its last live file", index, func() {
			s.exec(`UPDATE package_files SET deleted_at = now() WHERE repository_id = $1 AND filename = 'demo-1.0.tar.gz'`, s.repo)
			s.exec(`UPDATE package_versions SET deleted_at = now() WHERE package_id = (SELECT id FROM packages WHERE repository_id = $1 AND name_key = 'demo')`, s.repo)
		})

		if got := index(); got == before {
			t.Error("index stamp unchanged")
		}

		if _, n := project("demo"); n != 0 {
			t.Fatalf("files of demo after the last one is trashed = %d, want 0", n)
		}
	})

	t.Run("cargo", func(t *testing.T) {
		s := newQuotaSeeder(t, pg, "stamp-cargo", "cargo")
		stamp := func() string {
			v, err := pg.CargoIndexStamp(ctx, s.repo, "Serde")
			if err != nil {
				t.Fatal(err)
			}

			return v
		}

		if got := stamp(); got != "0:0" {
			t.Fatalf("stamp of an unknown crate = %q, want 0:0", got)
		}

		stampMoves(t, "publish (name matches case-insensitively)", stamp, func() {
			crate := &models.CargoCrate{
				RepositoryID: s.repo, Name: "serde", Version: "1.0.0", Checksum: langSHA("stamp-crate"),
				BlobKey: langSHA("stamp-crate"), SizeBytes: 5, MetadataJSON: []byte(`{}`),
			}
			if err := pg.CreateCargoCrate(ctx, crate); err != nil {
				t.Fatal(err)
			}
		})

		afterOne := stamp()

		crate, err := pg.GetCargoCrate(ctx, s.repo, "serde", "1.0.0")
		if err != nil || crate == nil {
			t.Fatalf("GetCargoCrate = %v, %v", crate, err)
		}

		pg.CountFileDownload(crate.ID)

		if err := pg.FlushDownloadCounts(ctx); err != nil {
			t.Fatal(err)
		}

		if got := stamp(); got != afterOne {
			t.Errorf("a download moved the stamp: %q -> %q", afterOne, got)
		}

		stampMoves(t, "yank", stamp, func() {
			if err := pg.YankCargoCrate(ctx, s.repo, "SERDE", "1.0.0", true); err != nil {
				t.Fatal(err)
			}
		})

		versions, err := pg.ListCargoIndexVersions(ctx, s.repo, "SERDE")
		if err != nil || len(versions) != 1 || !versions[0].Yanked || versions[0].Version != "1.0.0" {
			t.Errorf("ListCargoIndexVersions = %+v, %v", versions, err)
		}
	})
}

// Dependency groups are persisted at publish and backfilled once for rows that
// predate the column; a recorded value is never overwritten.
func TestNuGetDependencyGroups(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "nuget-depgroups", "nuget")

	create := func(version string, groups []byte) {
		t.Helper()

		pkg := &models.NuGetPackage{
			RepositoryID: s.repo, PackageID: "Demo", Version: version,
			BlobKey: fmt.Sprintf("%064x", len(version)*1000+int(version[0])), PackageSize: 5, DependencyGroups: groups,
		}
		if err := pg.CreateNuGetPackage(ctx, pkg); err != nil {
			t.Fatalf("CreateNuGetPackage(%s): %v", version, err)
		}
	}

	groups := func(version string) string {
		t.Helper()

		pkg, err := pg.GetNuGetPackage(ctx, s.repo, "Demo", version)
		if err != nil || pkg == nil {
			t.Fatalf("GetNuGetPackage(%s) = %v, %v", version, pkg, err)
		}

		if pkg.DependencyGroups == nil {
			return "<unrecorded>"
		}

		return string(pkg.DependencyGroups)
	}

	const recorded = `[{"targetFramework":"net8.0","dependencies":[]}]`

	create("1.0.0", []byte(recorded))
	create("2.0.0", nil)

	if got := groups("1.0.0"); got == "<unrecorded>" || got == "" {
		t.Fatalf("groups stored at publish = %q", got)
	}

	if got := groups("2.0.0"); got != "<unrecorded>" {
		t.Fatalf("a package published without groups reads %q, want unrecorded", got)
	}

	if err := pg.SetNuGetDependencyGroups(ctx, s.repo, "Demo", "2.0.0", []byte(`[]`)); err != nil {
		t.Fatal(err)
	}

	if got := groups("2.0.0"); got != "[]" {
		t.Errorf("backfilled groups = %q, want []", got)
	}

	// A second backfill must not replace the recorded value.
	if err := pg.SetNuGetDependencyGroups(ctx, s.repo, "Demo", "2.0.0", []byte(recorded)); err != nil {
		t.Fatal(err)
	}

	if got := groups("2.0.0"); got != "[]" {
		t.Errorf("backfill overwrote the recorded groups: %q", got)
	}

	// Backfilling leaves updated_at (a registration validator input) alone.
	before, _ := pg.GetNuGetPackage(ctx, s.repo, "Demo", "1.0.0")
	if err := pg.SetNuGetDependencyGroups(ctx, s.repo, "Demo", "1.0.0", []byte(`[]`)); err != nil {
		t.Fatal(err)
	}

	after, _ := pg.GetNuGetPackage(ctx, s.repo, "Demo", "1.0.0")
	if !before.UpdatedAt.Equal(after.UpdatedAt) || groups("1.0.0") == "[]" {
		t.Error("backfill touched a recorded row")
	}
}

// A group's hosted-wins rule asks which names a hosted repository holds, in
// any platform, compared case-insensitively and without trashed packages.
func TestCondaPackageNames(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()

	s := newQuotaSeeder(t, pg, "names-conda", "conda")
	add := func(name, platform string) {
		addCondaBuild(t, pg, s.repo, name, "1.0", "h1", platform, models.CondaExtConda)
	}

	add("Demo", "linux-64")
	add("demo", "noarch")
	add("other", "noarch")

	names, err := pg.ListCondaPackageNames(ctx, s.repo)
	if err != nil || len(names) != 2 || names[0] != "demo" || names[1] != "other" {
		t.Fatalf("names = %v, %v", names, err)
	}

	for name, want := range map[string]bool{"demo": true, "DEMO": true, "other": true, "absent": false} {
		if got, err := pg.HasCondaPackageName(ctx, s.repo, name); err != nil || got != want {
			t.Errorf("HasCondaPackageName(%q) = %v, %v; want %v", name, got, err, want)
		}
	}

	trashCondaPackage(t, s, "other")

	if got, err := pg.HasCondaPackageName(ctx, s.repo, "other"); err != nil || got {
		t.Errorf("a trashed package still counts: %v, %v", got, err)
	}
}
