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
			s.exec(`INSERT INTO helm_charts (repository_id, name, version, blob_id) VALUES ($1,'web',$2,$3)`, s.repo, version, s.blob(5))
		}

		stampMoves(t, "first chart", stamp, func() { add("1.0.0") })

		one := stamp()
		stampMoves(t, "second chart", stamp, func() { add("1.1.0") })
		stampMoves(t, "trash a chart", stamp, func() {
			s.exec(`UPDATE helm_charts SET deleted_at = now() WHERE repository_id = $1 AND version = '1.1.0'`, s.repo)
		})

		if got := stamp(); got != one {
			t.Errorf("stamp after trash = %q, want the single-chart stamp %q", got, one)
		}

		if empty == one {
			t.Error("empty and non-empty repositories share a stamp")
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
			s.exec(`INSERT INTO conda_packages (repository_id, name, version, build_string, platform, depends, blob_id, extension, build_number, md5, timestamp_ms, index_extra)
			        VALUES ($1,'numpy','1.0','h1','linux-64','{}',$2,'.conda',0,'m',1,'{}')`, s.repo, s.blob(5))
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
			s.exec(`INSERT INTO rubygems_gems (repository_id, name, version, platform, blob_id, size) VALUES ($1,'rake','1.0','ruby',$2,5)`, s.repo, s.blob(5))
		})
		stampMoves(t, "yank", stamp, func() {
			s.exec(`UPDATE rubygems_gems SET yanked = true WHERE repository_id = $1`, s.repo)
		})

		specs, err := pg.ListRubyGemSpecs(ctx, s.repo)
		if err != nil || len(specs) != 0 {
			t.Errorf("ListRubyGemSpecs after yank = %v, %v; want none", specs, err)
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

		insert := func(version string) {
			s.exec(`INSERT INTO cargo_crates (repository_id, name, version, metadata_json, blob_id) VALUES ($1,'serde',$2,'{}',$3)`, s.repo, version, s.blob(5))
		}

		stampMoves(t, "publish (name matches case-insensitively)", stamp, func() { insert("1.0.0") })

		afterOne := stamp()

		s.exec(`UPDATE cargo_crates SET download_count = download_count + 1, updated_at = now() WHERE repository_id = $1`, s.repo)

		if got := stamp(); got != afterOne {
			t.Errorf("a download moved the stamp: %q -> %q", afterOne, got)
		}

		stampMoves(t, "yank", stamp, func() {
			s.exec(`UPDATE cargo_crates SET yanked = true WHERE repository_id = $1`, s.repo)
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
