// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/auth"
)

func BenchmarkParseQuery(b *testing.B) {
	for _, tc := range []struct{ name, q string }{
		{"free_text", "lodash"},
		{"filters", "name:lodash format:npm repo:public sha256:abcdef0123"},
		{"mixed", "left pad format:npm format:pypi repo:a repo:b name:padding unknown:key extra words here"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				_ = ParseQuery(tc.q)
			}
		})
	}
}

func benchReadable(n int) map[uuid.UUID]struct{} {
	m := make(map[uuid.UUID]struct{}, n)

	for i := range n {
		m[uuid.NewSHA1(uuid.NameSpaceOID, fmt.Appendf(nil, "repo-%d", i))] = struct{}{}
	}

	return m
}

// BenchmarkBuildWhereClause builds the predicate + args shared by Search and
// CountResults; the readable-repository scope dominates as it grows.
func BenchmarkBuildWhereClause(b *testing.B) {
	filter := ParseQuery("name:lodash format:npm format:pypi repo:public free text sha256:abcdef01")

	for _, n := range []int{10, 100, 1000, 10000} {
		readable := benchReadable(n)

		b.Run(fmt.Sprintf("readable_repos=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				_, _ = buildWhereClause(filter, readable)
			}
		})
	}
}

// BenchmarkNarrowSearchView renders the 16-way UNION ALL subquery, which
// Search and CountResults rebuild on every call.
func BenchmarkNarrowSearchView(b *testing.B) {
	b.ReportAllocs()

	for b.Loop() {
		_ = narrowSearchView()
	}
}

func BenchmarkEscapeLike(b *testing.B) {
	b.ReportAllocs()

	for b.Loop() {
		_ = escapeLike("my_package%name\\x")
	}
}

// BenchmarkPackageRanking renders the ranking added to every search.
func BenchmarkPackageRanking(b *testing.B) {
	b.ReportAllocs()

	for b.Loop() {
		_ = packageRanking("s.name", "lodash", 3, "")
	}
}

func BenchmarkPrincipalCacheKey(b *testing.B) {
	id := "6f1c2d3e-0000-4000-8000-000000000001"
	p := auth.Principal{
		UserID: &id, Username: "alice", TokenKind: "access",
		TokenScopes: []string{"proj-a", "proj-b"}, GroupIDs: []string{"g1", "g2", "g3"},
	}

	b.ReportAllocs()

	for b.Loop() {
		_ = principalCacheKey(p)
	}
}

// BenchmarkReadableCacheHit is what a repeated search by the same principal
// costs for its readable set instead of scanning every repository and checking
// permissions on each (loadReadableRepos, a database round trip).
func BenchmarkReadableCacheHit(b *testing.B) {
	for _, n := range []int{100, 10000} {
		b.Run(fmt.Sprintf("readable_repos=%d", n), func(b *testing.B) {
			src := &fakeAuthCache{ttl: time.Minute}
			c := &readableCache{src: src}
			p := auth.Principal{Username: "alice"}
			key := principalCacheKey(p)
			gen, _ := c.begin()
			c.store(key, gen, benchReadable(n))

			b.ReportAllocs()

			for b.Loop() {
				gen, _ := c.begin()
				if _, ok := c.get(principalCacheKey(p), gen); !ok {
					b.Fatal("cache miss")
				}
			}
		})
	}
}
