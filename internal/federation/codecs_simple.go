// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import "strings"

// The formats below keep one row per unit in one table. Their unit keys are
// documented in docs/design/federation.md and built by migration 071.

const (
	kindMaven = "maven"
	kindNPM   = "npm"
)

func init() { //nolint:gochecknoinits // codec registry
	register(newTableCodec(rawSpec()))
	register(newTableCodec(mavenSpec()))
	register(newTableCodec(pypiSpec()))
	register(newTableCodec(helmSpec()))
	register(newTableCodec(cargoSpec()))
	register(newTableCodec(nugetSpec()))
	register(newTableCodec(goSpec()))
}

func eq1(col string) func(string) (string, []any, bool) {
	return func(key string) (string, []any, bool) {
		return "t." + col + " = $2", []any{key}, key != ""
	}
}

// nameVersion is the where builder of `<name>@<version>` keys.
func nameVersion(nameCol string) func(string) (string, []any, bool) {
	return func(key string) (string, []any, bool) {
		name, version, ok := strings.CutLast(key, "@")

		return "t." + nameCol + " = $2 AND t.version = $3", []any{name, version}, ok && name != "" && version != ""
	}
}

func rawSpec() tableSpec {
	return tableSpec{
		format:      "raw",
		table:       "raw_artifacts",
		kind:        KindArtifact,
		where:       eq1("path"),
		backfillKey: "t.path",
		backfillRef: "federation_blob_sha(t.blob_id)",
		blobCols:    []string{"blob_id"},
		info: func(r Row) *ArtifactInfo {
			return &ArtifactInfo{Name: r.Str("path"), Path: r.Str("path"), SHA256: r.Str("blob_id")}
		},
	}
}

func mavenSpec() tableSpec {
	return tableSpec{
		format:      kindMaven,
		table:       "maven_artifacts",
		kind:        KindArtifact,
		where:       eq1("repo_path"),
		backfillKey: "t.repo_path",
		backfillRef: "federation_blob_sha(t.blob_id)",
		blobCols:    []string{"blob_id"},
		regen: func(repoID string, r Row) []Regen {
			return []Regen{{Kind: kindMaven, RepoID: repoID, GroupID: r.Str("group_id"), ArtifactID: r.Str("artifact_id_coord")}}
		},
		info: func(r Row) *ArtifactInfo {
			return &ArtifactInfo{
				Name:    r.Str("group_id") + ":" + r.Str("artifact_id_coord"),
				Version: r.Str("version"),
				Path:    r.Str("repo_path"),
				SHA256:  r.Str("blob_id"),
			}
		},
	}
}

func pypiSpec() tableSpec {
	return tableSpec{
		format: "pypi",
		table:  "pypi_packages",
		kind:   KindArtifact,
		where: func(key string) (string, []any, bool) {
			parts := strings.SplitN(key, "/", 3)
			if len(parts) != 3 {
				return "", nil, false
			}

			return "t.name = $2 AND t.version = $3 AND t.filename = $4", []any{parts[0], parts[1], parts[2]}, true
		},
		backfillKey: "t.name || '/' || t.version || '/' || t.filename",
		backfillRef: "federation_blob_sha(t.blob_id)",
		blobCols:    []string{"blob_id"},
		info: func(r Row) *ArtifactInfo {
			return &ArtifactInfo{Name: r.Str("name"), Version: r.Str("version"), Path: r.Str("filename"), SHA256: r.Str("blob_id")}
		},
	}
}

func helmSpec() tableSpec {
	return tableSpec{
		format:      "helm",
		table:       "helm_charts",
		kind:        KindArtifact,
		where:       nameVersion("name"),
		backfillKey: "t.name || '@' || t.version",
		backfillRef: "federation_blob_sha(t.blob_id)",
		blobCols:    []string{"blob_id"},
		info: func(r Row) *ArtifactInfo {
			return &ArtifactInfo{Name: r.Str("name"), Version: r.Str("version"), Path: r.Str("name") + "-" + r.Str("version") + ".tgz", SHA256: r.Str("blob_id")}
		},
	}
}

func cargoSpec() tableSpec {
	return tableSpec{
		format:      "cargo",
		table:       "cargo_crates",
		kind:        KindArtifact,
		where:       nameVersion("name"),
		backfillKey: "t.name || '@' || t.version",
		backfillRef: "federation_blob_sha(t.blob_id)",
		blobCols:    []string{"blob_id"},
		exclude:     []string{"download_count"},
		info: func(r Row) *ArtifactInfo {
			return &ArtifactInfo{Name: r.Str("name"), Version: r.Str("version"), Path: r.Str("name") + "-" + r.Str("version") + ".crate", SHA256: r.Str("blob_id")}
		},
	}
}

func nugetSpec() tableSpec {
	return tableSpec{
		format:      "nuget",
		table:       "nuget_packages",
		kind:        KindArtifact,
		where:       nameVersion("package_id"),
		backfillKey: "t.package_id || '@' || t.version",
		backfillRef: "federation_blob_sha(t.blob_id)",
		blobCols:    []string{"blob_id"},
		exclude:     []string{"download_count"},
		info: func(r Row) *ArtifactInfo {
			return &ArtifactInfo{Name: r.Str("package_id"), Version: r.Str("version"), Path: r.Str("package_id") + "." + r.Str("version") + ".nupkg", SHA256: r.Str("blob_id")}
		},
	}
}

func goSpec() tableSpec {
	return tableSpec{
		format:      "go",
		table:       "gomod_modules",
		kind:        KindArtifact,
		where:       nameVersion("module_path"),
		backfillKey: "t.module_path || '@' || t.version",
		backfillRef: "federation_blob_sha(COALESCE(t.zip_blob_id, t.mod_blob_id))",
		blobCols:    []string{"zip_blob_id", "mod_blob_id"},
		info: func(r Row) *ArtifactInfo {
			return &ArtifactInfo{Name: r.Str("module_path"), Version: r.Str("version"), Path: r.Str("module_path") + "/@v/" + r.Str("version") + ".zip", SHA256: r.Str("zip_blob_id")}
		},
	}
}
