// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"strings"
	"testing"
)

// The narrow view feeds the count and the page selection, so it must stay free
// of the blobs table (joined for one page only) while keeping a branch for
// every format that Search reports.
func TestNarrowSearchView_NoBlobJoinEveryFormat(t *testing.T) {
	v := narrowSearchView()

	for _, banned := range []string{"JOIN blobs", "b.sha256", "b.size_bytes", "docker_manifests"} {
		if strings.Contains(v, banned) {
			t.Errorf("narrow view references %q:\n%s", banned, v)
		}
	}

	formats := []string{
		"apk",
		"cargo",
		"conda",
		"conan",
		"terraform",
		"terraform",
		"docker",
		"go",
		"helm",
		"ivy",
		"maven",
		"npm",
		"nuget",
		"pypi",
		"raw",
		"rubygems",
		"sbt",
		"rpm",
		"deb",
	}
	if got, want := strings.Count(v, "UNION ALL")+1, len(formats); got != want {
		t.Errorf("narrow view has %d branches, want %d", got, want)
	}

	for _, f := range formats {
		if !strings.Contains(v, "'"+f+"'::text AS format") {
			t.Errorf("narrow view lost the %s branch", f)
		}
	}
}

func TestSearchSource(t *testing.T) {
	tests := []struct {
		name         string
		filter       SearchFilter
		wantBlobJoin bool
		wantRepoJoin bool
	}{
		{"free text", SearchFilter{FreeText: "x"}, false, false},
		{"repo filter joins repositories", SearchFilter{Repos: []string{"a"}}, false, true},
		{"sha256 enriches the view", SearchFilter{SHA256Prefix: "ab"}, true, false},
		{"sha256 and repo", SearchFilter{SHA256Prefix: "ab", Repos: []string{"a"}}, true, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source, joinRepo := searchSource(tc.filter)

			if got := strings.Contains(source, "LEFT JOIN blobs"); got != tc.wantBlobJoin {
				t.Errorf("blob join = %v, want %v", got, tc.wantBlobJoin)
			}

			if joinRepo != tc.wantRepoJoin {
				t.Errorf("repo join = %v, want %v", joinRepo, tc.wantRepoJoin)
			}
		})
	}
}
