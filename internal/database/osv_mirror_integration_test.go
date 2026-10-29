// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func osvAdvisoryOf(id string, modified time.Time, affected ...models.OSVAffected) models.OSVAdvisory {
	return models.OSVAdvisory{ID: id, Modified: modified, Summary: id, Aliases: []string{"CVE-" + id}, Affected: affected}
}

func osvPkg(ecosystem, name string) models.OSVAffected {
	return models.OSVAffected{Ecosystem: ecosystem, Name: name, NameNorm: name}
}

func sortedKeys(keys []models.OSVPackageKey) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.Ecosystem+"/"+k.NameNorm)
	}

	slices.Sort(out)

	return out
}

func osvAffectedRows(t *testing.T, pg *PostgresDB, id string) []string {
	t.Helper()

	rows, err := pg.db.QueryContext(context.Background(),
		`SELECT ecosystem || '/' || name_norm FROM osv_affected WHERE advisory_id = $1 ORDER BY 1`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var out []string

	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}

		out = append(out, s)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return out
}

func osvPending(t *testing.T, pg *PostgresDB, ecosystem string) bool {
	t.Helper()

	st, err := pg.GetOSVMirrorState(context.Background(), ecosystem)
	if err != nil {
		t.Fatal(err)
	}

	return st != nil && st.RematchPending
}

// An advisory affects packages of several ecosystems, but only the ones that
// were configured when it was imported are stored. Adding an ecosystem later
// imports the same record again: with the same modified time it used to be
// skipped, so the new ecosystem never matched it.
func TestOSVApplyReappliesAdvisoryWhenAnEcosystemIsAdded(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	modified := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)

	npmOnly := osvAdvisoryOf("GHSA-multi", modified, osvPkg("npm", "left-pad"))
	both := osvAdvisoryOf("GHSA-multi", modified, osvPkg("npm", "left-pad"), osvPkg("PyPI", "left-pad-py"))

	if keys, err := pg.ApplyOSVAdvisories(ctx, []models.OSVAdvisory{npmOnly}); err != nil || len(keys) != 1 {
		t.Fatalf("first apply = %v, %v", keys, err)
	}

	if keys, err := pg.ApplyOSVAdvisories(ctx, []models.OSVAdvisory{npmOnly}); err != nil || len(keys) != 0 {
		t.Fatalf("an unchanged record must be skipped: %v, %v", keys, err)
	}

	keys, err := pg.ApplyOSVAdvisories(ctx, []models.OSVAdvisory{both})
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"PyPI/left-pad-py", "npm/left-pad"}; !slices.Equal(sortedKeys(keys), want) {
		t.Fatalf("touched = %v, want %v: the new ecosystem's packages must be re-matched", sortedKeys(keys), want)
	}

	if got := osvAffectedRows(t, pg, "GHSA-multi"); !slices.Equal(got, []string{"PyPI/left-pad-py", "npm/left-pad"}) {
		t.Fatalf("affected rows = %v, want both ecosystems", got)
	}

	if n, err := pg.CountOSVAdvisories(ctx, "PyPI"); err != nil || n != 1 {
		t.Fatalf("PyPI advisories = %d, %v; want 1", n, err)
	}

	if !osvPending(t, pg, "PyPI") {
		t.Fatal("the new ecosystem must be flagged for a re-match")
	}

	// Now complete: the same record again changes nothing, and an older copy is ignored.
	if keys, err := pg.ApplyOSVAdvisories(ctx, []models.OSVAdvisory{both}); err != nil || len(keys) != 0 {
		t.Fatalf("a complete record must be skipped: %v, %v", keys, err)
	}

	older := osvAdvisoryOf("GHSA-multi", modified.Add(-time.Hour), osvPkg("npm", "other"), osvPkg("Go", "example.com/other"))
	if keys, err := pg.ApplyOSVAdvisories(ctx, []models.OSVAdvisory{older}); err != nil || len(keys) != 0 {
		t.Fatalf("an older record must be skipped even when it adds an ecosystem: %v, %v", keys, err)
	}

	if got := osvAffectedRows(t, pg, "GHSA-multi"); !slices.Equal(got, []string{"PyPI/left-pad-py", "npm/left-pad"}) {
		t.Fatalf("affected rows = %v, the older copy must not replace the stored one", got)
	}
}

// An advisory that left an ecosystem's export is dropped from that ecosystem
// only; one with no affected package left is deleted, aliases with it. The
// packages that lost an advisory come back to be re-matched.
func TestOSVRemoveAdvisoriesDropsOneEcosystemAndOrphans(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	modified := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)

	if _, err := pg.ApplyOSVAdvisories(ctx, []models.OSVAdvisory{
		osvAdvisoryOf("GHSA-both", modified, osvPkg("npm", "a"), osvPkg("PyPI", "b")),
		osvAdvisoryOf("GHSA-npm", modified, osvPkg("npm", "c")),
		osvAdvisoryOf("GHSA-kept", modified, osvPkg("npm", "d")),
	}); err != nil {
		t.Fatal(err)
	}

	ids, err := pg.ListOSVAdvisoryIDs(ctx, "npm")
	if err != nil {
		t.Fatal(err)
	}

	slices.Sort(ids)

	if want := []string{"GHSA-both", "GHSA-kept", "GHSA-npm"}; !slices.Equal(ids, want) {
		t.Fatalf("npm advisory ids = %v, want %v", ids, want)
	}

	if ids, err := pg.ListOSVAdvisoryIDs(ctx, "PyPI"); err != nil || !slices.Equal(ids, []string{"GHSA-both"}) {
		t.Fatalf("PyPI advisory ids = %v, %v", ids, err)
	}

	if err := pg.FinishOSVRematch(ctx, "npm", time.Now(), time.Second); err != nil {
		t.Fatal(err)
	}

	if osvPending(t, pg, "npm") {
		t.Fatal("the re-match of the import must be done before the removal")
	}

	keys, err := pg.RemoveOSVAdvisories(ctx, "npm", []string{"GHSA-both", "GHSA-npm", "GHSA-unknown"})
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"npm/a", "npm/c"}; !slices.Equal(sortedKeys(keys), want) {
		t.Fatalf("touched = %v, want %v", sortedKeys(keys), want)
	}

	if got := osvAffectedRows(t, pg, "GHSA-both"); !slices.Equal(got, []string{"PyPI/b"}) {
		t.Fatalf("GHSA-both rows = %v, want the PyPI package only", got)
	}

	if got := osvAffectedRows(t, pg, "GHSA-npm"); len(got) != 0 {
		t.Fatalf("GHSA-npm rows = %v, want none", got)
	}

	if got := osvAffectedRows(t, pg, "GHSA-kept"); !slices.Equal(got, []string{"npm/d"}) {
		t.Fatalf("GHSA-kept rows = %v, want it untouched", got)
	}

	checkOSVRemovalAftermath(t, pg)
}

// checkOSVRemovalAftermath checks what RemoveOSVAdvisories left of the
// advisories of TestOSVRemoveAdvisoriesDropsOneEcosystemAndOrphans.
func checkOSVRemovalAftermath(t *testing.T, pg *PostgresDB) {
	t.Helper()

	ctx := context.Background()

	aliases, err := pg.OSVCVEAliases(ctx, []string{"GHSA-both", "GHSA-npm", "GHSA-kept"})
	if err != nil {
		t.Fatal(err)
	}

	if len(aliases) != 2 || aliases["GHSA-npm"] != nil {
		t.Fatalf("aliases = %v, want GHSA-npm deleted with its aliases and the other two kept", aliases)
	}

	if !osvPending(t, pg, "npm") {
		t.Fatal("the ecosystem must be flagged for a re-match")
	}

	if n, err := pg.CountOSVAdvisories(ctx, "npm"); err != nil || n != 1 {
		t.Fatalf("npm advisories = %d, %v; want 1", n, err)
	}

	// Nothing to remove: no keys, no error.
	if keys, err := pg.RemoveOSVAdvisories(ctx, "npm", []string{"GHSA-npm"}); err != nil || len(keys) != 0 {
		t.Fatalf("removing nothing = %v, %v", keys, err)
	}
}
