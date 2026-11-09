// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !nomcp

package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/search"
)

// recordingSearcher answers with one hit per readable repository and remembers the
// readable set and the filter it was given.
type recordingSearcher struct {
	readable map[uuid.UUID]struct{}
	filter   search.SearchFilter
	total    int64
}

func (r *recordingSearcher) SearchPage(
	_ context.Context, filter search.SearchFilter, _ search.PageOptions, _, _ int, readable map[uuid.UUID]struct{},
) ([]search.SearchResult, int64, error) {
	r.readable, r.filter = readable, filter

	out := make([]search.SearchResult, 0, len(readable))
	for id := range readable {
		out = append(out, search.SearchResult{Repository: id.String(), Name: "left-pad", Format: "npm"})
	}

	total := r.total
	if total == 0 {
		total = int64(len(out))
	}

	return out, total, nil
}

// fakeProjectScope keeps the repositories in keep and records the filter.
type fakeProjectScope struct {
	keep   map[uuid.UUID]struct{}
	uses   bool
	calls  int
	filter search.SearchFilter
}

func (f *fakeProjectScope) NarrowToProjects(
	_ context.Context, readable map[uuid.UUID]struct{}, filter search.SearchFilter,
) (map[uuid.UUID]struct{}, error) {
	f.calls++
	f.filter = filter

	kept := map[uuid.UUID]struct{}{}
	for id := range readable {
		if _, ok := f.keep[id]; ok {
			kept[id] = struct{}{}
		}
	}

	return kept, nil
}

func (f *fakeProjectScope) UsesProjects(context.Context) (bool, error) { return f.uses, nil }

// projectSearchHarness is the fixture with UUID repository ids, so they reach the
// readable set, and a recording searcher.
func projectSearchHarness(t *testing.T) (*harness, *recordingSearcher, []uuid.UUID) {
	t.Helper()

	db := fixtureDB()
	ids := make([]uuid.UUID, 0, len(db.repos))

	for _, r := range db.repos {
		id := uuid.New()
		r.ID = id.String()
		ids = append(ids, id)
	}

	h := newHarness(t, db)
	rs := &recordingSearcher{}
	h.s.SetSearcher(rs)

	return h, rs, ids
}

// TestSearchArtifactsAppliesProjectFilters: search_artifacts narrows by project: and
// project_role: like GET /api/v1/search, instead of parsing them away and searching
// every readable repository.
func TestSearchArtifactsAppliesProjectFilters(t *testing.T) {
	h, rs, ids := projectSearchHarness(t)
	scope := &fakeProjectScope{keep: map[uuid.UUID]struct{}{ids[0]: {}}}
	h.s.SetProjectScope(scope)

	if text, isErr := h.call(t, "search_artifacts", map[string]any{"query": "pad project:acme project_role:viewer"}); isErr {
		t.Fatalf("tool error: %s", text)
	}

	if scope.calls != 1 || !slices.Equal(scope.filter.Projects, []string{"acme"}) ||
		!slices.Equal(scope.filter.ProjectRoles, []string{"viewer"}) {
		t.Fatalf("scope calls=%d filter=%+v, want one call with project acme and role viewer", scope.calls, scope.filter)
	}

	if _, ok := rs.readable[ids[0]]; !ok || len(rs.readable) != 1 {
		t.Errorf("searched %d repositories, want only the one the scope kept", len(rs.readable))
	}

	scope.calls = 0

	if _, isErr := h.call(t, "search_artifacts", map[string]any{"query": "pad"}); isErr {
		t.Fatal("tool error without project filters")
	}

	if scope.calls != 0 || len(rs.readable) != len(ids) {
		t.Errorf("without project filters: scope calls=%d, searched %d of %d repositories", scope.calls, len(rs.readable), len(ids))
	}
}

// TestSearchArtifactsWithoutProjectScopeFindsNothing: if the scope is not installed,
// a project-filtered search is empty rather than unscoped.
func TestSearchArtifactsWithoutProjectScopeFindsNothing(t *testing.T) {
	h, rs, _ := projectSearchHarness(t)

	if _, isErr := h.call(t, "search_artifacts", map[string]any{"query": "pad project:acme"}); isErr {
		t.Fatal("tool error")
	}

	if len(rs.readable) != 0 {
		t.Errorf("searched %d repositories without a project scope, want none", len(rs.readable))
	}
}

// TestSearchArtifactsTooBroadHint: project:<project> is suggested only to callers
// that hold a project role.
func TestSearchArtifactsTooBroadHint(t *testing.T) {
	for _, uses := range []bool{true, false} {
		h, rs, _ := projectSearchHarness(t)
		rs.total = search.MaxSearchResults + 1
		h.s.SetProjectScope(&fakeProjectScope{uses: uses})

		text, isErr := h.call(t, "search_artifacts", map[string]any{"query": "pad"})
		if !isErr || !strings.Contains(text, "repo:<repository>") {
			t.Fatalf("uses=%v: got (%q, %v), want the too broad error", uses, text, isErr)
		}

		if strings.Contains(text, "project:<project>") != uses {
			t.Errorf("uses=%v: message %q, project:<project> suggested = %v", uses, text, !uses)
		}
	}
}
