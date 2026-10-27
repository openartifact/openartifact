// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

func seedCargoCrate(t *testing.T, pg *database.PostgresDB, repoID, name, version, description string, yanked bool, downloads int) {
	t.Helper()

	sha := fmt.Sprintf("%064x", len(name)*1000+len(version)*10+downloads)
	crate := &models.CargoCrate{
		RepositoryID: repoID, Name: name, Version: version, Description: description,
		MetadataJSON: []byte(`{}`), BlobKey: sha, Checksum: sha, Yanked: yanked, DownloadCount: downloads,
	}

	if err := pg.CreateCargoCrate(context.Background(), crate); err != nil {
		t.Fatalf("CreateCargoCrate(%s@%s): %v", name, version, err)
	}
}

// Cargo lowercases crate names in index paths and download URLs, so a crate
// published as "Demo-Crate" must answer to "demo-crate" in every lookup.
func TestCargoCrateLookupsAreCaseInsensitive(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repoID := newRepo(t, pg, "crates", "cargo")

	seedCargoCrate(t, pg, repoID, "Demo-Crate", "1.0.0", "", false, 0)
	seedCargoCrate(t, pg, repoID, "Demo-Crate", "1.1.0", "", false, 0)
	seedCargoCrate(t, pg, repoID, "other", "1.0.0", "", false, 0)

	got, err := pg.GetCargoCrate(ctx, repoID, "demo-crate", "1.0.0")
	if err != nil || got == nil || got.Name != "Demo-Crate" {
		t.Fatalf("GetCargoCrate(lower-case) = %+v, %v; want the Demo-Crate row", got, err)
	}

	if missing, err := pg.GetCargoCrate(ctx, repoID, "demo-crate", "9.9.9"); err != nil || missing != nil {
		t.Fatalf("GetCargoCrate(unknown version) = %+v, %v; want nil", missing, err)
	}

	versions, err := pg.ListCargoCrateVersions(ctx, repoID, "DEMO-CRATE")
	if err != nil || len(versions) != 2 {
		t.Fatalf("ListCargoCrateVersions(upper-case) = %d rows, %v; want 2", len(versions), err)
	}

	if versions[0].Version != "1.0.0" || versions[1].Version != "1.1.0" {
		t.Errorf("versions = %s, %s; want publish order 1.0.0, 1.1.0", versions[0].Version, versions[1].Version)
	}

	if err := pg.YankCargoCrate(ctx, repoID, "demo-crate", "1.0.0", true); err != nil {
		t.Fatalf("YankCargoCrate: %v", err)
	}

	if err := pg.IncrementCargoDownloadCount(ctx, repoID, "demo-crate", "1.0.0"); err != nil {
		t.Fatalf("IncrementCargoDownloadCount: %v", err)
	}

	got, _ = pg.GetCargoCrate(ctx, repoID, "Demo-Crate", "1.0.0")
	if got == nil || !got.Yanked || got.DownloadCount != 1 {
		t.Errorf("after yank + download via lower-case name: %+v; want yanked with 1 download", got)
	}

	// Another repository's crate of the same name is never matched.
	otherRepo := newRepo(t, pg, "crates2", "cargo")
	if got, _ := pg.GetCargoCrate(ctx, otherRepo, "demo-crate", "1.0.0"); got != nil {
		t.Errorf("lookup leaked across repositories: %+v", got)
	}
}

// seedSearchFixture stores three crates behind six versions.
func seedSearchFixture(t *testing.T) (*database.PostgresDB, string) {
	t.Helper()

	pg := freshPostgres(t, 5)
	repoID := newRepo(t, pg, "crates", "cargo")

	seedCargoCrate(t, pg, repoID, "alpha", "0.9.0", "old words", false, 5)
	seedCargoCrate(t, pg, repoID, "alpha", "0.10.0", "new words", false, 7)
	seedCargoCrate(t, pg, repoID, "alpha", "1.0.0-rc.1", "new words", true, 1)
	seedCargoCrate(t, pg, repoID, "beta", "1.0.0", "other", false, 2)
	seedCargoCrate(t, pg, repoID, "gamma", "2.0.0", "other", true, 3)
	seedCargoCrate(t, pg, repoID, "gamma", "2.1.0", "other", true, 4)

	return pg, repoID
}

// Search returns one row per crate with the total number of matching crates,
// whatever the number of versions behind them.
func TestSearchCargoCrateSummariesGroupsByCrate(t *testing.T) {
	pg, repoID := seedSearchFixture(t)
	ctx := context.Background()

	all, total, err := pg.SearchCargoCrateSummaries(ctx, repoID, "", 100)
	if err != nil {
		t.Fatalf("SearchCargoCrateSummaries: %v", err)
	}

	if total != 3 || len(all) != 3 {
		t.Fatalf("got %d crates, total %d; want 3 crates (one per name) and total 3", len(all), total)
	}

	alpha := all[0]
	if alpha.Name != "alpha" || alpha.Downloads != 13 || alpha.Description != "new words" {
		t.Errorf("alpha = %+v; want 13 summed downloads and the newest description", alpha)
	}

	if !slices.Equal(alpha.Versions, []string{"0.9.0", "0.10.0", "1.0.0-rc.1"}) ||
		!slices.Equal(alpha.LiveVersions, []string{"0.9.0", "0.10.0"}) {
		t.Errorf("alpha versions = %v, live = %v; want all three and the two not yanked, oldest first", alpha.Versions, alpha.LiveVersions)
	}

	if gamma := all[2]; len(gamma.LiveVersions) != 0 || len(gamma.Versions) != 2 {
		t.Errorf("gamma = %+v; want every version yanked", gamma)
	}

	// The page is limited, the total is not.
	page, total, err := pg.SearchCargoCrateSummaries(ctx, repoID, "", 2)
	if err != nil || len(page) != 2 || total != 3 {
		t.Fatalf("limited search = %d crates, total %d, err %v; want 2 crates, total 3", len(page), total, err)
	}
}

// Matching is by name or description, literally and case-insensitively; the
// per-version listing that backups use is unaffected.
func TestSearchCargoCrateSummariesMatching(t *testing.T) {
	pg, repoID := seedSearchFixture(t)
	ctx := context.Background()

	found, total, err := pg.SearchCargoCrateSummaries(ctx, repoID, "WORDS", 100)
	if err != nil || len(found) != 1 || total != 1 || found[0].Name != "alpha" {
		t.Fatalf("search by description = %+v, total %d, err %v; want only alpha", found, total, err)
	}

	if none, total, err := pg.SearchCargoCrateSummaries(ctx, repoID, "%", 100); err != nil || len(none) != 0 || total != 0 {
		t.Fatalf("a literal %% matched %d crates (total %d, err %v)", len(none), total, err)
	}

	// Per-version listing (backup export) keeps one row per version.
	rows, err := pg.SearchCargoCrates(ctx, repoID, "", 100)
	if err != nil || len(rows) != 6 {
		t.Fatalf("SearchCargoCrates = %d rows, %v; want 6 (one per version)", len(rows), err)
	}
}
