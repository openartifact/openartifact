// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/search"
)

// rankingFixture: helm charts in repository lib (readable by alice) and in
// hidden (readable by nobody but the admin), with creation times chosen so
// that recency alone would order them differently from relevance.
type rankingFixture struct {
	h     *search.Handler
	admin *auth.Principal
	alice *auth.Principal
	bob   *auth.Principal
}

func newRankingFixture(t *testing.T) rankingFixture {
	t.Helper()

	pg := freshPostgres(t, 5)
	ctx := context.Background()

	newSearchRepo(t, pg, "lib", "helm", "", false)
	newSearchRepo(t, pg, "hidden", "helm", "", false)

	blobID, err := pg.UpsertBlob(ctx, componentsBlobSHA, componentsBlobSHA, 5)
	if err != nil {
		t.Fatalf("UpsertBlob: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	tied := now.Add(-2 * time.Hour)

	for _, c := range []struct {
		repo, name, version string
		at                  time.Time
	}{
		{"lib", "widget", "1.0.0", now.Add(-72 * time.Hour)},
		{"lib", "widget", "2.0.0", now.Add(-48 * time.Hour)},
		{"lib", "widget", "1.1.0", now.Add(-24 * time.Hour)}, // the latest by creation time
		{"lib", "widget-extra", "1.0.0", now.Add(-time.Hour)},
		{"lib", "widget-a", "1.0.0", tied},
		{"lib", "widget-b", "1.0.0", tied}, // same instant as widget-a: the id decides
		{"lib", "my-widget", "1.0.0", now.Add(-time.Minute)},
		{"hidden", "widget", "9.9.9", now},
	} {
		execSQL(t, pg, `INSERT INTO helm_charts (repository_id, name, version, blob_id, created_at)
			VALUES ((SELECT id FROM repositories WHERE name = $1), $2, $3, $4, $5)`, c.repo, c.name, c.version, blobID, c.at)
	}

	log := newTestLogger(t)

	return rankingFixture{
		h:     search.NewHandler(pg.GetDB(), auth.NewEvaluator(pg, log, false), log),
		admin: &auth.Principal{Username: "admin", IsLegacy: true},
		alice: searchUser(t, pg, "alice", "lib"),
		bob:   searchUser(t, pg, "bob", ""),
	}
}

func (f rankingFixture) get(t *testing.T, who *auth.Principal, params url.Values) searchResponse {
	t.Helper()

	ctx := context.Background()
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/search?"+params.Encode(), nil)
	r = r.WithContext(middleware.WithPrincipal(r.Context(), who))
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("GET search?%s: want 200, got %d body=%s", params.Encode(), w.Code, w.Body.String())
	}

	var resp searchResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	return resp
}

func (r searchResponse) nameVersions() []string {
	out := make([]string, 0, len(r.Packages))
	for _, p := range r.Packages {
		out = append(out, p.Name+"@"+p.Version)
	}

	return out
}

func query(q string, extra ...string) url.Values {
	v := url.Values{"q": {q}}
	for i := 0; i+1 < len(extra); i += 2 {
		v.Set(extra[i], extra[i+1])
	}

	return v
}

// allPages walks the result list page by page and returns the concatenation.
func (f rankingFixture) allPages(t *testing.T, who *auth.Principal, q string, pageSize int, group string) []string {
	t.Helper()

	var all []string

	for offset := 0; offset < 50; offset += pageSize {
		params := query(q, "limit", strconv.Itoa(pageSize), "offset", strconv.Itoa(offset))
		if group != "" {
			params.Set("group", group)
		}

		page := f.get(t, who, params).nameVersions()
		if len(page) == 0 {
			break
		}

		all = append(all, page...)
	}

	return all
}

func TestSearchRanksExactThenPrefixThenSubstringThenRecency(t *testing.T) {
	f := newRankingFixture(t)

	for _, term := range []string{"widget", "WIDGET", "name:widget"} {
		got := f.get(t, f.alice, query(term)).nameVersions()

		// Exact name first (newest version first), then names starting with
		// it (newest first, id breaks the tie), then names merely containing it.
		if want := []string{"widget@1.1.0", "widget@2.0.0", "widget@1.0.0", "widget-extra@1.0.0"}; !slices.Equal(got[:4], want) {
			t.Errorf("%q: first four = %v, want %v", term, got[:4], want)
		}

		if got[4][:8] != "widget-a" && got[4][:8] != "widget-b" || got[5][:8] != "widget-a" && got[5][:8] != "widget-b" || got[4] == got[5] {
			t.Errorf("%q: tied prefix matches = %v", term, got[4:6])
		}

		if len(got) != 7 || got[6] != "my-widget@1.0.0" {
			t.Errorf("%q: got %v, want the substring match last of 7", term, got)
		}
	}
}

func TestSearchPaginationIsStable(t *testing.T) {
	f := newRankingFixture(t)

	for _, group := range []string{"", "package"} {
		full := f.get(t, f.alice, func() url.Values {
			v := query("widget")
			if group != "" {
				v.Set("group", group)
			}

			return v
		}()).nameVersions()

		for _, size := range []int{1, 2, 3} {
			if got := f.allPages(t, f.alice, "widget", size, group); !slices.Equal(got, full) {
				t.Errorf("group=%q pages of %d: %v, want %v", group, size, got, full)
			}
		}
	}
}

func TestSearchGroupsPackagesToTheirLatestVersion(t *testing.T) {
	f := newRankingFixture(t)

	resp := f.get(t, f.alice, query("widget", "group", "package"))
	got := resp.nameVersions()

	if len(got) != 5 || got[0] != "widget@1.1.0" || got[1] != "widget-extra@1.0.0" || got[4] != "my-widget@1.0.0" {
		t.Errorf("grouped = %v, want widget@1.1.0, widget-extra, widget-a/b, my-widget", got)
	}

	if resp.Total != 5 {
		t.Errorf("grouped total = %d, want the 5 packages, not the 7 versions", resp.Total)
	}

	if ungrouped := f.get(t, f.alice, query("widget")); ungrouped.Total != 7 {
		t.Errorf("ungrouped total = %d, want 7 versions", ungrouped.Total)
	}

	// Asking past the last package still reports the package total.
	if past := f.get(t, f.alice, query("widget", "group", "package", "offset", "40")); past.Total != 5 || len(past.Packages) != 0 {
		t.Errorf("past the end: total %d, %d packages; want 5 and none", past.Total, len(past.Packages))
	}
}

// Without a name to rank by (a format: only query) nothing is ranked and the
// newest packages come first, repositories alphabetically.
func TestSearchWithoutTermOrdersByRecency(t *testing.T) {
	f := newRankingFixture(t)

	for _, group := range []string{"", "package"} {
		params := query("format:helm")
		if group != "" {
			params.Set("group", group)
		}

		resp := f.get(t, f.alice, params)
		if len(resp.Packages) == 0 || resp.Packages[0].Name != "my-widget" {
			t.Errorf("group=%q: first package of format:helm = %v, want the newest, my-widget", group, resp.nameVersions())
		}

		if got := resp.repoNames(); !slices.Equal(got, []string{"lib"}) {
			t.Errorf("group=%q: repositories = %v, want [lib]", group, got)
		}
	}
}

// A query matching more than the cap is refused as too broad, grouped or not,
// and one just under it is served.
func TestSearchTooBroadQueryIsRefused(t *testing.T) {
	pg := freshPostgres(t, 5)

	newSearchRepo(t, pg, "bulk", "helm", "", false)

	blobID, err := pg.UpsertBlob(context.Background(), componentsBlobSHA, componentsBlobSHA, 5)
	if err != nil {
		t.Fatalf("UpsertBlob: %v", err)
	}

	// 2500 charts x 4 versions = 10000 rows named bulk-N (exactly the cap),
	// plus one more row for the 10001st.
	execSQL(t, pg, `INSERT INTO helm_charts (repository_id, name, version, blob_id)
		SELECT (SELECT id FROM repositories WHERE name = 'bulk'), 'bulk-' || c, v || '.0.0', $1
		FROM generate_series(1, 2500) c, generate_series(1, 4) v`, blobID)

	log := newTestLogger(t)
	h := search.NewHandler(pg.GetDB(), auth.NewEvaluator(pg, log, false), log)
	admin := &auth.Principal{Username: "admin", IsLegacy: true}

	status := func(params url.Values) (int, string) {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/search?"+params.Encode(), nil)
		r = r.WithContext(middleware.WithPrincipal(r.Context(), admin))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		return w.Code, w.Body.String()
	}

	for _, group := range []string{"", "package"} {
		params := query("bulk-")
		if group != "" {
			params.Set("group", group)
		}

		if code, body := status(params); code != http.StatusOK {
			t.Errorf("group=%q at exactly the cap: status %d %s, want 200", group, code, body)
		}

		execSQL(t, pg, `INSERT INTO helm_charts (repository_id, name, version, blob_id)
			VALUES ((SELECT id FROM repositories WHERE name = 'bulk'), 'bulk-extra-'||$2::text, '1.0.0', $1)`, blobID, group)

		code, body := status(params)
		if code != http.StatusBadRequest || !strings.Contains(body, "query_too_broad") {
			t.Errorf("group=%q above the cap: status %d %s, want 400 query_too_broad", group, code, body)
		}

		execSQL(t, pg, `DELETE FROM helm_charts WHERE name LIKE 'bulk-extra-%'`)
	}
}

func TestSearchRankingKeepsAccessControl(t *testing.T) {
	f := newRankingFixture(t)

	// The admin also sees hidden/widget@9.9.9, the newest exact match.
	if got := f.get(t, f.admin, query("widget", "group", "package")).nameVersions(); got[0] != "widget@9.9.9" && got[0] != "widget@1.1.0" {
		t.Errorf("admin grouped = %v", got)
	}

	for _, p := range f.get(t, f.alice, query("widget")).Packages {
		if p.Repository != "lib" {
			t.Errorf("alice sees %s/%s", p.Repository, p.Name)
		}
	}

	if resp := f.get(t, f.bob, query("widget", "group", "package")); len(resp.Packages) != 0 || resp.Total != 0 {
		t.Errorf("bob (no permissions) got %d packages, total %d", len(resp.Packages), resp.Total)
	}
}

func TestSearchRanksRepositoriesByNameRelevance(t *testing.T) {
	pg := freshPostgres(t, 5)

	for _, name := range []string{"my-widget", "widget-tools", "a-widget", "widget", "unrelated"} {
		newSearchRepo(t, pg, name, "raw", "", false)
	}

	log := newTestLogger(t)
	f := rankingFixture{h: search.NewHandler(pg.GetDB(), auth.NewEvaluator(pg, log, false), log), admin: &auth.Principal{Username: "admin", IsLegacy: true}}

	got := f.get(t, f.admin, query("widget")).repoNames()
	if want := []string{"widget", "widget-tools", "a-widget", "my-widget"}; !slices.Equal(got, want) {
		t.Errorf("repositories = %v, want exact, prefix, then substring by name: %v", got, want)
	}
}

// TestRepositoryNameSearchUsesTrigramIndex: with sequential scans priced out
// (a handful of rows would otherwise always be scanned), the ILIKE '%term%'
// the repository search runs is answered by idx_repositories_name_trgm.
func TestRepositoryNameSearchUsesTrigramIndex(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()

	newSearchRepo(t, pg, "widget", "raw", "", false)

	conn, err := pg.GetDB().Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}

	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatalf("SET: %v", err)
	}

	rows, err := conn.QueryContext(ctx, `EXPLAIN SELECT id FROM repositories WHERE name ILIKE '%widget%'`)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}

	defer func() { _ = rows.Close() }()

	var plan strings.Builder

	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan: %v", err)
		}

		plan.WriteString(line + "\n")
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("EXPLAIN rows: %v", err)
	}

	if !strings.Contains(plan.String(), "idx_repositories_name_trgm") {
		t.Errorf("plan does not use the trigram index:\n%s", plan.String())
	}
}
