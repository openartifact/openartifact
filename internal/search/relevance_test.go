// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestRelevanceTermPrefersNameQualifier(t *testing.T) {
	for name, tc := range map[string]struct {
		filter SearchFilter
		want   string
	}{
		"free text":      {SearchFilter{FreeText: "lodash"}, "lodash"},
		"name qualifier": {SearchFilter{Name: "lodash", FreeText: "other"}, "lodash"},
		"no term":        {SearchFilter{Formats: []string{"npm"}}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := relevanceTerm(tc.filter); got != tc.want {
				t.Errorf("relevanceTerm = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRelevanceExprWithoutTermRanksEverythingEqually(t *testing.T) {
	expr, args := relevanceExpr("s.name", "", 3)

	// Cast, because the expression is also an ORDER BY item, where a bare 0
	// would be read as a column position.
	if expr != "0::int" || len(args) != 0 {
		t.Errorf("got %q %v, want a constant rank and no arguments", expr, args)
	}
}

func TestRelevanceExprBindsEscapedTermAndPrefix(t *testing.T) {
	expr, args := relevanceExpr("s.name", `a_b%`, 7)

	if want := []any{`a\_b\%`, `a\_b\%%`}; !slices.Equal(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}

	for _, want := range []string{"s.name ILIKE $7 THEN 0", "s.name ILIKE $8 THEN 1", "ELSE 2"} {
		if !strings.Contains(expr, want) {
			t.Errorf("expr %q does not contain %q", expr, want)
		}
	}

	if strings.Contains(expr, "substring") {
		t.Errorf("a repository name has no segments, got %q", expr)
	}
}

func TestPackageRankingWithoutTermRanksEverythingEqually(t *testing.T) {
	r := packageRanking("name", "", 3, "")

	if r.expr != "0::int" || r.cols != "" || r.names != "" || len(r.args) != 0 {
		t.Errorf("got %+v, want a constant rank, no columns and no arguments", r)
	}
}

// The rank binds the raw term once: it is lowered in SQL, with the same
// lower() as the name, and compared with = and starts_with, which take no
// pattern to escape.
func TestPackageRankingBindsTermOnce(t *testing.T) {
	r := packageRanking("s.name", `a_b%`, 7, "")

	if want := []any{`a_b%`}; !slices.Equal(r.args, want) {
		t.Errorf("args = %v, want %v", r.args, want)
	}

	if !strings.Contains(r.cols, "lower($7::text) AS lt") || strings.Contains(r.cols, "$8") {
		t.Errorf("columns %q must bind the term as $7 and nothing else", r.cols)
	}

	if strings.Contains(r.expr, "ILIKE") || strings.Contains(r.expr, "$") {
		t.Errorf("rank %q must read the columns, not the arguments or ILIKE", r.expr)
	}
}

// A qualified name ranks by its last segment too, cut once per row and only
// for names that have a separator.
func TestPackageRankingSegmentsMatchQualifiedNames(t *testing.T) {
	r := packageRanking("s.name", "lodash", 1, "")

	if n := strings.Count(r.cols, `substring(lower(s.name) from '[^/:]*$')`); n != 1 {
		t.Errorf("the segment must be cut once per row, got %d in %q", n, r.cols)
	}

	for _, want := range []string{"strpos(s.name, '/') > 0", "strpos(s.name, ':') > 0", "lower(s.name) AS ln"} {
		if !strings.Contains(r.cols, want) {
			t.Errorf("columns %q do not contain %q", r.cols, want)
		}
	}

	for _, want := range []string{"ln = lt OR tl = lt THEN 0", "starts_with(ln, lt) OR starts_with(tl, lt) THEN 1", "ELSE 2"} {
		if !strings.Contains(r.expr, want) {
			t.Errorf("rank %q does not contain %q", r.expr, want)
		}
	}

	if r.names != ", lt, ln, tl" {
		t.Errorf("names = %q, want the three rank columns", r.names)
	}
}

func TestPackageRankingRankIfSkipsOtherRows(t *testing.T) {
	r := packageRanking("name", "x", 1, "count(*) OVER () <= 10")

	for _, want := range []string{
		"CASE WHEN count(*) OVER () <= 10 THEN lower($1::text) END AS lt",
		"CASE WHEN count(*) OVER () <= 10 THEN lower(name) END AS ln",
		"CASE WHEN (count(*) OVER () <= 10) AND (strpos(name",
	} {
		if !strings.Contains(r.cols, want) {
			t.Errorf("columns %q do not contain %q", r.cols, want)
		}
	}
}

// The rank columns are computed in the match, once per row and only below the
// cap, and the page reads them: no other ranking work in the page.
func TestSearchQueryComputesRankColumnsOnce(t *testing.T) {
	readable := map[uuid.UUID]struct{}{uuid.New(): {}}

	for name, opts := range map[string]PageOptions{"flat": {}, "grouped": {GroupByPackage: true}} {
		t.Run(name, func(t *testing.T) {
			q, _ := buildSearchQuery(SearchFilter{FreeText: "lodash"}, 25, 0, readable, true, opts)

			if n := strings.Count(q, "AS ln"); n != 1 {
				t.Errorf("ln computed %d times, want once:\n%s", n, q)
			}

			if strings.Contains(q, "LATERAL") || strings.Contains(q, "ILIKE $3") {
				t.Errorf("the page must rank from the match's columns:\n%s", q)
			}
		})
	}
}

func TestSearchPageRanksBeforeRecencyAndBindsRankArguments(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}

	defer func() { _ = db.Close() }()

	// where: name, readable; rank: the term; then limit and offset.
	mock.ExpectQuery(`(?s)ORDER BY relevance, created_at DESC, id.*ORDER BY p\.relevance, p\.created_at DESC, p\.id`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), `lodash`, 5, 0).
		WillReturnRows(sqlmock.NewRows(pageCols()))

	readable := map[uuid.UUID]struct{}{uuid.New(): {}}
	if _, _, err := NewExecutor(db).SearchPage(context.Background(), SearchFilter{FreeText: "lodash"}, PageOptions{}, 5, 0, readable); err != nil {
		t.Fatalf("SearchPage: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestSearchPageGroupsByPackage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}

	defer func() { _ = db.Close() }()

	repoID := uuid.New()
	mock.ExpectQuery(`(?s)latest AS \(\s*SELECT DISTINCT ON \(repository_id, format, name\).*ORDER BY repository_id, format, name, created_at DESC, id`).
		WillReturnRows(sqlmock.NewRows(pageCols()).
			AddRow(repoID.String(), "repo", "npm", "lodash", "4.17.21", "", "", int64(0), time.Now(), nil, nil, nil, int64(3)))

	results, total, err := NewExecutor(db).SearchPage(context.Background(), SearchFilter{FreeText: "lodash"}, PageOptions{GroupByPackage: true}, 8, 0, map[uuid.UUID]struct{}{repoID: {}})
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}

	if len(results) != 1 || total != 3 {
		t.Errorf("got %d results, total %d; want 1 result and the package total 3", len(results), total)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A grouped page past the last package asks for the package count, not the row count.
func TestSearchPageGroupedCountsPackagesPastTheEnd(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}

	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`WITH matched`).WillReturnRows(sqlmock.NewRows(pageCols()))
	mock.ExpectQuery(`(?s)SELECT DISTINCT repository_id, format, name FROM m`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))

	_, total, err := NewExecutor(db).SearchPage(context.Background(), SearchFilter{FreeText: "x"}, PageOptions{GroupByPackage: true}, 8, 40, map[uuid.UUID]struct{}{uuid.New(): {}})
	if err != nil || total != 4 {
		t.Fatalf("total %d, err %v; want 4, nil", total, err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestSearchRepositoriesRanksExactThenPrefixThenName(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}

	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`(?s)ORDER BY CASE WHEN r\.name ILIKE \$3 THEN 0 WHEN r\.name ILIKE \$4 THEN 1 ELSE 2 END, r\.name, r\.id\s+LIMIT \$5 OFFSET \$6`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), `ui-kit`, `ui-kit%`, 8, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "format", "description"}))

	readable := map[uuid.UUID]struct{}{uuid.New(): {}}
	if _, err := NewExecutor(db).SearchRepositories(context.Background(), SearchFilter{FreeText: "ui-kit"}, 8, 0, readable); err != nil {
		t.Fatalf("SearchRepositories: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func pageCols() []string {
	return []string{"repository_id", "repository", "format", "name", "version", "path", "sha256", "size", "created_at", "group_id", "artifact_id_coord", "scope", "total"}
}
