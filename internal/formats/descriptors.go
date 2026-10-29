// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import "github.com/openartifact/openartifact/internal/format/storage"

// Shorthands of the storage descriptors: a Field reads one value of the artifact
// detail document (see storage.Source).

func fromP(path string) storage.Field { return storage.Field{Source: storage.FromPackage, Path: path} }
func fromV(path string) storage.Field { return storage.Field{Source: storage.FromVersion, Path: path} }
func fromF(path string) storage.Field { return storage.Field{Source: storage.FromFile, Path: path} }
func fromC(path string) storage.Field {
	return storage.Field{Source: storage.FromChecksums, Path: path}
}
func fromS(path string) storage.Field { return storage.Field{Source: storage.FromSide, Path: path} }
func fromK(path string) storage.Field { return storage.Field{Source: storage.FromColumns, Path: path} }
func fromM(path string) storage.Field { return storage.Field{Source: storage.FromSibling, Path: path} }

// joined makes an array-valued field display with sep between the elements.
func joined(f storage.Field, sep string) storage.Field {
	f.Join = sep

	return f
}

// entry is one display entry of the artifact detail.
func entry(key string, from storage.Field) storage.InfoField {
	return storage.InfoField{Key: key, From: from}
}

// keyHead is the cleanup version of a format whose version_key carries more
// coordinates after the version, separated by '|' (arch, component): the files
// that differ only in them are one version.
const keyHead = "split_part(v.version_key, '|', 1)"

// licenseList is the promotion Licenses expression of a format that keeps the
// license of a version in one version-metadata JSON value (a string, or an array
// of strings): the text[] the engine checks against the destination's license
// policies. expr is a jsonb expression over sv.
func licenseList(expr string) string {
	return `CASE jsonb_typeof(` + expr + `)
	  WHEN 'array' THEN ARRAY(SELECT e FROM jsonb_array_elements_text(` + expr + `) e WHERE e <> '')
	  WHEN 'string' THEN array_remove(ARRAY[` + expr + ` #>> '{}'], '')
	  ELSE '{}'::text[] END`
}
