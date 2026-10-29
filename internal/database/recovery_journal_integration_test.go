// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestRecoveryJournalSkipsDownloadCounts: a download only bumps download_count
// (and updated_at); the journal does not queue the row for a new sidecar,
// while any other change to the row still does.
func TestRecoveryJournalSkipsDownloadCounts(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	f.seedNPMVersion(t, "alpha", "1.0.0", 10, time.Now().UTC().Add(-time.Hour))

	id := f.ids["alpha@1.0.0"]
	dirty := func() int {
		t.Helper()

		var n int
		if err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM recovery_dirty WHERE table_name = 'package_files' AND row_key = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}

		return n
	}

	reset := func() {
		t.Helper()

		if _, err := f.db.ExecContext(ctx, `DELETE FROM recovery_dirty`); err != nil {
			t.Fatal(err)
		}
	}

	if dirty() != 1 {
		t.Fatalf("the insert queued %d marks, want 1", dirty())
	}

	reset()

	for range 3 {
		if _, err := f.db.ExecContext(ctx, `UPDATE package_files SET download_count = download_count + 1, updated_at = now() WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := f.db.ExecContext(ctx, `UPDATE package_files SET download_count = download_count + 1 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	if n := dirty(); n != 0 {
		t.Fatalf("%d download updates queued %d journal marks, want 0", 4, n)
	}

	for name, stmt := range map[string]string{
		"filename":           `UPDATE package_files SET filename = 'renamed.tgz' WHERE id = $1`,
		"trash":              `UPDATE package_files SET deleted_at = now() WHERE id = $1`,
		"counter and column": `UPDATE package_files SET download_count = download_count + 1, arch = 'x' WHERE id = $1`,
	} {
		reset()

		if _, err := f.db.ExecContext(ctx, stmt, id); err != nil {
			t.Fatal(fmt.Errorf("%s: %w", name, err))
		}

		if dirty() != 1 {
			t.Errorf("%s: the update queued no journal mark", name)
		}
	}

	reset()

	if _, err := f.db.ExecContext(ctx, `DELETE FROM package_files WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	if dirty() != 1 {
		t.Error("a delete queued no journal mark")
	}
}
