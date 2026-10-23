// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

// fixture seeds one artifact of a format the way an upload leaves it, with
// every copied column set to a non-default value so a dropped column is seen.
type fixture struct {
	format string
	// version is the artifact's version; release versions are protected by immutable_releases.
	version string
	// path is the bundle item's artifact_path hint.
	path string
	// tables are the tables the artifact's rows live in (all soft-deleted by the trash case).
	tables []string
	// compare are the tables whose source and destination rows must match column by column.
	compare []string
	// ignore are extra columns left out of that comparison (parent ids that differ per repository).
	ignore []string
	// seed inserts the artifact into repoID with blobID as its content.
	seed func(h *harness, repoID, blobID string)
	// copies is how many artifacts one promotion writes (default 1).
	copies int
	// verify runs extra checks after the first promotion.
	verify func(h *harness, src, dst string)
}

func (f fixture) wantCopies() int {
	if f.copies == 0 {
		return 1
	}

	return f.copies
}

// fixtures returns one fixture per hosted format.
func fixtures() []fixture {
	return append(rowFixtures(), specialFixtures()...)
}

// rowFixtures covers the formats the generic row promoter handles.
func rowFixtures() []fixture {
	return []fixture{
		{
			format: "maven", version: "2.4.0", path: "com/acme/shop/2.4.0/shop-2.4.0.jar",
			tables: []string{"maven_artifacts"}, compare: []string{"maven_artifacts"},
			seed: func(h *harness, repo, blob string) {
				h.row("maven_artifacts", repo, "blob_id", blob, map[string]any{
					"group_id": "com.acme", "artifact_id_coord": "shop", "version": "2.4.0", "classifier": "sources",
					"extension": "jar", "snapshot_timestamp": "20240101.000000", "snapshot_build_number": 7,
					"is_snapshot": true, "repo_path": "com/acme/shop/2.4.0/shop-2.4.0.jar",
					"md5": "m5", "sha1": "s1", "sha512": "s512",
				})
			},
		},
		{
			format: "ivy", version: "1.0.0", path: "shop.jar",
			tables: []string{"ivy_artifacts"}, compare: []string{"ivy_artifacts"},
			seed: func(h *harness, repo, blob string) {
				h.row("ivy_artifacts", repo, "blob_id", blob, map[string]any{
					"organisation": "acme", "module": "shop", "revision": "1.0.0", "artifact_name": "shop",
					"artifact_type": "source", "artifact_ext": "jar",
				})
			},
		},
		{
			format: "sbt", version: "1.0.0", path: "shop_2.13-1.0.0.jar",
			tables: []string{"sbt_artifacts"}, compare: []string{"sbt_artifacts"},
			seed: func(h *harness, repo, blob string) {
				h.row("sbt_artifacts", repo, "blob_id", blob, map[string]any{
					"group_id": "com.acme", "artifact_id": "shop_2.13", "version": "1.0.0",
					"scala_version": "2.13", "filename": "shop_2.13-1.0.0.jar",
				})
			},
		},
		{
			format: "pypi", version: "1.0.0", path: "shop-1.0.0-py3-none-any.whl",
			tables: []string{"pypi_packages"}, compare: []string{"pypi_packages"},
			seed: func(h *harness, repo, blob string) {
				h.row("pypi_packages", repo, "blob_id", blob, map[string]any{
					"name": "shop", "version": "1.0.0", "filename": "shop-1.0.0-py3-none-any.whl",
					"package_type": "bdist_wheel", "python_version": "py3", "requires_python": ">=3.9",
					"summary": "a shop", "author": "Ann", "author_email": "ann@example.com", "license": "MIT",
					"keywords": "shop,cart", "classifiers": sqlExpr("'{\"A\",\"B\"}'"),
				})
			},
		},
		{
			format: "nuget", version: "3.1.0", path: "Acme.Shop.3.1.0.nupkg",
			tables: []string{"nuget_packages"}, compare: []string{"nuget_packages"},
			seed: func(h *harness, repo, blob string) {
				h.row("nuget_packages", repo, "blob_id", blob, map[string]any{
					"package_id": "Acme.Shop", "version": "3.1.0", "title": "Shop", "description": "d", "summary": "s",
					"authors": "Ann", "owners": "Bob", "tags": "a b", "project_url": "https://p", "license_url": "https://l",
					"icon_url": "https://i", "require_license_acceptance": true, "is_prerelease": true,
					"min_client_version": "5.0", "dependency_groups": sqlExpr(`'[{"targetFramework":"net8.0"}]'::jsonb`),
				})
			},
		},
		{
			format: "cargo", version: "1.2.3", path: "shop-1.2.3.crate",
			tables: []string{"cargo_crates"}, compare: []string{"cargo_crates"},
			seed: func(h *harness, repo, blob string) {
				h.row("cargo_crates", repo, "blob_id", blob, map[string]any{
					"name": "shop", "version": "1.2.3", "description": "d", "homepage": "https://h", "documentation": "https://d",
					"repository": "https://r", "license": "MIT", "keywords": sqlExpr("'{a,b}'"), "categories": sqlExpr("'{c}'"),
					"authors": sqlExpr("'{Ann}'"), "readme": "# shop", "metadata_json": sqlExpr(`'{"features":{"x":[]}}'::jsonb`),
					"yanked": true,
				})
			},
		},
		{
			format: "rubygems", version: "1.0.0", path: "shop-1.0.0-java.gem",
			tables: []string{"rubygems_gems"}, compare: []string{"rubygems_gems"},
			seed: func(h *harness, repo, blob string) {
				h.row("rubygems_gems", repo, "blob_id", blob, map[string]any{
					"name": "shop", "version": "1.0.0", "platform": "java", "description": "d", "summary": "s",
					"homepage": "https://h", "authors": sqlExpr("'{Ann}'"), "licenses": sqlExpr("'{MIT}'"),
					"dependencies": sqlExpr(`'[{"name":"rack"}]'::jsonb`), "metadata_json": sqlExpr(`'{"k":"v"}'::jsonb`),
					"yanked": true, "size": 4242,
				})
			},
		},
		{
			format: "conda", version: "1.0", path: "shop-1.0-h123_0.conda",
			tables: []string{"conda_packages"}, compare: []string{"conda_packages"},
			seed: func(h *harness, repo, blob string) {
				h.row("conda_packages", repo, "blob_id", blob, map[string]any{
					"name": "shop", "version": "1.0", "build_string": "h123_0", "platform": "linux-64",
					"depends": sqlExpr("'{python,numpy}'"), "extension": ".conda", "build_number": 3, "md5": "abc",
					"timestamp_ms": 1700000000000, "index_extra": sqlExpr(`'{"license":"MIT"}'::jsonb`),
				})
			},
		},
		{
			format: "helm", version: "0.9.0", path: "shop-0.9.0.tgz",
			tables: []string{"helm_charts"}, compare: []string{"helm_charts"},
			seed: func(h *harness, repo, blob string) {
				h.row("helm_charts", repo, "blob_id", blob, map[string]any{
					"name": "shop", "version": "0.9.0", "app_version": "2.4.0", "description": "d", "keywords": sqlExpr("'{a}'"),
					"home": "https://h", "sources": sqlExpr("'{https://s}'"), "maintainers": sqlExpr(`'[{"name":"Ann"}]'::jsonb`),
					"icon": "https://i", "api_version": "v2", "type": "application",
				})
			},
		},
		{
			format: "raw", path: "/docs/readme.txt",
			tables: []string{"raw_artifacts"}, compare: []string{"raw_artifacts"},
			seed: func(h *harness, repo, blob string) {
				h.row("raw_artifacts", repo, "blob_id", blob, map[string]any{
					"path": "docs/readme.txt", "content_type": "text/plain", "metadata": sqlExpr(`'{"k":"v"}'::jsonb`),
				})
			},
		},
	}
}
