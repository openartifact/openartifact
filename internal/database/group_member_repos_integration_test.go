// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

// seedGroup creates a group with n members (priorities n-1 .. 0, so the
// priority order is the reverse of the creation order) and returns the group id
// and the member ids in priority order.
func seedGroup(tb testing.TB, pg *PostgresDB, n int) (string, []string) {
	tb.Helper()

	ctx := context.Background()
	group := newQuotaSeeder(tb, pg, "grp-repos", "raw")
	byPriority := make([]string, n)

	for i := range n {
		m := newQuotaSeeder(tb, pg, fmt.Sprintf("member-%d", i), "raw")
		if err := pg.AddGroupMember(ctx, group.repo, m.repo, n-1-i); err != nil {
			tb.Fatal(err)
		}

		byPriority[n-1-i] = m.repo
	}

	return group.repo, byPriority
}

// ListGroupMemberRepositories returns the same repositories, in the same order,
// as ListGroupMembers followed by GetRepositoryByID, and leaves out members in
// the trash.
func TestListGroupMemberRepositoriesMatchesPerMemberLookup(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	group, byPriority := seedGroup(t, pg, 5)

	got, err := pg.ListGroupMemberRepositories(ctx, group)
	if err != nil {
		t.Fatalf("ListGroupMemberRepositories: %v", err)
	}

	ids := make([]string, len(got))
	for i, r := range got {
		ids[i] = r.ID

		want, err := pg.GetRepositoryByID(ctx, r.ID)
		if err != nil || want == nil {
			t.Fatalf("GetRepositoryByID(%s) = %v, %v", r.ID, want, err)
		}

		if !reflect.DeepEqual(r, want) {
			t.Fatalf("repository differs from GetRepositoryByID:\n got  %+v\n want %+v", *r, *want)
		}
	}

	if !slices.Equal(ids, byPriority) {
		t.Fatalf("member order = %v, want priority order %v", ids, byPriority)
	}

	if err := pg.DeleteRepository(ctx, byPriority[1]); err != nil {
		t.Fatalf("DeleteRepository: %v", err)
	}

	got, err = pg.ListGroupMemberRepositories(ctx, group)
	if err != nil {
		t.Fatalf("ListGroupMemberRepositories: %v", err)
	}

	remaining := make([]string, 0, len(got))
	for _, r := range got {
		remaining = append(remaining, r.ID)
	}

	if want := slices.Delete(slices.Clone(byPriority), 1, 2); !slices.Equal(remaining, want) {
		t.Fatalf("after deleting a member: %v, want %v", remaining, want)
	}
}

// BenchmarkGroupMembers compares the per-member lookup the group handler used
// (ListGroupMembers + one GetRepositoryByID per member) with the single query.
func BenchmarkGroupMembers(b *testing.B) {
	pg := newQuotaTestDB(b, 4)
	ctx := context.Background()
	group, _ := seedGroup(b, pg, 8)

	b.Run("per-member", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			links, err := pg.ListGroupMembers(ctx, group)
			if err != nil {
				b.Fatal(err)
			}

			for _, l := range links {
				if _, err := pg.GetRepositoryByID(ctx, l.MemberRepoID); err != nil {
					b.Fatal(err)
				}
			}
		}
	})

	b.Run("one-query", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			if _, err := pg.ListGroupMemberRepositories(ctx, group); err != nil {
				b.Fatal(err)
			}
		}
	})
}
