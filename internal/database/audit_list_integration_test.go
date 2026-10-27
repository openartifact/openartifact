// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// seedAudit inserts n entries sharing one created_at, so that only the id
// tiebreaker keeps a cursor walk deterministic, plus older distinct ones.
func seedAudit(t *testing.T, pg *PostgresDB, tied, older int) time.Time {
	t.Helper()

	ctx := context.Background()
	tie := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)

	if _, err := pg.GetDB().ExecContext(ctx,
		`INSERT INTO audit_logs (action, created_at) SELECT 'repository.created', $1 FROM generate_series(1, $2)`, tie, tied); err != nil {
		t.Fatal(err)
	}

	if _, err := pg.GetDB().ExecContext(ctx,
		`INSERT INTO audit_logs (action, created_at) SELECT 'repository.deleted', $1::timestamptz - (g || ' minutes')::interval FROM generate_series(1, $2) g`, tie, older); err != nil {
		t.Fatal(err)
	}

	return tie
}

func TestListAuditLogsKeysetPagingVisitsEveryRowOnce(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	seedAudit(t, pg, 7, 4)

	var (
		seen   []string
		cursor *models.AuditCursor
		pages  int
	)

	for {
		page, err := pg.ListAuditLogs(ctx, models.AuditListParams{Limit: 3, After: cursor})
		if err != nil {
			t.Fatal(err)
		}

		pages++

		for _, l := range page.Logs {
			seen = append(seen, l.ID)
		}

		if cursor != nil && page.Total != 0 {
			t.Errorf("keyset page reported a total (%d); counting is what it avoids", page.Total)
		}

		if page.Next == nil {
			break
		}

		if len(page.Logs) != 3 {
			t.Fatalf("a page with a next cursor holds %d rows, want the full 3", len(page.Logs))
		}

		cursor = page.Next

		if pages > 10 {
			t.Fatal("cursor walk does not terminate")
		}
	}

	if len(seen) != 11 {
		t.Fatalf("walk saw %d rows, want 11", len(seen))
	}

	seenSet := map[string]bool{}
	for _, id := range seen {
		if seenSet[id] {
			t.Errorf("row %s returned twice", id)
		}

		seenSet[id] = true
	}

	// The walk order must equal the unpaged order.
	all, err := pg.ListAuditLogs(ctx, models.AuditListParams{Limit: 100, NoTotal: true})
	if err != nil {
		t.Fatal(err)
	}

	var want []string
	for _, l := range all.Logs {
		want = append(want, l.ID)
	}

	if !slices.Equal(seen, want) {
		t.Errorf("cursor order differs from the full listing:\n got %v\nwant %v", seen, want)
	}

	if all.Next != nil {
		t.Error("a listing that fits one page has a next cursor")
	}
}

func TestListAuditLogsOffsetPagingAndTotals(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	seedAudit(t, pg, 3, 4)

	tests := []struct {
		name       string
		limit      int
		offset     int
		wantRows   int
		wantNext   bool
		wantTotal  int
		wantExact  bool
		noTotal    bool
		skipTotals bool
	}{
		{name: "first page", limit: 3, offset: 0, wantRows: 3, wantNext: true, wantTotal: 7, wantExact: true},
		{name: "middle page", limit: 3, offset: 3, wantRows: 3, wantNext: true, wantTotal: 7, wantExact: true},
		{name: "last partial page", limit: 3, offset: 6, wantRows: 1, wantNext: false, wantTotal: 7, wantExact: true},
		{name: "exactly the end", limit: 7, offset: 0, wantRows: 7, wantNext: false, wantTotal: 7, wantExact: true},
		{name: "past the end", limit: 3, offset: 50, wantRows: 0, wantNext: false, wantTotal: 0, wantExact: true, skipTotals: true},
		{name: "no total requested", limit: 3, offset: 0, wantRows: 3, wantNext: true, noTotal: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			page, err := pg.ListAuditLogs(ctx, models.AuditListParams{Limit: tc.limit, Offset: tc.offset, NoTotal: tc.noTotal})
			if err != nil {
				t.Fatal(err)
			}

			if len(page.Logs) != tc.wantRows || (page.Next != nil) != tc.wantNext {
				t.Errorf("rows=%d next=%v, want %d / %v", len(page.Logs), page.Next != nil, tc.wantRows, tc.wantNext)
			}

			if tc.noTotal && page.Total != 0 {
				t.Errorf("NoTotal page reported total %d", page.Total)
			}

			if !tc.noTotal && !tc.skipTotals && (page.Total != tc.wantTotal || page.TotalExact != tc.wantExact) {
				t.Errorf("total=%d exact=%v, want %d / %v", page.Total, page.TotalExact, tc.wantTotal, tc.wantExact)
			}
		})
	}

	t.Run("past the end still reports the real total", func(t *testing.T) {
		page, err := pg.ListAuditLogs(ctx, models.AuditListParams{Limit: 3, Offset: 50})
		if err != nil {
			t.Fatal(err)
		}

		if page.Total != 7 || !page.TotalExact {
			t.Errorf("total = %d exact = %v, want the real 7", page.Total, page.TotalExact)
		}
	})

	t.Run("limits below one are rejected", func(t *testing.T) {
		for _, limit := range []int{0, -5} {
			if _, err := pg.ListAuditLogs(ctx, models.AuditListParams{Limit: limit}); !errors.Is(err, ErrInvalidAuditLimit) {
				t.Errorf("limit %d: err = %v, want ErrInvalidAuditLimit", limit, err)
			}
		}
	})
}

func TestListAuditLogsFilters(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	db := pg.GetDB()

	var projectID, otherProject string
	if err := db.QueryRowContext(ctx, `INSERT INTO projects (name, slug) VALUES ('p1','p1') RETURNING id`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}

	if err := db.QueryRowContext(ctx, `INSERT INTO projects (name, slug) VALUES ('p2','p2') RETURNING id`).Scan(&otherProject); err != nil {
		t.Fatal(err)
	}

	var ownedRepo, foreignRepo string
	if err := db.QueryRowContext(ctx, `INSERT INTO repositories (name, format, project_id) VALUES ('owned','raw',$1) RETURNING id`, projectID).
		Scan(&ownedRepo); err != nil {
		t.Fatal(err)
	}

	if err := db.QueryRowContext(ctx, `INSERT INTO repositories (name, format, project_id) VALUES ('foreign','raw',$1) RETURNING id`, otherProject).
		Scan(&foreignRepo); err != nil {
		t.Fatal(err)
	}

	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	type row struct {
		action  string
		project string
		repo    string
		details string
		at      time.Time
	}

	rows := []row{
		{"project.updated", projectID, "", `{"actor":"alice"}`, base},
		{"repository.updated", "", ownedRepo, `{"actor":"bob","package":"left-pad","version":"1.0.0"}`, base.Add(time.Minute)},
		{"repository.updated", "", foreignRepo, `{"actor":"alice","package":"left-pad","version":"2.0.0"}`, base.Add(2 * time.Minute)},
		{"project.token_created", projectID, "", `{"actor":"alice"}`, base.Add(3 * time.Minute)},
		{"user.created", "", "", `{"actor":"50%_off"}`, base.Add(4 * time.Minute)},
		{"user.deleted", "", "", `{"actor":"plain"}`, base.Add(5 * time.Minute)},
	}

	for _, r := range rows {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO audit_logs (action, project_id, repository_id, details_json, created_at)
			 VALUES ($1, NULLIF($2,'')::uuid, NULLIF($3,'')::uuid, $4::jsonb, $5)`,
			r.action, r.project, r.repo, r.details, r.at); err != nil {
			t.Fatal(err)
		}
	}

	actions := func(t *testing.T, p models.AuditListParams) []string {
		t.Helper()

		p.Limit = 100

		page, err := pg.ListAuditLogs(ctx, p)
		if err != nil {
			t.Fatal(err)
		}

		out := make([]string, len(page.Logs))
		for i, l := range page.Logs {
			out[i] = l.Action
		}

		return out
	}

	at := func(d time.Duration) *time.Time { v := base.Add(d); return &v }

	tests := []struct {
		name string
		p    models.AuditListParams
		want []string // newest first
	}{
		{"project folds in its repositories' events", models.AuditListParams{ProjectID: projectID},
			[]string{"project.token_created", "repository.updated", "project.updated"}},
		{"another project sees only its own", models.AuditListParams{ProjectID: otherProject}, []string{"repository.updated"}},
		{"one repository only", models.AuditListParams{RepositoryID: ownedRepo}, []string{"repository.updated"}},
		{"token events can be hidden", models.AuditListParams{ProjectID: projectID, HideProjectTokenEvents: true},
			[]string{"repository.updated", "project.updated"}},
		{"package filter", models.AuditListParams{PackageName: "left-pad"}, []string{"repository.updated", "repository.updated"}},
		{"package and version", models.AuditListParams{PackageName: "left-pad", PackageVersion: "2.0.0"}, []string{"repository.updated"}},
		{"action substring, case-insensitive", models.AuditListParams{Action: "TOKEN"}, []string{"project.token_created"}},
		{"a percent in the action filter is not a wildcard", models.AuditListParams{Action: "project%updated"}, []string{}},
		{"an underscore in the action filter is not a wildcard", models.AuditListParams{Action: "project_updated"}, []string{}},
		{"an underscore still matches itself", models.AuditListParams{Action: "token_c"}, []string{"project.token_created"}},
		{"actor by name", models.AuditListParams{Actor: "bob"}, []string{"repository.updated"}},
		{"actor wildcards are literal", models.AuditListParams{Actor: "50%_"}, []string{"user.created"}},
		{"actor underscore is not a wildcard", models.AuditListParams{Actor: "pl_in"}, []string{}},
		{"since is inclusive", models.AuditListParams{Since: at(4 * time.Minute)}, []string{"user.deleted", "user.created"}},
		{"until is exclusive", models.AuditListParams{Until: at(time.Minute)}, []string{"project.updated"}},
		{
			"since and until together",
			models.AuditListParams{Since: at(time.Minute), Until: at(3 * time.Minute)},
			[]string{"repository.updated", "repository.updated"},
		},
		{"nothing matches", models.AuditListParams{Actor: "nobody"}, []string{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := actions(t, tc.p); !slices.Equal(got, tc.want) {
				t.Errorf("actions = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("filtered totals count only the matches", func(t *testing.T) {
		page, err := pg.ListAuditLogs(ctx, models.AuditListParams{ProjectID: projectID, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}

		if page.Total != 3 || !page.TotalExact || page.Next == nil {
			t.Errorf("total=%d exact=%v next=%v, want 3 / true / non-nil", page.Total, page.TotalExact, page.Next != nil)
		}
	})
}

func TestInsertAuditLogStoresAddressesAndToleratesGarbage(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)

	for i, ip := range []*string{new("203.0.113.7"), new("2001:db8::1"), new("not-an-ip"), nil} {
		ev := &models.AuditLog{
			Action:      "user." + []string{"created", "updated", "deleted", "disabled"}[i],
			IPAddress:   ip,
			DetailsJSON: []byte(`{}`),
		}
		if err := pg.InsertAuditLog(ctx, ev); err != nil {
			t.Fatalf("InsertAuditLog(%v): %v", ip, err)
		}
	}

	page, err := pg.ListAuditLogs(ctx, models.AuditListParams{Limit: 10, NoTotal: true})
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}

	for _, l := range page.Logs {
		v := "<nil>"
		if l.IPAddress != nil {
			v = *l.IPAddress
		}

		got[l.Action] = v
	}

	want := map[string]string{
		"user.created":  "203.0.113.7",
		"user.updated":  "2001:db8::1",
		"user.deleted":  "<nil>",
		"user.disabled": "<nil>",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s stored as %q, want %q", k, got[k], v)
		}
	}
}
