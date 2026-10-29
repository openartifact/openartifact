// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"net/url"
	"strings"

	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/quarantine"
)

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

// cutLast splits a unit key around its last '@'; without one the whole key is the head.
func cutLast(key string) (string, string) {
	head, tail, ok := strings.CutLast(key, "@")
	if !ok {
		return key, ""
	}

	return head, tail
}

// The Federation.QuarantineKeys of the formats whose unit key is not the path a
// download handler checks a scan under.

func pypiQuarantineKeys(key string) []string { // name/version/filename
	return quarantine.PathKeys(key, key[strings.LastIndex(key, "/")+1:])
}

func helmQuarantineKeys(key string) []string { // name@version
	name, ver := cutLast(key)
	file := name + "-" + ver + ".tgz"

	return quarantine.PathKeys(key, file, "charts/"+file)
}

func cargoQuarantineKeys(key string) []string { // name@version
	name, ver := cutLast(key)

	return quarantine.PathKeys(key, name+"-"+ver+".crate", name+"/"+ver)
}

func goQuarantineKeys(key string) []string { // module@version
	mod, ver := cutLast(key)
	esc := url.PathEscape(mod)

	return quarantine.PathKeys(key, mod+"/@v/"+ver+".zip", mod+"/@v/"+ver+".mod", esc+"@"+ver)
}

func nugetQuarantineKeys(key string) []string { // id@version
	id, ver := cutLast(key)
	file := strings.ToLower(id) + "." + strings.ToLower(ver) + ".nupkg"

	return quarantine.PathKeys(key, file)
}
