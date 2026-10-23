// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// TestAuditProjectActivityUsesBitmapOr guards the project-activity predicate:
// "project_id = $1 OR repository_id = ANY(ARRAY(subquery))" must be served by
// a BitmapOr of the two partial indexes, not a scan of the whole table.
func TestAuditProjectActivityUsesBitmapOr(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	db := pg.GetDB()

	var projectID string
	if err := db.QueryRowContext(ctx, `INSERT INTO projects (name, slug) VALUES ('acme', 'acme') RETURNING id`).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	var otherID string
	if err := db.QueryRowContext(ctx, `INSERT INTO projects (name, slug) VALUES ('other', 'other') RETURNING id`).Scan(&otherID); err != nil {
		t.Fatalf("insert other project: %v", err)
	}

	// 2000 repositories; the project owns the first two.
	if _, err := db.ExecContext(ctx, `INSERT INTO repositories (id, name, format, project_id)
		SELECT gen_random_uuid(), 'r' || i, 'raw', CASE WHEN i <= 2 THEN $1::uuid ELSE $2::uuid END FROM generate_series(1, 2000) i`, projectID, otherID); err != nil {
		t.Fatalf("insert repositories: %v", err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO audit_logs (action, repository_id, created_at)
		SELECT 'repository.updated', r.id, now() - (g || ' seconds')::interval
		FROM (SELECT id, row_number() OVER () AS n FROM repositories) r, generate_series(1, 40) g`); err != nil {
		t.Fatalf("insert audit rows: %v", err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO audit_logs (action, project_id) SELECT 'project.updated', $1::uuid FROM generate_series(1, 10)`, projectID); err != nil {
		t.Fatalf("insert project audit rows: %v", err)
	}

	if _, err := db.ExecContext(ctx, `ANALYZE audit_logs; ANALYZE repositories`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	where, args := auditFilterWhere(models.AuditListParams{ProjectID: projectID})

	// The filter alone (no ORDER BY .. LIMIT, which lets the planner prefer a
	// created_at walk on a table this small) must be index-addressable.
	if plan := explain(t, db, `EXPLAIN SELECT count(*) FROM audit_logs `+where, args...); !strings.Contains(plan, "BitmapOr") {
		t.Errorf("plan does not use BitmapOr:\n%s", plan)
	}

	// Behavior: the 10 project rows plus the 2 owned repositories' 80 rows.
	page, err := pg.ListAuditLogs(ctx, models.AuditListParams{ProjectID: projectID, Limit: 500, NoTotal: true})
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}

	if len(page.Logs) != 90 {
		t.Errorf("project activity rows = %d, want 90", len(page.Logs))
	}

	page, err = pg.ListAuditLogs(ctx, models.AuditListParams{ProjectID: projectID, Limit: 500, NoTotal: true, HideProjectTokenEvents: true})
	if err != nil || len(page.Logs) != 90 {
		t.Errorf("hide-token listing = %v, %v", page, err)
	}
}

// TestAuditHideProjectTokenEvents checks the maintainer-only filter.
func TestAuditHideProjectTokenEvents(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	db := pg.GetDB()

	var projectID string
	if err := db.QueryRowContext(ctx, `INSERT INTO projects (name, slug) VALUES ('acme', 'acme') RETURNING id`).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	for _, a := range []string{"project.token_created", "project.token_revoked", "project.updated"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO audit_logs (action, project_id) VALUES ($1, $2::uuid)`, a, projectID); err != nil {
			t.Fatalf("insert %s: %v", a, err)
		}
	}

	for hide, want := range map[bool]int{false: 3, true: 1} {
		page, err := pg.ListAuditLogs(ctx, models.AuditListParams{ProjectID: projectID, Limit: 50, NoTotal: true, HideProjectTokenEvents: hide})
		if err != nil || len(page.Logs) != want {
			t.Errorf("hide=%v: rows = %v, err = %v, want %d", hide, page, err, want)
		}
	}
}

func explain(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()

	rows, err := db.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder

	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}

		plan.WriteString(line + "\n")
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return plan.String()
}

// TestAuditExportKeysetWalkAndRange walks the whole log with keyset pages
// (what the export does) and checks the since (inclusive) / until (exclusive)
// bounds, including rows that share one created_at.
func TestAuditExportKeysetWalkAndRange(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	db := pg.GetDB()

	// 50 rows, one per minute; rows 10 and 11 share a timestamp, so the
	// cursor's id tiebreak is exercised.
	if _, err := db.ExecContext(ctx, `INSERT INTO audit_logs (action, created_at)
		SELECT 'repository.updated', TIMESTAMPTZ '2026-03-01 12:00:00+00' - (CASE WHEN i = 11 THEN 10 ELSE i END || ' minutes')::interval
		FROM generate_series(0, 49) i`); err != nil {
		t.Fatalf("insert audit rows: %v", err)
	}

	walk := func(params models.AuditListParams) []*models.AuditLog {
		t.Helper()

		params.Limit, params.NoTotal = 7, true

		var all []*models.AuditLog

		for {
			page, err := pg.ListAuditLogs(ctx, params)
			if err != nil {
				t.Fatalf("ListAuditLogs: %v", err)
			}

			all = append(all, page.Logs...)

			if page.Next == nil {
				return all
			}

			params.After = page.Next
		}
	}

	all := walk(models.AuditListParams{})
	if len(all) != 50 {
		t.Fatalf("walk returned %d rows, want 50", len(all))
	}

	seen := map[string]bool{}

	for i, l := range all {
		if seen[l.ID] {
			t.Fatalf("row %s returned twice", l.ID)
		}

		seen[l.ID] = true

		if i > 0 && l.CreatedAt.After(all[i-1].CreatedAt) {
			t.Fatalf("row %d is newer than its predecessor", i)
		}
	}

	since := time.Date(2026, 3, 1, 11, 30, 0, 0, time.UTC)
	until := time.Date(2026, 3, 1, 11, 50, 0, 0, time.UTC)

	// 11:30 (inclusive) .. 11:50 (exclusive): 11:49 down to 11:30 is 20
	// minutes, minus 11:49 (no row: its slot holds the duplicated 11:50), and
	// the two 11:50 rows are out.
	ranged := walk(models.AuditListParams{Since: &since, Until: &until})
	if len(ranged) != 19 {
		t.Errorf("range returned %d rows, want 19", len(ranged))
	}

	for _, l := range ranged {
		if l.CreatedAt.Before(since) || !l.CreatedAt.Before(until) {
			t.Errorf("row at %v outside [%v, %v)", l.CreatedAt, since, until)
		}
	}

	if open := walk(models.AuditListParams{Since: &until}); len(open) != 12 {
		t.Errorf("since-only returned %d rows, want 12", len(open))
	}
}
