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

// fakeProjectScope keeps the repositories in keep, records the filter and counts how
// often the caller's roles are resolved.
type fakeProjectScope struct {
	keep      map[uuid.UUID]struct{}
	uses      bool
	calls     int
	usesCalls int
	filter    search.SearchFilter
}

func (f *fakeProjectScope) NarrowToProjects(
	_ context.Context, readable map[uuid.UUID]struct{}, filter search.SearchFilter,
) (map[uuid.UUID]struct{}, bool, error) {
	f.calls++
	f.filter = filter

	kept := map[uuid.UUID]struct{}{}
	for id := range readable {
		if _, ok := f.keep[id]; ok {
			kept[id] = struct{}{}
		}
	}

	return kept, f.uses, nil
}

func (f *fakeProjectScope) UsesProjects(context.Context) (bool, error) {
	f.usesCalls++

	return f.uses, nil
}

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

// TestSearchArtifactsTooBroadReusesNarrowedRoles: when the search was narrowed by
// project, the too-broad answer reuses the roles the narrowing resolved.
func TestSearchArtifactsTooBroadReusesNarrowedRoles(t *testing.T) {
	for _, uses := range []bool{true, false} {
		h, rs, ids := projectSearchHarness(t)
		rs.total = search.MaxSearchResults + 1
		scope := &fakeProjectScope{keep: map[uuid.UUID]struct{}{ids[0]: {}}, uses: uses}
		h.s.SetProjectScope(scope)

		text, isErr := h.call(t, "search_artifacts", map[string]any{"query": "pad project_role:viewer"})
		if !isErr || !strings.Contains(text, "query matches more than") {
			t.Fatalf("uses=%v: got (%q, %v), want the too broad error", uses, text, isErr)
		}

		if scope.calls != 1 || scope.usesCalls != 0 {
			t.Errorf("uses=%v: narrowed %d times, resolved roles again %d times; want 1 and 0", uses, scope.calls, scope.usesCalls)
		}

		if strings.Contains(text, "project:<project>") != uses {
			t.Errorf("uses=%v: message %q", uses, text)
		}
	}
}

// TestSearchArtifactsRejectsBadProjectFilters: the MCP tool refuses what
// GET /api/v1/search refuses with 400 invalid_filter, as a tool error the model can
// read, before any project lookup.
func TestSearchArtifactsRejectsBadProjectFilters(t *testing.T) {
	for name, query := range map[string]string{
		"unknown role":          "pad project_role:Viewer",
		"too many projects":     "pad" + strings.Repeat(" project:acme", 51),
		"too many project_role": "pad" + strings.Repeat(" project_role:viewer", 51),
	} {
		h, rs, _ := projectSearchHarness(t)
		scope := &fakeProjectScope{uses: true}
		h.s.SetProjectScope(scope)

		text, isErr := h.call(t, "search_artifacts", map[string]any{"query": query})
		if !isErr || !strings.Contains(text, "project") || strings.Contains(text, "internal error") {
			t.Errorf("%s: got (%q, %v), want a tool error naming the project filter", name, text, isErr)
		}

		if scope.calls != 0 || rs.readable != nil {
			t.Errorf("%s: scope called %d times, searched %v; want neither", name, scope.calls, rs.readable)
		}
	}
}

// TestSearchArtifactsPropFilters: search_artifacts passes prop: on to the search and
// refuses what GET /api/v1/search refuses, as a tool error, before searching.
func TestSearchArtifactsPropFilters(t *testing.T) {
	h, rs, _ := projectSearchHarness(t)

	if _, isErr := h.call(t, "search_artifacts", map[string]any{"query": "pad prop:status=approved"}); isErr {
		t.Fatal("tool error for a valid prop filter")
	}

	if !slices.Equal(rs.filter.Props, []string{"status=approved"}) {
		t.Errorf("searched props %v, want [status=approved]", rs.filter.Props)
	}

	for name, query := range map[string]string{
		"no =":      "pad prop:status",
		"empty key": "pad prop:=approved",
		"too many":  "pad" + strings.Repeat(" prop:k=v", 11),
	} {
		h, rs, _ := projectSearchHarness(t)

		text, isErr := h.call(t, "search_artifacts", map[string]any{"query": query})
		if !isErr || !strings.Contains(text, "prop") || strings.Contains(text, "internal error") {
			t.Errorf("%s: got (%q, %v), want a tool error about prop", name, text, isErr)
		}

		if rs.readable != nil {
			t.Errorf("%s: searched despite the bad filter", name)
		}
	}
}
