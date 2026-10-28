// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repotype_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
	"github.com/openartifact/openartifact/migrations"
)

// read returns the file at a path relative to this package.
func read(t *testing.T, path string) string {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

// missing returns the names of want that are not in have.
func missing(want, have []string) []string {
	var out []string

	for _, n := range want {
		if !slices.Contains(have, n) {
			out = append(out, n)
		}
	}

	return out
}

func TestRegistryShape(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}

	for _, s := range repotypetest.Specs() {
		if s.Name == "" || s.Name != strings.ToLower(s.Name) {
			t.Errorf("format %q: names are non-empty and lower case", s.Name)
		}

		if seen[s.Name] {
			t.Errorf("format %q is registered twice", s.Name)
		}

		seen[s.Name] = true

		if len(s.PackageTables) == 0 {
			t.Errorf("%s: no package tables", s.Name)
		}

		if s.DefaultUpstream != "" && !s.Proxy {
			t.Errorf("%s: has a default upstream but cannot be proxied", s.Name)
		}

		if s.GeneratedGroupMetadata && !s.Group {
			t.Errorf("%s: generates group metadata but cannot be grouped", s.Name)
		}

		if s.GrypeScan && s.PURLType == "" {
			t.Errorf("%s: grype scans by Package URL, which needs a PURL type", s.Name)
		}
	}
}

// TestPolicy freezes who may be proxied, grouped and federated and the default
// upstreams: the registry replaced switch statements, and this is what they
// said.
func TestPolicy(t *testing.T) {
	t.Parallel()

	all := "apk, cargo, conan, conda, deb, docker, go, helm, ivy, maven, nix, npm, nuget, pacman, pypi, raw, rpm, rubygems, sbt, terraform"

	if got := repotype.ProxyFormats(); got != all {
		t.Errorf("ProxyFormats() = %q, want %q", got, all)
	}

	if got := repotype.GroupFormats(); got != all {
		t.Errorf("GroupFormats() = %q, want %q", got, all)
	}

	if got, want := repotype.FederationFormats(), "cargo, docker, go, helm, maven, npm, nuget, pypi, raw"; got != want {
		t.Errorf("FederationFormats() = %q, want %q", got, want)
	}

	upstreams := map[string]string{
		"docker":    "https://registry-1.docker.io",
		"pypi":      "https://pypi.org/simple",
		"cargo":     "https://index.crates.io",
		"go":        "https://proxy.golang.org",
		"nuget":     "https://api.nuget.org/v3/index.json",
		"conan":     "https://center2.conan.io",
		"rubygems":  "https://rubygems.org",
		"pacman":    "https://geo.mirror.pkgbuild.com",
		"nix":       "https://cache.nixos.org",
		"terraform": "https://registry.terraform.io",
	}

	for _, name := range repotype.Names() {
		if got := repotype.DefaultUpstreamURL(name); got != upstreams[name] {
			t.Errorf("DefaultUpstreamURL(%q) = %q, want %q", name, got, upstreams[name])
		}
	}

	for _, unknown := range []string{"", "gomod", "unknown", "DEB"} {
		if repotype.ProxySupported(unknown) || repotype.GroupSupported(unknown) || repotype.FederationSupported(unknown) ||
			repotype.GeneratedGroupMetadata(unknown) || repotype.Valid(unknown) || repotype.DefaultUpstreamURL(unknown) != "" {
			t.Errorf("%q is not a format", unknown)
		}
	}
}

var checkFormats = regexp.MustCompile(`(?s)chk_repo_format CHECK \(format IN \((.*?)\)\)`)

// TestLatestMigrationAcceptsEveryFormat checks the chk_repo_format constraint
// the newest migration that defines it against the registry in both
// directions: a format missing from it cannot be created, one the registry
// lacks is dead.
func TestLatestMigrationAcceptsEveryFormat(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range slices.Backward(entries) {
		body, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}

		m := checkFormats.FindSubmatch(body)
		if m == nil {
			continue
		}

		var have []string

		for f := range strings.SplitSeq(string(m[1]), ",") {
			have = append(have, strings.Trim(strings.TrimSpace(f), "'"))
		}

		if names := missing(repotype.Names(), have); len(names) > 0 {
			t.Errorf("registered formats %v are not in chk_repo_format of %s: add a migration that extends the constraint "+
				"(and a *_validate.no_tx.sql validating it)", names, e.Name())
		}

		if names := missing(have, repotype.Names()); len(names) > 0 {
			t.Errorf("chk_repo_format of %s accepts %v, which are not registered formats", e.Name(), names)
		}

		return
	}

	t.Fatal("no migration defines chk_repo_format")
}

// queryBody returns the text of the named sqlc query of a file.
func queryBody(t *testing.T, file, name string) string {
	t.Helper()

	src := read(t, file)

	_, rest, ok := strings.Cut(src, "-- name: "+name+" ")
	if !ok {
		t.Fatalf("%s: query %s not found", file, name)
	}

	body, _, _ := strings.Cut(rest, "\n-- name: ")

	return body
}

// TestComponentQueriesFilterEveryFormat checks that the package listing and
// count queries have a branch that answers to each format's name.
func TestComponentQueriesFilterEveryFormat(t *testing.T) {
	t.Parallel()

	const file = "../database/queries/components.sql"

	for _, q := range []string{"ListRepoComponents", "ListRepoComponentsByName", "CountRepoComponents", "GetRepoComponentBlob"} {
		body := queryBody(t, file, q)

		for _, name := range repotype.Names() {
			if !strings.Contains(body, "'"+name+"'") {
				t.Errorf("%s: query %s has no branch for format %q: add the package table to the union", file, q, name)
			}
		}
	}
}

// unions are the files that enumerate every package table, with the tables
// they leave out on purpose.
//
//nolint:gochecknoglobals // test table
var unions = map[string]map[string]string{
	"../database/queries/components.sql":     nil,
	"../database/queries/catalog_detail.sql": nil,
	"../database/queries/security.sql":       {"docker_manifests": "docker images are scanned per tag, not per manifest row"},
	"../database/queries/repo_usage.sql":     {"docker_manifests": "docker usage is accounted through its tags"},
	"../database/catalog_branches.go":        nil,
	"../database/postgres_quota.go":          {"docker_tags": "tags own no blob: the manifests carry the quota"},
	"../database/postgres_tiering.go":        {"docker_tags": "tags own no blob: the manifests are tiered"},
	"../database/trash_kinds.go":             nil,
	"../recovery/tables.go":                  nil,
	"../backup/backup.go":                    {"rubygems_gems": "exported through ListRubyGems, not by table name"},
}

// TestPackageTablesInUnions checks that every query or table list that unions
// the package tables names the tables of every format.
func TestPackageTablesInUnions(t *testing.T) {
	t.Parallel()

	for file, skip := range unions {
		src := read(t, file)

		for _, s := range repotypetest.Specs() {
			for _, table := range s.PackageTables {
				if _, ok := skip[table]; ok {
					continue
				}

				if !regexp.MustCompile(`\b` + table + `\b`).MatchString(src) {
					t.Errorf("%s does not reference %s, the package table of format %q: add it to the union", file, table, s.Name)
				}
			}
		}
	}
}

var sqlFormatList = regexp.MustCompile(`format IN \(([^)]*)\)`)

// formatsIn returns the formats of the `format IN (...)` lists of a file.
func formatsIn(t *testing.T, file string) [][]string {
	t.Helper()

	var out [][]string

	for _, m := range sqlFormatList.FindAllStringSubmatch(read(t, file), -1) {
		var names []string

		for f := range strings.SplitSeq(m[1], ",") {
			names = append(names, strings.Trim(strings.TrimSpace(f), "'"))
		}

		out = append(out, names)
	}

	return out
}

// TestSQLFormatLists checks the hard-coded format lists of SQL against the
// registry flags they stand for.
func TestSQLFormatLists(t *testing.T) {
	t.Parallel()

	var generated, gpgGenerated []string

	for _, s := range repotypetest.Specs() {
		if s.GeneratedGroupMetadata {
			generated = append(generated, s.Name)

			if s.Signing == repotype.SigningGPG {
				gpgGenerated = append(gpgGenerated, s.Name)
			}
		}
	}

	check := func(file string, want []string, n int) {
		lists := formatsIn(t, file)
		if len(lists) != n {
			t.Fatalf("%s: found %d format lists, want %d", file, len(lists), n)
		}

		for _, got := range lists {
			if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
				t.Errorf("%s lists %v, the registry says %v", file, got, want)
			}
		}
	}

	// Groups that serve generated metadata.
	check("../database/postgres_groupmeta.go", generated, 2)
	// Hosted repositories whose generated metadata is GPG-signed.
	check("../database/queries/gpg.sql", gpgGenerated, 1)
}

// TestOpenAPIFormatEnums checks the format enums of the OpenAPI document.
func TestOpenAPIFormatEnums(t *testing.T) {
	t.Parallel()

	var doc any
	if err := json.Unmarshal([]byte(read(t, "../api/openapi.json")), &doc); err != nil {
		t.Fatal(err)
	}

	found := 0

	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			if enum, ok := x["enum"].([]any); ok && slices.Contains(enum, any("rubygems")) {
				found++

				var have []string
				for _, e := range enum {
					have = append(have, e.(string))
				}

				if names := missing(repotype.Names(), have); len(names) > 0 {
					t.Errorf("openapi.json %s lacks formats %v", path, names)
				}

				if names := missing(have, repotype.Names()); len(names) > 0 {
					t.Errorf("openapi.json %s lists %v, which are not registered formats", path, names)
				}
			}

			for k, c := range x {
				walk(path+"/"+k, c)
			}
		case []any:
			for _, c := range x {
				walk(path, c)
			}
		}
	}

	walk("", doc)

	if found < 3 {
		t.Errorf("found %d format enums in openapi.json, want at least 3 (Repository, list filter, create request)", found)
	}
}
