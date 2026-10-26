// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// platformFixture seeds two projects, two repositories, three users, tokens of
// every kind and quotas, and returns the IDs the tests assert on.
type platformFixture struct {
	acmeID, betaID     string
	r1, r2             string
	dana, lee, service string
}

func platformSeed(t *testing.T, pg *PostgresDB) platformFixture {
	t.Helper()

	db := pg.GetDB()
	ctx := context.Background()
	f := platformFixture{}

	scan := func(dst *string, q string, args ...any) {
		t.Helper()

		if err := db.QueryRowContext(ctx, q, args...).Scan(dst); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	scan(&f.acmeID, `INSERT INTO projects (name, slug) VALUES ('acme', 'acme') RETURNING id`)
	scan(&f.betaID, `INSERT INTO projects (name, slug) VALUES ('beta', 'beta') RETURNING id`)
	scan(&f.dana, `INSERT INTO auth_users (username) VALUES ('dana') RETURNING id`)
	scan(&f.lee, `INSERT INTO auth_users (username) VALUES ('lee') RETURNING id`)
	scan(&f.service, `INSERT INTO auth_users (username, kind, project_id) VALUES ('svc-acme', 'service', $1) RETURNING id`, f.acmeID)
	scan(&f.r1, `INSERT INTO repositories (name, format, project_id) VALUES ('r1', 'raw', $1) RETURNING id`, f.acmeID)
	scan(&f.r2, `INSERT INTO repositories (name, format, project_id) VALUES ('r2', 'raw', $1) RETURNING id`, f.betaID)

	exec := func(q string, args ...any) {
		t.Helper()

		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	// Personal: dana's CI token expires in the past, lee's never; dana's revoked one is hidden.
	exec(`INSERT INTO auth_tokens (user_id, name, kind, token_hash, scopes, expires_at, created_at)
	      VALUES ($1, 'dana-ci', 'access', 'h1', '["pt-1"]', now() - interval '1 day', now() - interval '3 hours')`, f.dana)
	exec(`INSERT INTO auth_tokens (user_id, name, kind, token_hash, created_at) VALUES ($1, 'lee-laptop', 'access', 'h2', now() - interval '2 hours')`, f.lee)
	exec(`INSERT INTO auth_tokens (user_id, name, kind, token_hash, revoked_at) VALUES ($1, 'dana-old', 'access', 'h3', now())`, f.dana)
	// Project: a token of acme created by lee, one of beta created by dana.
	exec(`INSERT INTO auth_tokens (user_id, name, kind, token_hash, project_id, project_role, created_by, expires_at, created_at)
	      VALUES ($1, 'acme-deploy', 'deploy', 'h4', $2, 'developer', $3, now() + interval '10 days', now() - interval '1 hour')`, f.service, f.acmeID, f.lee)
	// Federation: a token of acme's r1 and one of beta's r2 (+ a revoked one).
	exec(`INSERT INTO federation_repos (repo_id) VALUES ($1), ($2)`, f.r1, f.r2)
	exec(`INSERT INTO federation_peer_tokens (repo_id, name, token_hash, created_by, created_at) VALUES ($1, 'peer-a', '\x01', 'lee', now() - interval '30 minutes')`, f.r1)
	exec(`INSERT INTO federation_peer_tokens (repo_id, name, token_hash, created_by, created_at) VALUES ($1, 'peer-b', '\x02', 'dana', now() - interval '20 minutes')`, f.r2)
	exec(`INSERT INTO federation_peer_tokens (repo_id, name, token_hash, created_by, revoked_at) VALUES ($1, 'peer-gone', '\x03', 'dana', now())`, f.r1)

	// Quotas: r1 is limited and over it, r2 has counters only; acme has a ceiling.
	exec(`INSERT INTO storage_quotas (repository_id, max_size_bytes, current_size_bytes, current_artifact_count) VALUES ($1, 100, 150, 3)`, f.r1)
	exec(`INSERT INTO storage_quotas (repository_id, current_size_bytes) VALUES ($1, 40)`, f.r2)
	exec(`INSERT INTO project_quotas (project_id, max_size_bytes) VALUES ($1, 1000)`, f.acmeID)
	exec(`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'owner'), ($1, $3, 'viewer')`, f.acmeID, f.dana, f.lee)

	return f
}

func tokenNames(items []*models.TokenListItem) []string {
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.Name)
	}

	slices.Sort(names)

	return names
}

func TestListUnifiedTokensVisibilityAndFilters(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	f := platformSeed(t, pg)
	ctx := context.Background()

	list := func(p models.TokenListParams) ([]*models.TokenListItem, int) {
		t.Helper()

		p.Page, p.Limit = 1, 50

		items, total, err := pg.ListUnifiedTokens(ctx, p)
		if err != nil {
			t.Fatalf("ListUnifiedTokens(%+v): %v", p, err)
		}

		if total != len(items) {
			t.Errorf("total = %d for %d items", total, len(items))
		}

		return items, total
	}

	all := []string{"acme-deploy", "dana-ci", "lee-laptop", "peer-a", "peer-b"}

	if items, _ := list(models.TokenListParams{Admin: true}); !slices.Equal(tokenNames(items), all) {
		t.Errorf("admin sees %v, want %v (revoked tokens hidden)", tokenNames(items), all)
	}

	// dana: her own personal token plus the tokens of acme (she is not a maintainer of beta).
	items, _ := list(models.TokenListParams{ViewerID: f.dana, ProjectIDs: []string{f.acmeID}})
	if want := []string{"acme-deploy", "dana-ci", "peer-a"}; !slices.Equal(tokenNames(items), want) {
		t.Errorf("maintainer sees %v, want %v", tokenNames(items), want)
	}

	// A viewer with no maintained project sees personal tokens only, never another user's.
	items, _ = list(models.TokenListParams{ViewerID: f.lee})
	if want := []string{"lee-laptop"}; !slices.Equal(tokenNames(items), want) {
		t.Errorf("plain user sees %v, want %v", tokenNames(items), want)
	}

	// No viewer and no admin: nothing.
	if items, _ := list(models.TokenListParams{}); len(items) != 0 {
		t.Errorf("unscoped caller sees %v", tokenNames(items))
	}

	for name, tc := range map[string]struct {
		p    models.TokenListParams
		want []string
	}{
		"type personal":   {models.TokenListParams{Admin: true, Types: []string{"personal"}}, []string{"dana-ci", "lee-laptop"}},
		"type project":    {models.TokenListParams{Admin: true, Types: []string{"project"}}, []string{"acme-deploy"}},
		"type federation": {models.TokenListParams{Admin: true, Types: []string{"federation"}}, []string{"peer-a", "peer-b"}},
		"query on name":   {models.TokenListParams{Admin: true, Query: "PEER"}, []string{"peer-a", "peer-b"}},
		"query on owner":  {models.TokenListParams{Admin: true, Query: "lee"}, []string{"acme-deploy", "lee-laptop", "peer-a"}},
		"project filter":  {models.TokenListParams{Admin: true, Project: "acme"}, []string{"acme-deploy", "peer-a"}},
		"expired already": {models.TokenListParams{Admin: true, ExpiresBefore: new(time.Now())}, []string{"dana-ci"}},
		"expires in 30d":  {models.TokenListParams{Admin: true, ExpiresBefore: new(time.Now().Add(30 * 24 * time.Hour))}, []string{"acme-deploy", "dana-ci"}},
	} {
		if items, _ := list(tc.p); !slices.Equal(tokenNames(items), tc.want) {
			t.Errorf("%s: got %v, want %v", name, tokenNames(items), tc.want)
		}
	}
}

func TestListUnifiedTokensFieldsAndPaging(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	platformSeed(t, pg)
	ctx := context.Background()

	items, total, err := pg.ListUnifiedTokens(ctx, models.TokenListParams{Admin: true, Page: 1, Limit: 2})
	if err != nil {
		t.Fatalf("ListUnifiedTokens: %v", err)
	}

	if total != 5 || len(items) != 2 {
		t.Fatalf("total = %d, page size = %d, want 5 and 2", total, len(items))
	}

	// Newest first: peer-b (20 min), peer-a (30 min).
	if items[0].Name != "peer-b" || items[1].Name != "peer-a" {
		t.Errorf("order = %s, %s", items[0].Name, items[1].Name)
	}

	if f := items[0]; f.Type != "federation" || f.Repository != "r2" || f.Project != "beta" || f.Owner != "dana" || f.ExpiresAt != nil {
		t.Errorf("federation token = %+v", f)
	}

	rest, _, err := pg.ListUnifiedTokens(ctx, models.TokenListParams{Admin: true, Page: 3, Limit: 2})
	if err != nil || len(rest) != 1 {
		t.Fatalf("last page = %v, %v", rest, err)
	}

	t.Run("fields", func(t *testing.T) { checkUnifiedTokenFields(t, pg) })
}

func checkUnifiedTokenFields(t *testing.T, pg *PostgresDB) {
	t.Helper()

	ctx := context.Background()
	byName := map[string]*models.TokenListItem{}

	all, _, _ := pg.ListUnifiedTokens(ctx, models.TokenListParams{Admin: true, Page: 1, Limit: 50})
	for _, it := range all {
		byName[it.Name] = it
	}

	if p := byName["acme-deploy"]; p.Type != "project" || p.Project != "acme" || p.Role != "developer" || p.Owner != "lee" || p.ExpiresAt == nil {
		t.Errorf("project token = %+v", p)
	}

	if p := byName["dana-ci"]; p.Type != "personal" || p.Owner != "dana" || !slices.Equal(p.Scopes, []string{"pt-1"}) || p.Project != "" {
		t.Errorf("personal token = %+v", p)
	}

	if p := byName["lee-laptop"]; p.Scopes == nil || len(p.Scopes) != 0 {
		t.Errorf("a token without scopes must decode to an empty list, got %#v", p.Scopes)
	}
}

func TestListQuotaOverview(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	platformSeed(t, pg)
	ctx := context.Background()

	items, total, err := pg.ListQuotaOverview(ctx, models.QuotaListParams{Page: 1, Limit: 50})
	if err != nil {
		t.Fatalf("ListQuotaOverview: %v", err)
	}

	byKey := map[string]*models.QuotaOverview{}
	for _, it := range items {
		byKey[it.Scope+"/"+it.Name] = it
	}

	if total != len(items) || byKey["repository/r1"] == nil || byKey["repository/r2"] == nil || byKey["project/acme"] == nil || byKey["project/beta"] == nil {
		t.Fatalf("total %d, items %v", total, byKey)
	}

	if r := byKey["repository/r1"]; r.Project != "acme" || r.MaxSizeBytes != 100 || r.UsedBytes != 150 || r.ArtifactCount != 3 {
		t.Errorf("r1 = %+v", r)
	}

	// Project usage is the sum of its repositories' counters; the ceiling comes from project_quotas.
	if p := byKey["project/acme"]; p.MaxSizeBytes != 1000 || p.UsedBytes != 150 {
		t.Errorf("acme = %+v", p)
	}

	if p := byKey["project/beta"]; p.MaxSizeBytes != 0 || p.UsedBytes != 40 {
		t.Errorf("beta = %+v", p)
	}

	checkQuotaOverviewFilters(t, pg, items)
}

func checkQuotaOverviewFilters(t *testing.T, pg *PostgresDB, items []*models.QuotaOverview) {
	t.Helper()

	ctx := context.Background()

	// Largest usage first.
	if items[0].UsedBytes < items[len(items)-1].UsedBytes {
		t.Errorf("not ordered by usage: first %d, last %d", items[0].UsedBytes, items[len(items)-1].UsedBytes)
	}

	limited, _, err := pg.ListQuotaOverview(ctx, models.QuotaListParams{LimitedOnly: true, Page: 1, Limit: 50})
	if err != nil {
		t.Fatalf("limited: %v", err)
	}

	if len(limited) != 2 {
		t.Errorf("limited quotas = %d, want r1 and acme", len(limited))
	}

	repos, total, err := pg.ListQuotaOverview(ctx, models.QuotaListParams{Scope: models.QuotaScopeRepository, Query: "R2", Page: 1, Limit: 50})
	if err != nil || total != 1 || len(repos) != 1 || repos[0].Name != "r2" {
		t.Errorf("scope+q = %v (total %d), %v", repos, total, err)
	}

	page2, total2, err := pg.ListQuotaOverview(ctx, models.QuotaListParams{Page: 2, Limit: 3})
	if err != nil || len(page2) != len(items)-3 {
		t.Errorf("page 2 = %d items (total %d), %v", len(page2), total2, err)
	}
}

func TestListProjectStats(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	f := platformSeed(t, pg)

	stats, err := pg.ListProjectStats(context.Background(), []string{f.acmeID, f.betaID})
	if err != nil {
		t.Fatalf("ListProjectStats: %v", err)
	}

	if s := stats[f.acmeID]; s == nil || s.MemberCount != 2 || s.SizeBytes != 150 || s.MaxSizeBytes != 1000 {
		t.Errorf("acme = %+v", s)
	}

	if s := stats[f.betaID]; s == nil || s.MemberCount != 0 || s.SizeBytes != 40 || s.MaxSizeBytes != 0 {
		t.Errorf("beta = %+v", s)
	}

	if got, err := pg.ListProjectStats(context.Background(), nil); err != nil || len(got) != 0 {
		t.Errorf("no ids = %v, %v", got, err)
	}
}

func TestGetRepositoryUsage(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	f := platformSeed(t, pg)
	ctx := context.Background()
	db := pg.GetDB()

	// Every format branch of the query must run against the real schema, empty or not.
	empty, err := pg.GetRepositoryUsage(ctx, f.r1)
	if err != nil {
		t.Fatalf("GetRepositoryUsage (empty): %v", err)
	}

	if empty.PackageCount != 0 || empty.VersionCount != 0 || empty.LastPublishedAt != nil {
		t.Errorf("empty repository = %+v", empty)
	}

	var blobID string
	if err := db.QueryRowContext(ctx, `INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES (repeat('a', 64), 10, 'k') RETURNING id`).Scan(&blobID); err != nil {
		t.Fatalf("insert blob: %v", err)
	}

	seed(t, db, `INSERT INTO raw_artifacts (repository_id, path, blob_id, created_at) VALUES
		($1, 'a.txt', $2, '2026-01-01T00:00:00Z'), ($1, 'b.txt', $2, '2026-02-01T00:00:00Z'), ($1, 'gone.txt', $2, '2026-03-01T00:00:00Z'),
		($3, 'other.txt', $2, '2026-04-01T00:00:00Z')`, f.r1, blobID, f.r2)
	seed(t, db, `UPDATE raw_artifacts SET deleted_at = now() WHERE path = 'gone.txt'`)

	usage, err := pg.GetRepositoryUsage(ctx, f.r1)
	if err != nil {
		t.Fatalf("GetRepositoryUsage: %v", err)
	}

	want := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if usage.PackageCount != 2 || usage.VersionCount != 2 || usage.LastPublishedAt == nil || !usage.LastPublishedAt.Equal(want) {
		t.Errorf("usage = %+v (last %v), want 2 packages, 2 versions, last %v (soft-deleted and other-repository rows excluded)", usage, usage.LastPublishedAt, want)
	}
}

func seed(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()

	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("seed %q: %v", q, err)
	}
}

func TestResolveAuditNames(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	f := platformSeed(t, pg)
	db := pg.GetDB()

	var providerID, groupID string
	if err := db.QueryRowContext(context.Background(),
		`INSERT INTO auth_sso_providers (slug, kind, display_name, oidc_config) VALUES ('corp', 'oidc', 'Corp', '{}') RETURNING id`).Scan(&providerID); err != nil {
		t.Fatalf("insert provider: %v", err)
	}

	if err := db.QueryRowContext(context.Background(), `INSERT INTO auth_groups (name) VALUES ('eng') RETURNING id`).Scan(&groupID); err != nil {
		t.Fatalf("insert group: %v", err)
	}

	missing := "99999999-9999-4999-8999-999999999999"

	names, err := pg.ResolveAuditNames(context.Background(), models.AuditNameRefs{
		UserIDs: []string{f.dana, missing}, GroupIDs: []string{groupID}, RepositoryIDs: []string{f.r1},
		ProjectIDs: []string{f.acmeID}, SSOProviderIDs: []string{providerID},
	})
	if err != nil {
		t.Fatalf("ResolveAuditNames: %v", err)
	}

	checks := map[string]string{
		"user":     names.Users[f.dana],
		"group":    names.Groups[groupID],
		"repo":     names.Repositories[f.r1],
		"project":  names.Projects[f.acmeID],
		"provider": names.SSOProviders[providerID],
	}

	for k, want := range map[string]string{"user": "dana", "group": "eng", "repo": "r1", "project": "acme", "provider": "corp"} {
		if checks[k] != want {
			t.Errorf("%s name = %q, want %q", k, checks[k], want)
		}
	}

	if _, ok := names.Users[missing]; ok {
		t.Error("an unknown id must be absent, not named")
	}

	if empty, err := pg.ResolveAuditNames(context.Background(), models.AuditNameRefs{}); err != nil || empty == nil {
		t.Errorf("empty refs = %v, %v", empty, err)
	}
}

func TestAuditListFiltersByRepositoryAndPackage(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	f := platformSeed(t, pg)
	db := pg.GetDB()

	seed(t, db, `INSERT INTO audit_logs (action, repository_id, details_json, created_at) VALUES
		('artifact.deleted', $1, '{"package":"left-pad","version":"1.0.0"}', now() - interval '3 minutes'),
		('artifact.deleted', $1, '{"package":"left-pad","version":"2.0.0"}', now() - interval '2 minutes'),
		('artifact.deleted', $1, '{"package":"right-pad","version":"1.0.0"}', now() - interval '1 minute'),
		('artifact.deleted', $2, '{"package":"left-pad","version":"1.0.0"}', now())`, f.r1, f.r2)

	count := func(p models.AuditListParams) int {
		t.Helper()

		p.Limit, p.NoTotal = 50, true

		page, err := pg.ListAuditLogs(context.Background(), p)
		if err != nil {
			t.Fatalf("ListAuditLogs(%+v): %v", p, err)
		}

		return len(page.Logs)
	}

	for name, tc := range map[string]struct {
		p    models.AuditListParams
		want int
	}{
		"repository only":    {models.AuditListParams{RepositoryID: f.r1}, 3},
		"other repository":   {models.AuditListParams{RepositoryID: f.r2}, 1},
		"package":            {models.AuditListParams{RepositoryID: f.r1, PackageName: "left-pad"}, 2},
		"package and ver":    {models.AuditListParams{RepositoryID: f.r1, PackageName: "left-pad", PackageVersion: "2.0.0"}, 1},
		"version only":       {models.AuditListParams{RepositoryID: f.r1, PackageVersion: "1.0.0"}, 2},
		"unknown package":    {models.AuditListParams{RepositoryID: f.r1, PackageName: "nope"}, 0},
		"no repository rows": {models.AuditListParams{RepositoryID: "99999999-9999-4999-8999-999999999999"}, 0},
	} {
		if got := count(tc.p); got != tc.want {
			t.Errorf("%s: %d rows, want %d", name, got, tc.want)
		}
	}
}

func TestListEnabledSSOProvidersSkipsDisabled(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	db := pg.GetDB()

	seed(t, db, `INSERT INTO auth_sso_providers (slug, kind, display_name, enabled, oidc_config) VALUES
		('on', 'oidc', 'On', true, '{}'), ('off', 'oidc', 'Off', false, '{}')`)

	enabled, err := pg.ListEnabledSSOProviders(context.Background())
	if err != nil {
		t.Fatalf("ListEnabledSSOProviders: %v", err)
	}

	if len(enabled) != 1 || enabled[0].Slug != "on" {
		t.Errorf("enabled providers = %+v, want only 'on'", enabled)
	}

	all, err := pg.ListAllSSOProviders(context.Background())
	if err != nil || len(all) != 2 {
		t.Errorf("all providers = %d, %v", len(all), err)
	}
}
