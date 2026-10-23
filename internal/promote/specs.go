// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package promote

import "github.com/openartifact/openartifact/internal/database/models"

// rowSpecs returns the formats that the generic row promoter handles: one
// table row per artifact file, one blob per row.
func rowSpecs() []rowSpec {
	return []rowSpec{
		{
			format:  "maven",
			kind:    models.TrashKindMaven,
			table:   "maven_artifacts",
			name:    "s.group_id || ':' || s.artifact_id_coord",
			version: "s.version",
			idents:  []string{"s.repo_path"},
			key:     []string{"repo_path"},
			cols: []string{
				"group_id", "artifact_id_coord", "version", "classifier", "extension",
				"snapshot_timestamp", "snapshot_build_number", "is_snapshot", "repo_path",
				"md5", "sha1", "sha512",
			},
		},
		{
			format:  "ivy",
			kind:    models.TrashKindIvy,
			table:   "ivy_artifacts",
			name:    "s.organisation || ':' || s.module",
			version: "s.revision",
			idents:  []string{"s.artifact_name || '.' || s.artifact_ext"},
			key:     []string{"organisation", "module", "revision", "artifact_name", "artifact_ext"},
			cols:    []string{"organisation", "module", "revision", "artifact_name", "artifact_type", "artifact_ext"},
		},
		{
			format:  "sbt",
			kind:    models.TrashKindSBT,
			table:   "sbt_artifacts",
			name:    "s.group_id || ':' || s.artifact_id",
			version: "s.version",
			idents:  []string{"s.filename"},
			key:     []string{"group_id", "artifact_id", "version", "filename"},
			cols:    []string{"group_id", "artifact_id", "version", "scala_version", "filename"},
		},
		{
			format:   "pypi",
			kind:     models.TrashKindPyPI,
			table:    "pypi_packages",
			name:     "s.name",
			version:  "s.version",
			idents:   []string{"s.filename"},
			key:      []string{"name", "version", "filename"},
			licenses: "array_remove(ARRAY[NULLIF(s.license, '')], NULL)",
			cols: []string{
				"name", "version", "filename", "package_type", "python_version", "requires_python",
				"summary", "author", "author_email", "license", "keywords", "classifiers",
			},
		},
		{
			format:  "nuget",
			kind:    models.TrashKindNuGet,
			table:   "nuget_packages",
			name:    "s.package_id",
			version: "s.version",
			idents:  []string{"s.package_id || '.' || s.version || '.nupkg'"},
			key:     []string{"package_id", "version"},
			cols: []string{
				"package_id", "version", "title", "description", "summary", "authors", "owners", "tags",
				"project_url", "license_url", "icon_url", "require_license_acceptance", "is_prerelease",
				"min_client_version", "dependency_groups",
			},
		},
		{
			format:   "cargo",
			kind:     models.TrashKindCargo,
			table:    "cargo_crates",
			name:     "s.name",
			version:  "s.version",
			idents:   []string{"s.name || '-' || s.version || '.crate'"},
			key:      []string{"name", "version"},
			licenses: "array_remove(ARRAY[NULLIF(s.license, '')], NULL)",
			cols: []string{
				"name", "version", "description", "homepage", "documentation", "repository", "license",
				"keywords", "categories", "authors", "readme", "metadata_json", "yanked",
			},
		},
		{
			format:   "rubygems",
			kind:     models.TrashKindRubyGems,
			table:    "rubygems_gems",
			name:     "s.name",
			version:  "s.version",
			idents:   []string{"s.name || '-' || s.version || CASE WHEN s.platform IN ('', 'ruby') THEN '' ELSE '-' || s.platform END || '.gem'"},
			key:      []string{"name", "version", "platform"},
			licenses: "s.licenses",
			cols: []string{
				"name", "version", "platform", "description", "summary", "homepage", "authors", "licenses",
				"dependencies", "metadata_json", "yanked", "size",
			},
		},
		{
			format:  "conda",
			kind:    models.TrashKindConda,
			table:   "conda_packages",
			name:    "s.name",
			version: "s.version",
			idents:  []string{"s.name || '-' || s.version || '-' || s.build_string || s.extension"},
			key:     []string{"name", "version", "build_string", "platform", "extension"},
			cols: []string{
				"name", "version", "build_string", "platform", "depends", "extension", "build_number",
				"md5", "timestamp_ms", "index_extra",
			},
		},
		{
			format:  "helm",
			kind:    models.TrashKindHelm,
			table:   "helm_charts",
			name:    "s.name",
			version: "s.version",
			idents:  []string{"s.name || '-' || s.version || '.tgz'"},
			key:     []string{"name", "version"},
			cols: []string{
				"name", "version", "app_version", "description", "keywords", "home", "sources",
				"maintainers", "icon", "api_version", "type",
			},
		},
		{
			format:  "raw",
			kind:    models.TrashKindRaw,
			table:   "raw_artifacts",
			name:    "s.path",
			version: "",
			idents:  []string{"s.path"},
			key:     []string{"path"},
			cols:    []string{"path", "content_type", "metadata"},
		},
	}
}
