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
	expr, args := relevanceExpr("s.name", "", 3, true)

	// Cast, because the expression is also an ORDER BY item, where a bare 0
	// would be read as a column position.
	if expr != "0::int" || len(args) != 0 {
		t.Errorf("got %q %v, want a constant rank and no arguments", expr, args)
	}
}

func TestRelevanceExprBindsEscapedTermAndPrefix(t *testing.T) {
	expr, args := relevanceExpr("s.name", `a_b%`, 7, false)

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

func TestRelevanceExprSegmentsMatchQualifiedNames(t *testing.T) {
	expr, _ := relevanceExpr("s.name", "lodash", 1, true)

	if strings.Count(expr, `substring(s.name from '[^/:]*$')`) != 2 {
		t.Errorf("exact and prefix arms must both look at the last segment, got %q", expr)
	}
}

func TestSearchPageRanksBeforeRecencyAndBindsRankArguments(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}

	defer func() { _ = db.Close() }()

	// where: name, readable; rank: exact, prefix; then limit and offset.
	mock.ExpectQuery(`(?s)ORDER BY relevance, created_at DESC, id.*ORDER BY p\.relevance, p\.created_at DESC, p\.id`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), `lodash`, `lodash%`, 5, 0).
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
