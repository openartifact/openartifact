// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/quarantine"
	blobstore "github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// plain is the registration of a format whose hosted handler is built from the
// standard dependencies alone and whose metadata needs no lifecycle hooks: the
// ten values that differ per format, in the order of plugin.Definition.
func plain(
	id string,
	hosted func(*plugin.Deps) (plugin.Hosted, error),
	supply plugin.Analysis,
	snippets clientsetup.Builder,
	mirror proxy.Hooks,
	merge *group.Hooks,
	store storage.Storage,
) plugin.Definition {
	return plugin.Definition{
		ID:        id,
		NewHosted: hosted,
		Supply:    supply,
		Snippets:  snippets,
		Mirror:    plugin.ProxyOf(mirror),
		Merge:     plugin.GroupOf(merge),
		Meta:      lifecycle.None,
		Store:     store,
	}
}

// ErrStoreMismatch is returned when the database does not implement the store
// interface a handler constructor asks for.
var ErrStoreMismatch = errors.New("formats: database does not implement the handler's store")

// baseURLHosted adapts the constructor most handlers have, taking the logger,
// the format's store interface, the blob store downloads are served from and the
// public base URL, to a plugin.Definition.NewHosted. The format packages declare
// their own store interface (they must not import the database), which the
// PostgresDB implements; the assertion fails at mount, naming the format, when
// it does not.
func baseURLHosted[S any, H plugin.Hosted](
	ctor func(*logger.Logger, S, blobstore.BlobStore, string) H,
) func(*plugin.Deps) (plugin.Hosted, error) {
	return func(d *plugin.Deps) (plugin.Hosted, error) {
		db, err := storeOf[S](d)
		if err != nil {
			return nil, err
		}

		return ctor(d.Log, db, d.Served, d.BaseURL), nil
	}
}

// downloadOnly adapts the constructor of the handlers that only serve downloads
// and announce nothing (deb, rpm: no base URL), together with the format's REST
// upload handler when the server built one.
func downloadOnly[S any, H interface{ RegisterRepoRoutes(chi.Router) }](
	format string,
	ctor func(*logger.Logger, S, blobstore.BlobStore) H,
) func(*plugin.Deps) (plugin.Hosted, error) {
	return func(d *plugin.Deps) (plugin.Hosted, error) {
		db, err := storeOf[S](d)
		if err != nil {
			return nil, err
		}

		return withREST(quiet{ctor(d.Log, db, d.Served)}, d, format), nil
	}
}

// storeOf is the database as the store interface S of a handler package.
func storeOf[S any](d *plugin.Deps) (S, error) {
	db, ok := any(d.DB).(S)
	if !ok {
		return db, fmt.Errorf("%w: %v", ErrStoreMismatch, reflect.TypeFor[S]())
	}

	return db, nil
}

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

// infoOf is the display entries that read the keys of one source under the same
// names.
func infoOf(source storage.Source, keys ...string) []storage.InfoField {
	out := make([]storage.InfoField, len(keys))
	for i, k := range keys {
		out[i] = storage.InfoField{Key: k, From: storage.Field{Source: source, Path: k}}
	}

	return out
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
