// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"strings"
	"testing"
)

func TestContainsPattern(t *testing.T) {
	for in, want := range map[string]string{
		"":         "%%",
		"abc":      "%abc%",
		"a_b":      `%a\_b%`,
		"50%":      `%50\%%`,
		`a\b`:      `%a\\b%`,
		`%_\`:      `%\%\_\\%`,
		"Ünï cödé": "%Ünï cödé%",
	} {
		if got := containsPattern(in); got != want {
			t.Errorf("containsPattern(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRollupQueryShape(t *testing.T) {
	t.Run("filters are bound in order", func(t *testing.T) {
		query, args := rollupQuery(rollupFilter{
			repoID: "r", repoFormat: "npm", format: "npm", name: "n", query: "a_b", limit: 20, offset: 40,
		})

		want := []any{"r", "npm", "n", `%a\_b%`, 20, 40}
		if len(args) != len(want) {
			t.Fatalf("args %v, want %v", args, want)
		}

		for i := range want {
			if args[i] != want[i] {
				t.Errorf("arg %d = %v, want %v", i+1, args[i], want[i])
			}
		}

		for _, frag := range []string{"pk.format = $2", "pk.name = $3", "v.version ILIKE $4", "pk.name ILIKE $4", "LIMIT $5 OFFSET $6"} {
			if !strings.Contains(query, frag) {
				t.Errorf("query lacks %q:\n%s", frag, query)
			}
		}
	})

	t.Run("a docker repository has no hosted branch", func(t *testing.T) {
		query, _ := rollupQuery(rollupFilter{repoID: "r", repoFormat: "docker", limit: 10})
		if strings.Contains(query, "hosted AS") || !strings.Contains(query, "rowgrouped AS") {
			t.Errorf("docker rollup:\n%s", query)
		}
	})

	t.Run("flagged reads the flagged versions", func(t *testing.T) {
		query, _ := rollupQuery(rollupFilter{repoID: "r", repoFormat: "npm", flagged: true, limit: 10})
		for _, frag := range []string{"flagged_ids AS MATERIALIZED", "fv AS MATERIALIZED", "fpk AS MATERIALIZED", "a.latest_id IN (SELECT id FROM fv)"} {
			if !strings.Contains(query, frag) {
				t.Errorf("flagged query lacks %q:\n%s", frag, query)
			}
		}
	})

	t.Run("sort by name", func(t *testing.T) {
		query, _ := rollupQuery(rollupFilter{repoID: "r", repoFormat: "npm", sortByName: true, limit: 10})
		if !strings.Contains(query, "ORDER BY name, format") || !strings.HasSuffix(query, "ORDER BY p.name, p.format") {
			t.Errorf("by-name order:\n%s", query)
		}
	})
}

func TestPackageCountQueryCoversEveryBranch(t *testing.T) {
	query := packageCountQuery()

	for _, frag := range []string{"FROM packages pk WHERE pk.repository_id = cr.id", "FROM docker_tags t", "FROM proxy_cache_entries t", "cr.format <> 'docker'", "cr.format = 'docker'"} {
		if !strings.Contains(query, frag) {
			t.Errorf("package count lacks %q:\n%s", frag, query)
		}
	}

	if strings.Contains(query, "FROM package_files") {
		t.Errorf("the hosted count must not read package_files:\n%s", query)
	}
}
