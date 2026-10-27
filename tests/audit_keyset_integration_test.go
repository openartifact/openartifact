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
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

type auditSeedRow struct {
	id      string
	action  string
	actor   string
	created time.Time
}

// seedAuditRows inserts n rows whose created_at values come in groups of four
// identical timestamps, so keyset paging has to break ties on id. Ids ascend
// with i but timestamps ascend with i/4, so id order inside a tie group differs
// from insertion order of other groups.
func seedAuditRows(t *testing.T, pg *database.PostgresDB, n int) []auditSeedRow {
	t.Helper()

	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	actions := []string{"auth.login", "artifact.uploaded", "repository.created"}
	rows := make([]auditSeedRow, n)

	for i := range rows {
		rows[i] = auditSeedRow{
			id:      fmt.Sprintf("00000000-0000-4000-8000-%012d", n-i), // descending ids: ties must still order by id ASC
			action:  actions[i%len(actions)],
			actor:   fmt.Sprintf("user%d", i%2),
			created: base.Add(time.Duration(i/4) * time.Second),
		}

		if _, err := pg.GetDB().ExecContext(context.Background(),
			`INSERT INTO audit_logs (id, action, created_at, actor_type, details_json) VALUES ($1, $2, $3, 'user', jsonb_build_object('actor', $4::text))`,
			rows[i].id, rows[i].action, rows[i].created, rows[i].actor); err != nil {
			t.Fatalf("seed audit row %d: %v", i, err)
		}
	}

	return rows
}

// wantAuditOrder is the contract order: created_at DESC, id ASC.
func wantAuditOrder(rows []auditSeedRow, keep func(auditSeedRow) bool) []string {
	var sel []auditSeedRow

	for _, r := range rows {
		if keep == nil || keep(r) {
			sel = append(sel, r)
		}
	}

	slices.SortFunc(sel, func(a, b auditSeedRow) int {
		if c := b.created.Compare(a.created); c != 0 {
			return c
		}

		if a.id < b.id {
			return -1
		}

		return 1
	})

	ids := make([]string, len(sel))
	for i, r := range sel {
		ids[i] = r.id
	}

	return ids
}

func auditIDs(logs []*models.AuditLog) []string {
	ids := make([]string, len(logs))
	for i, l := range logs {
		ids[i] = l.ID
	}

	return ids
}

// walkAuditKeyset follows Next cursors to the end and returns every id in
// visit order plus the page sizes seen.
func walkAuditKeyset(t *testing.T, pg *database.PostgresDB, p models.AuditListParams) ([]string, []int) {
	t.Helper()

	var (
		ids   []string
		pages []int
	)

	for range 1000 {
		page, err := pg.ListAuditLogs(context.Background(), p)
		if err != nil {
			t.Fatalf("ListAuditLogs: %v", err)
		}

		ids = append(ids, auditIDs(page.Logs)...)
		pages = append(pages, len(page.Logs))

		if page.Next == nil {
			return ids, pages
		}

		if last := page.Logs[len(page.Logs)-1]; page.Next.ID != last.ID || !page.Next.CreatedAt.Equal(last.CreatedAt) {
			t.Fatalf("Next cursor %+v is not the last row of the page (%s %v)", page.Next, last.ID, last.CreatedAt)
		}

		p.After = page.Next
	}

	t.Fatal("keyset walk did not terminate")

	return nil, nil
}

func TestAuditKeysetPagingWalksEveryRowOnce(t *testing.T) {
	pg := freshPostgres(t, 5)
	rows := seedAuditRows(t, pg, 57)

	tests := []struct {
		name   string
		params models.AuditListParams
		keep   func(auditSeedRow) bool
	}{
		{name: "size 1", params: models.AuditListParams{Limit: 1}},
		{name: "size inside a tie group", params: models.AuditListParams{Limit: 3}},
		{name: "size equal to tie group", params: models.AuditListParams{Limit: 4}},
		{name: "size not dividing total", params: models.AuditListParams{Limit: 10}},
		{name: "size dividing total", params: models.AuditListParams{Limit: 19}},
		{name: "size equal to total", params: models.AuditListParams{Limit: 57}},
		{name: "size above total", params: models.AuditListParams{Limit: 100}},
		{
			name:   "action filter",
			params: models.AuditListParams{Limit: 5, Action: "auth.login"},
			keep:   func(r auditSeedRow) bool { return r.action == "auth.login" },
		},
		{
			name:   "actor filter",
			params: models.AuditListParams{Limit: 6, Actor: "user1"},
			keep:   func(r auditSeedRow) bool { return r.actor == "user1" },
		},
		{
			name:   "actor and action filter",
			params: models.AuditListParams{Limit: 2, Actor: "user0", Action: "artifact"},
			keep:   func(r auditSeedRow) bool { return r.actor == "user0" && r.action == "artifact.uploaded" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := wantAuditOrder(rows, tt.keep)

			// Keyset walk, starting from the first page (After == nil).
			got, pages := walkAuditKeyset(t, pg, tt.params)
			if !slices.Equal(got, want) {
				t.Fatalf("keyset walk order mismatch:\n got %v\nwant %v", got, want)
			}

			// Every page is full except possibly the last.
			for i, n := range pages[:len(pages)-1] {
				if n != tt.params.Limit {
					t.Errorf("page %d has %d rows, want %d", i, n, tt.params.Limit)
				}
			}

			// has_more is exact: the last page is the only one without Next.
			wantPages := (len(want) + tt.params.Limit - 1) / tt.params.Limit
			if len(pages) != wantPages {
				t.Errorf("walked %d pages, want %d", len(pages), wantPages)
			}
		})
	}
}

func TestAuditOffsetPagingUnchanged(t *testing.T) {
	pg := freshPostgres(t, 5)
	rows := seedAuditRows(t, pg, 57)
	all := wantAuditOrder(rows, nil)

	tests := []struct {
		name      string
		params    models.AuditListParams
		wantIDs   []string
		wantTotal int
		wantNext  bool
	}{
		{name: "first page", params: models.AuditListParams{Limit: 10}, wantIDs: all[:10], wantTotal: 57, wantNext: true},
		{name: "middle page", params: models.AuditListParams{Limit: 10, Offset: 20}, wantIDs: all[20:30], wantTotal: 57, wantNext: true},
		{name: "last partial page", params: models.AuditListParams{Limit: 10, Offset: 50}, wantIDs: all[50:], wantTotal: 57},
		{name: "last exact page", params: models.AuditListParams{Limit: 7, Offset: 50}, wantIDs: all[50:], wantTotal: 57},
		{name: "past the end", params: models.AuditListParams{Limit: 10, Offset: 200}, wantIDs: nil, wantTotal: 57},
		{
			name:      "filtered",
			params:    models.AuditListParams{Limit: 5, Offset: 5, Action: "auth.login"},
			wantIDs:   wantAuditOrder(rows, func(r auditSeedRow) bool { return r.action == "auth.login" })[5:10],
			wantTotal: 19,
			wantNext:  true,
		},
		{name: "filter without matches", params: models.AuditListParams{Limit: 5, Actor: "nobody"}, wantIDs: nil, wantTotal: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := pg.ListAuditLogs(context.Background(), tt.params)
			if err != nil {
				t.Fatalf("ListAuditLogs: %v", err)
			}

			if got := auditIDs(page.Logs); !slices.Equal(got, tt.wantIDs) && (len(got) != 0 || len(tt.wantIDs) != 0) {
				t.Errorf("ids = %v, want %v", got, tt.wantIDs)
			}

			if page.Total != tt.wantTotal || !page.TotalExact {
				t.Errorf("total = %d exact=%v, want %d exact", page.Total, page.TotalExact, tt.wantTotal)
			}

			if (page.Next != nil) != tt.wantNext {
				t.Errorf("Next = %+v, want present=%v", page.Next, tt.wantNext)
			}
		})
	}
}

func TestAuditTotalAvoidsCountOnLargeTables(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()

	const rows = 12000 // above the exact-count ceiling

	if _, err := pg.GetDB().ExecContext(ctx, `
		INSERT INTO audit_logs (action, created_at, actor_type, details_json)
		SELECT CASE WHEN g % 100 = 0 THEN 'auth.login' ELSE 'artifact.downloaded' END,
		       now() - g * interval '1 second', 'user', jsonb_build_object('actor', 'bench')
		FROM generate_series(1, $1) g`, rows); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := pg.GetDB().ExecContext(ctx, `ANALYZE audit_logs`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	tests := []struct {
		name      string
		params    models.AuditListParams
		wantTotal int
		wantExact bool
	}{
		{name: "unfiltered uses the planner estimate", params: models.AuditListParams{Limit: 25}, wantTotal: rows},
		{name: "filtered reports a lower bound", params: models.AuditListParams{Limit: 25, Action: "artifact.downloaded"}, wantTotal: 26},
		{
			name:      "filtered lower bound honours offset",
			params:    models.AuditListParams{Limit: 25, Offset: 50, Action: "artifact.downloaded"},
			wantTotal: 76,
		},
		{
			name:      "filtered final page is exact",
			params:    models.AuditListParams{Limit: 25, Offset: 100, Action: "auth.login"},
			wantTotal: 120,
			wantExact: true,
		},
		{
			name:      "unfiltered final page is exact",
			params:    models.AuditListParams{Limit: 25, Offset: rows - 10},
			wantTotal: rows,
			wantExact: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := pg.ListAuditLogs(ctx, tt.params)
			if err != nil {
				t.Fatalf("ListAuditLogs: %v", err)
			}

			if page.Total != tt.wantTotal || page.TotalExact != tt.wantExact {
				t.Errorf("total = %d exact=%v, want %d exact=%v", page.Total, page.TotalExact, tt.wantTotal, tt.wantExact)
			}
		})
	}
}

// A project's activity is the entries naming the project plus those of the
// repositories it owns, never another project's, and it pages by keyset.
func TestAuditProjectScopeIncludesOwnedRepositoriesOnly(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()

	acme := &models.Project{Name: "acme"}
	beta := &models.Project{Name: "beta"}

	for _, p := range []*models.Project{acme, beta} {
		if err := pg.CreateProject(ctx, p); err != nil {
			t.Fatalf("CreateProject %s: %v", p.Name, err)
		}
	}

	mkRepo := func(name string, project *models.Project) *models.Repository {
		r := &models.Repository{Name: name, Format: "raw", RepoType: "hosted", ProjectID: &project.ID}
		if err := pg.CreateRepository(ctx, r); err != nil {
			t.Fatalf("CreateRepository %s: %v", name, err)
		}

		return r
	}

	acmeRepo, betaRepo := mkRepo("acme-raw", acme), mkRepo("beta-raw", beta)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	ids := map[string]string{}
	seed := func(i int, key, action string, project, repo *string) {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		ids[key] = id

		if _, err := pg.GetDB().ExecContext(ctx,
			`INSERT INTO audit_logs (id, action, created_at, actor_type, details_json, project_id, repository_id) VALUES ($1, $2, $3, 'user', '{}', $4, $5)`,
			id, action, base.Add(time.Duration(i)*time.Second), project, repo); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}

	seed(1, "acme-project", "project.member_added", &acme.ID, nil)
	seed(2, "acme-repo", "artifact.purged", nil, &acmeRepo.ID)
	seed(3, "beta-project", "project.member_added", &beta.ID, nil)
	seed(4, "beta-repo", "artifact.purged", nil, &betaRepo.ID)
	seed(5, "global", "http.request", nil, nil)
	seed(6, "acme-both", "project.repository_created", &acme.ID, &acmeRepo.ID) // matches both branches: listed once

	want := []string{ids["acme-both"], ids["acme-repo"], ids["acme-project"]} // newest first

	got, pages := walkAuditKeyset(t, pg, models.AuditListParams{ProjectID: acme.ID, Limit: 1, NoTotal: true})
	if !slices.Equal(got, want) {
		t.Fatalf("acme activity = %v, want %v", got, want)
	}

	if len(pages) != 3 {
		t.Errorf("pages = %v, want one entry per page", pages)
	}

	page, err := pg.ListAuditLogs(ctx, models.AuditListParams{ProjectID: beta.ID, Limit: 10, NoTotal: true})
	if err != nil {
		t.Fatalf("ListAuditLogs beta: %v", err)
	}

	if gotBeta, wantBeta := auditIDs(
		page.Logs,
	), []string{
		ids["beta-repo"],
		ids["beta-project"],
	}; !slices.Equal(gotBeta, wantBeta) || page.Next != nil ||
		page.Total != 0 {
		t.Errorf("beta activity = %v (next %v, total %d), want %v with no count", gotBeta, page.Next, page.Total, wantBeta)
	}

	// The filters narrow inside the project.
	page, err = pg.ListAuditLogs(ctx, models.AuditListParams{ProjectID: acme.ID, Limit: 10, Action: "purged"})
	if err != nil || !slices.Equal(auditIDs(page.Logs), []string{ids["acme-repo"]}) {
		t.Errorf("filtered = %v, %v", auditIDs(page.Logs), err)
	}

	// A repository moved to another project takes its history along.
	if err := pg.MoveRepositoryToProject(ctx, acmeRepo.ID, beta.ID, 0); err != nil {
		t.Fatalf("MoveRepositoryToProject: %v", err)
	}

	page, err = pg.ListAuditLogs(ctx, models.AuditListParams{ProjectID: beta.ID, Limit: 10, NoTotal: true})
	if err != nil {
		t.Fatalf("ListAuditLogs beta after move: %v", err)
	}

	if !slices.Contains(auditIDs(page.Logs), ids["acme-repo"]) {
		t.Errorf("beta activity after transfer = %v, want it to hold the moved repository's entries", auditIDs(page.Logs))
	}
}
