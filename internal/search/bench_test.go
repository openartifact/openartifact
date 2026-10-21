// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
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
