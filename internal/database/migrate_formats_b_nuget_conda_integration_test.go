// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"
)

// nugetRow inserts a nuget_packages row of the pre-019 schema.
func nugetRow(t *testing.T, db *sql.DB, repo, blob, id, version, createdAt string, trashed bool) string {
	t.Helper()

	deleted := "NULL"
	if trashed {
		deleted = "now()"
	}

	return mustScan[string](t, db, fmt.Sprintf(
		`INSERT INTO nuget_packages (repository_id, package_id, version, blob_id, created_at, deleted_at)
		 VALUES ($1, $2, $3, $4, $5::timestamptz, %s) RETURNING id`, deleted), repo, id, version, blob, createdAt)
}

func nugetVersions(t *testing.T, db *sql.DB, repo, id string, trashed bool) []string {
	t.Helper()

	rows, err := db.QueryContext(context.Background(),
		`SELECT version FROM nuget_packages WHERE repository_id = $1 AND package_id = $2 AND (deleted_at IS NOT NULL) = $3 ORDER BY created_at, id`,
		repo, id, trashed)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = rows.Close() }()

	var out []string

	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}

		out = append(out, v)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return out
}

// Migration 019 rewrites stored NuGet versions to the normalized form clients
// request (NU-3) without ever violating the live-row unique index: a row whose
// normalized form is taken by another live row keeps its version, and only one
// of several live rows with the same normalized form is rewritten.
func TestMigrationFormatsBNuGetVersionBackfill(t *testing.T) {
	db := scratchDB(t)
	migrateThrough(t, db, "018")

	const systemOrg = "00000000-0000-0000-0000-000000000001"

	blob := mustScan[string](t, db, `INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES (repeat('a', 64), 3, repeat('a', 64)) RETURNING id`)
	repoA := mustScan[string](t, db, `INSERT INTO repositories (name, format, org_id) VALUES ('nuget-a', 'nuget', $1) RETURNING id`, systemOrg)
	repoB := mustScan[string](t, db, `INSERT INTO repositories (name, format, org_id) VALUES ('nuget-b', 'nuget', $1) RETURNING id`, systemOrg)

	ts := func(minute int) string { return fmt.Sprintf("2025-01-01 00:%02d:00+00", minute) }

	// foo: every spelling that normalizes without conflict; "garbage" is not a
	// version and "5.0.0" is already normalized.
	for i, v := range []string{"1.0.0.0", "2.0", "01.2.3", "1.0.0-Beta", "3.0.0+meta", "garbage", "5.0.0", "4.7.0.1-RC.1"} {
		nugetRow(t, db, repoA, blob, "foo", v, ts(i), false)
	}

	// bar: "1.0" collides with the live "1.0.0" and stays; the trashed
	// "1.0.0.0" is outside the live index and is rewritten; of the live "2.0"
	// and "2.0.0.0" the older one wins "2.0.0".
	nugetRow(t, db, repoA, blob, "bar", "1.0.0", ts(0), false)
	nugetRow(t, db, repoA, blob, "bar", "1.0", ts(1), false)
	nugetRow(t, db, repoA, blob, "bar", "1.0.0.0", ts(2), true)
	nugetRow(t, db, repoA, blob, "bar", "2.0", ts(3), false)
	nugetRow(t, db, repoA, blob, "bar", "2.0.0.0", ts(4), false)

	// The same id in another repository is independent of repoA's rows.
	nugetRow(t, db, repoB, blob, "foo", "1.0", ts(0), false)
	nugetRow(t, db, repoB, blob, "foo", "1.0.0", ts(1), true)

	migrateAll(t, db)

	for _, c := range []struct {
		repo, id string
		trashed  bool
		want     []string
	}{
		{repoA, "foo", false, []string{"1.0.0", "2.0.0", "1.2.3", "1.0.0-beta", "3.0.0", "garbage", "5.0.0", "4.7.0.1-rc.1"}},
		{repoA, "bar", false, []string{"1.0.0", "1.0", "2.0.0", "2.0.0.0"}},
		{repoA, "bar", true, []string{"1.0.0"}},
		{repoB, "foo", false, []string{"1.0.0"}},
		{repoB, "foo", true, []string{"1.0.0"}},
	} {
		if got := nugetVersions(t, db, c.repo, c.id, c.trashed); !slices.Equal(got, c.want) {
			t.Errorf("%s trashed=%v versions = %v, want %v", c.id, c.trashed, got, c.want)
		}
	}

	if n := mustScan[int](t, db, `SELECT count(*) FROM pg_index WHERE NOT indisvalid`); n != 0 {
		t.Errorf("%d invalid indexes left behind", n)
	}

	// The live-row unique index still guards the normalized key.
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO nuget_packages (repository_id, package_id, version, blob_id) VALUES ($1, 'foo', '2.0.0', $2)`, repoA, blob)
	wantPgCode(t, err, pgUniqueViolation, "duplicate of a rewritten live row")
}

// Migration 019 adds the conda file columns with defaults for existing rows and
// swaps the live-row unique index for one that includes the extension (CONDA-2).
func TestMigrationFormatsBCondaExtension(t *testing.T) {
	db := scratchDB(t)
	migrateThrough(t, db, "018")

	const systemOrg = "00000000-0000-0000-0000-000000000001"

	blob := mustScan[string](t, db, `INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES (repeat('b', 64), 3, repeat('b', 64)) RETURNING id`)
	repo := mustScan[string](t, db, `INSERT INTO repositories (name, format, org_id) VALUES ('conda', 'conda', $1) RETURNING id`, systemOrg)
	pre := mustScan[string](t, db,
		`INSERT INTO conda_packages (repository_id, name, version, build_string, platform, blob_id)
		 VALUES ($1, 'scikit-learn', '1.0', 'py39_0', 'linux-64', $2) RETURNING id`, repo, blob)

	// Rows of hyphenated names as the old first-hyphen parser stored them:
	// python-dateutil-2.9.0-pyh_0 -> python / dateutil / 2.9.0-pyh_0, a
	// hyphen-free name is stored right, and a trashed row is re-split too.
	legacy := map[string]string{}

	for file, parts := range map[string][3]string{
		"python-dateutil-2.9.0-pyh_0":  {"python", "dateutil", "2.9.0-pyh_0"},
		"a-b-c-d-1.0-0":                {"a", "b", "c-d-1.0-0"},
		"ca-certificates-2024.2.2-h_0": {"ca", "certificates", "2024.2.2-h_0"},
		"numpy-1.26.4-py311_0":         {"numpy", "1.26.4", "py311_0"},
	} {
		legacy[file] = mustScan[string](t, db,
			`INSERT INTO conda_packages (repository_id, name, version, build_string, platform, blob_id, deleted_at)
			 VALUES ($1, $2, $3, $4, 'noarch', $5, CASE WHEN $6::boolean THEN now() END) RETURNING id`, repo, parts[0], parts[1], parts[2], blob, parts[0] == "ca")
	}

	migrateAll(t, db)

	for file, want := range map[string]string{
		"python-dateutil-2.9.0-pyh_0":  "python-dateutil|2.9.0|pyh_0",
		"a-b-c-d-1.0-0":                "a-b-c-d|1.0|0",
		"ca-certificates-2024.2.2-h_0": "ca-certificates|2024.2.2|h_0",
		"numpy-1.26.4-py311_0":         "numpy|1.26.4|py311_0",
	} {
		got := mustScan[string](t, db, `SELECT name || '|' || version || '|' || build_string FROM conda_packages WHERE id = $1`, legacy[file])
		if got != want {
			t.Errorf("legacy %s = %q, want %q", file, got, want)
		}
	}

	if got := mustScan[string](t, db, `SELECT name || '|' || version || '|' || build_string FROM conda_packages WHERE id = $1`, pre); got != "scikit-learn|1.0|py39_0" {
		t.Errorf("a row with a hyphen-free build was changed: %q", got)
	}

	if got := mustScan[string](t, db, `SELECT extension FROM conda_packages WHERE id = $1`, pre); got != ".tar.bz2" {
		t.Errorf("legacy row extension = %q, want .tar.bz2", got)
	}

	if got := mustScan[string](t, db,
		`SELECT build_number || '|' || md5 || '|' || timestamp_ms || '|' || index_extra::text || '|' || cardinality(depends) FROM conda_packages WHERE id = $1`, pre); got != "0||0|{}|0" {
		t.Errorf("legacy row defaults = %q", got)
	}

	const insert = `INSERT INTO conda_packages (repository_id, name, version, build_string, platform, blob_id, extension)
		 VALUES ($1, 'scikit-learn', '1.0', 'py39_0', 'linux-64', $2, $3)`

	// The .conda of the same build coexists; a second .conda does not.
	mustExec(t, db, insert, repo, blob, ".conda")

	_, err := db.ExecContext(context.Background(), insert, repo, blob, ".conda")
	wantPgCode(t, err, pgUniqueViolation, "duplicate .conda")

	_, err = db.ExecContext(context.Background(), insert, repo, blob, ".tar.bz2")
	wantPgCode(t, err, pgUniqueViolation, "duplicate .tar.bz2")

	_, err = db.ExecContext(context.Background(), insert, repo, blob, ".zip")
	wantPgCode(t, err, pgCheckViolation, "unknown extension")

	if !mustScan[bool](t, db, `SELECT convalidated FROM pg_constraint WHERE conname = 'chk_conda_packages_extension'`) {
		t.Error("chk_conda_packages_extension is not validated")
	}

	if n := mustScan[int](t, db, `SELECT count(*) FROM pg_index WHERE NOT indisvalid`); n != 0 {
		t.Errorf("%d invalid indexes left behind", n)
	}

	if n := mustScan[int](t, db, `SELECT count(*) FROM pg_indexes WHERE indexname = 'conda_packages_live_coords_key'`); n != 0 {
		t.Error("the old live-row index (without extension) is still there")
	}

	// A trashed .conda does not block uploading it again.
	mustExec(t, db, `UPDATE conda_packages SET deleted_at = now() WHERE extension = '.conda'`)
	mustExec(t, db, insert, repo, blob, ".conda")
}
