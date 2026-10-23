// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestCountResults(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows := sqlmock.NewRows([]string{"count"}).AddRow(42)
	mock.ExpectQuery(`SELECT COUNT`).WillReturnRows(rows)

	exec := NewExecutor(db)
	readableRepos := map[uuid.UUID]struct{}{uuid.New(): {}}

	count, err := exec.CountResults(context.Background(), SearchFilter{FreeText: "test"}, readableRepos)
	if err != nil {
		t.Fatalf("CountResults: %v", err)
	}

	if count != 42 {
		t.Errorf("want 42, got %d", count)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCountResultsEmptyReadableReposReturnsZero(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	exec := NewExecutor(db)

	count, err := exec.CountResults(context.Background(), SearchFilter{FreeText: "test"}, nil)
	if err != nil {
		t.Fatalf("CountResults: %v", err)
	}

	if count != 0 {
		t.Errorf("want 0 for empty readableRepos, got %d", count)
	}

	// No query should have been issued at all.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestSearchEmptyReadableReposReturnsNoRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	exec := NewExecutor(db)

	results, err := exec.Search(context.Background(), SearchFilter{FreeText: "test"}, 25, 0, map[uuid.UUID]struct{}{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("want 0 results for empty readableRepos, got %d", len(results))
	}

	// No query should have been issued at all.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestSearchFreeTextQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	repoID := uuid.New()
	now := time.Now()
	cols := []string{"repository_id", "repository", "format", "name", "version", "path", "sha256", "size", "created_at", "group_id", "artifact_id_coord", "scope", "total"}
	rows := sqlmock.NewRows(cols).AddRow(repoID, "my-repo", "npm", "lodash", "4.17.21", "lodash-4.17.21.tgz", "abc123", int64(100), now, nil, nil, nil, int64(1))
	mock.ExpectQuery(`SELECT`).WillReturnRows(rows)

	exec := NewExecutor(db)
	readableRepos := map[uuid.UUID]struct{}{repoID: {}}

	results, err := exec.Search(context.Background(), SearchFilter{FreeText: "lodash"}, 25, 0, readableRepos)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}

	if results[0].Name != "lodash" {
		t.Errorf("want name=lodash, got %q", results[0].Name)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestSearchFormatFilter(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	repoID := uuid.New()
	now := time.Now()
	cols := []string{"repository_id", "repository", "format", "name", "version", "path", "sha256", "size", "created_at", "group_id", "artifact_id_coord", "scope", "total"}
	rows := sqlmock.NewRows(cols).AddRow(repoID, "npm-repo", "npm", "react", "18.0.0", "", "", int64(0), now, nil, nil, nil, int64(1))
	mock.ExpectQuery(`SELECT`).WillReturnRows(rows)

	exec := NewExecutor(db)
	readableRepos := map[uuid.UUID]struct{}{repoID: {}}
	filter := SearchFilter{Formats: []string{"npm"}}

	results, err := exec.Search(context.Background(), filter, 25, 0, readableRepos)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 1 || results[0].Format != "npm" {
		t.Errorf("expected 1 npm result, got %v", results)
	}
}

func TestSearchSHA256PrefixClause(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	repoID := uuid.New()
	now := time.Now()
	cols := []string{"repository_id", "repository", "format", "name", "version", "path", "sha256", "size", "created_at", "group_id", "artifact_id_coord", "scope", "total"}
	rows := sqlmock.NewRows(cols).AddRow(repoID, "repo", "rpm", "mypkg", "1.0", "", "deadbeef1234", int64(0), now, nil, nil, nil, int64(1))
	mock.ExpectQuery(`SELECT`).WillReturnRows(rows)

	exec := NewExecutor(db)
	readableRepos := map[uuid.UUID]struct{}{repoID: {}}

	results, err := exec.Search(context.Background(), SearchFilter{SHA256Prefix: "deadbeef"}, 25, 0, readableRepos)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 1 {
		t.Errorf("want 1, got %d", len(results))
	}
}

func TestSearchPostFilterRemovesUnreadableRepo(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	readableID := uuid.New()
	unreadableID := uuid.New()
	now := time.Now()
	cols := []string{"repository_id", "repository", "format", "name", "version", "path", "sha256", "size", "created_at", "group_id", "artifact_id_coord", "scope", "total"}
	rows := sqlmock.NewRows(cols).
		AddRow(readableID, "repo-a", "rpm", "pkg-a", "1.0", "", "", int64(0), now, nil, nil, nil, int64(2)).
		AddRow(unreadableID, "repo-b", "rpm", "pkg-b", "2.0", "", "", int64(0), now, nil, nil, nil, int64(2))
	mock.ExpectQuery(`SELECT`).WillReturnRows(rows)

	exec := NewExecutor(db)
	readableRepos := map[uuid.UUID]struct{}{readableID: {}}

	results, err := exec.Search(context.Background(), SearchFilter{Name: "pkg"}, 25, 0, readableRepos)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("want 1 after post-filter, got %d", len(results))
	}

	if results[0].Name != "pkg-a" {
		t.Errorf("want pkg-a, got %q", results[0].Name)
	}
}

func TestSearchMavenMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	repoID := uuid.New()
	now := time.Now()
	cols := []string{"repository_id", "repository", "format", "name", "version", "path", "sha256", "size", "created_at", "group_id", "artifact_id_coord", "scope", "total"}
	rows := sqlmock.NewRows(cols).AddRow(repoID, "maven-repo", "maven", "myapp", "1.0.0", "", "", int64(0), now, "com.example", "myapp", nil, int64(1))
	mock.ExpectQuery(`SELECT`).WillReturnRows(rows)

	exec := NewExecutor(db)
	readableRepos := map[uuid.UUID]struct{}{repoID: {}}

	results, err := exec.Search(context.Background(), SearchFilter{Formats: []string{"maven"}}, 25, 0, readableRepos)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("want 1, got %d", len(results))
	}

	if results[0].Metadata["groupId"] != "com.example" {
		t.Errorf("want groupId=com.example, got %q", results[0].Metadata["groupId"])
	}
}

func TestSearchRepositoriesIssuesNoQueryWithoutScope(t *testing.T) {
	tests := []struct {
		name     string
		filter   SearchFilter
		readable map[uuid.UUID]struct{}
	}{
		{"no readable repositories", SearchFilter{FreeText: "ui-kit"}, nil},
		{"sha256 identifies artifacts, not repositories", SearchFilter{SHA256Prefix: "deadbeef"}, map[uuid.UUID]struct{}{uuid.New(): {}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("open sqlmock: %v", err)
			}
			defer func() { _ = db.Close() }()

			results, err := NewExecutor(db).SearchRepositories(context.Background(), tc.filter, 25, 0, tc.readable)
			if err != nil {
				t.Fatalf("SearchRepositories: %v", err)
			}

			if len(results) != 0 {
				t.Errorf("want no repositories, got %v", results)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("a query was issued: %v", err)
			}
		})
	}
}

func TestSearchRepositoriesPostFilterRemovesUnreadableRepo(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	readableID, unreadableID := uuid.New(), uuid.New()
	rows := sqlmock.NewRows([]string{"id", "name", "format", "description"}).
		AddRow(readableID, "ui-kit", "npm", "shared components").
		AddRow(unreadableID, "ui-kit-secret", "npm", "")
	mock.ExpectQuery(`FROM repositories r`).WillReturnRows(rows)

	results, err := NewExecutor(db).SearchRepositories(context.Background(),
		SearchFilter{FreeText: "ui-kit"}, 25, 0, map[uuid.UUID]struct{}{readableID: {}})
	if err != nil {
		t.Fatalf("SearchRepositories: %v", err)
	}

	if len(results) != 1 || results[0].Name != "ui-kit" || results[0].Description != "shared components" {
		t.Errorf("want only the readable ui-kit repository, got %+v", results)
	}
}

func TestBuildWhereClauseTreatsLikeMetacharactersLiterally(t *testing.T) {
	repoID := uuid.New()

	where, args := buildWhereClause(SearchFilter{FreeText: `ui_kit%`, SHA256Prefix: `ab_`}, map[uuid.UUID]struct{}{repoID: {}})

	if want := `s.name ILIKE $1 AND s.sha256 LIKE $2 AND s.repository_id = ANY($3::uuid[])`; where != want {
		t.Errorf("where = %q, want %q", where, want)
	}

	if args[0] != `%ui\_kit\%%` || args[1] != `ab\_%` {
		t.Errorf("args = %q, %q; LIKE metacharacters must be escaped", args[0], args[1])
	}
}

func TestSearchPageReturnsTotalFromOneQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	repoID := uuid.New()
	cols := []string{"repository_id", "repository", "format", "name", "version", "path", "sha256", "size", "created_at", "group_id", "artifact_id_coord", "scope", "total"}
	rows := sqlmock.NewRows(cols).AddRow(repoID, "repo", "npm", "lodash", "1.0.0", "", "", int64(0), time.Now(), nil, nil, nil, int64(137))
	mock.ExpectQuery(`WITH matched AS MATERIALIZED .* count\(\*\) OVER \(\)`).WillReturnRows(rows)

	results, total, err := NewExecutor(db).SearchPage(context.Background(), SearchFilter{FreeText: "lodash"}, PageOptions{}, 1, 0, map[uuid.UUID]struct{}{repoID: {}})
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}

	if len(results) != 1 || total != 137 {
		t.Fatalf("got %d results, total %d; want 1 result, total 137", len(results), total)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// A page past the last match has no row to carry the total, so it is counted
// separately; page 0 with no rows is simply an empty result, no second query.
func TestSearchPageEmptyPageCounts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	cols := []string{"repository_id", "repository", "format", "name", "version", "path", "sha256", "size", "created_at", "group_id", "artifact_id_coord", "scope", "total"}
	mock.ExpectQuery(`WITH matched`).WillReturnRows(sqlmock.NewRows(cols))
	mock.ExpectQuery(`WITH matched`).WillReturnRows(sqlmock.NewRows(cols))
	mock.ExpectQuery(`SELECT COUNT`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(30))

	exec := NewExecutor(db)
	readable := map[uuid.UUID]struct{}{uuid.New(): {}}

	_, total, err := exec.SearchPage(context.Background(), SearchFilter{FreeText: "x"}, PageOptions{}, 25, 0, readable)
	if err != nil || total != 0 {
		t.Fatalf("first page: total %d, err %v; want 0, nil", total, err)
	}

	_, total, err = exec.SearchPage(context.Background(), SearchFilter{FreeText: "x"}, PageOptions{}, 25, 50, readable)
	if err != nil || total != 30 {
		t.Fatalf("page past the end: total %d, err %v; want 30, nil", total, err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
