// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/storage"
)

// dockerSpec describes one of the two soft-deletable docker row kinds. Docker
// is the only format that keeps dedicated tables, so it is the one explicit
// special case of the trash engine; every other format is described by its
// storage.Trash descriptor and lives in package_files (trash_files.go).
//
// All fragments are compile-time constants; nothing user-controlled is ever
// interpolated into the generated SQL. They are written against the alias "t".
type dockerSpec struct {
	kind  models.TrashKind
	table string
	// name and version are text expressions for the trash listing.
	name    string
	version string
	// quota marks rows whose upload reserved storage_quotas usage; a purge
	// gives it back.
	quota bool
	// hasBlob marks rows with a blob_id column.
	hasBlob bool
	// listWhere is an extra predicate applied to the trash listing (to hide rows
	// that are listed under another item), "" for none.
	listWhere string
}

// dockerFormat is the one format name the trash engine special-cases.
const dockerFormat = "docker"

// dockerSpecs lists the docker kinds in the order the trash listing and the
// purge worker visit them.
//
//nolint:gochecknoglobals // immutable registry
var dockerSpecs = []*dockerSpec{
	{
		kind: models.TrashKindDockerManifest, table: "docker_manifests", quota: true, hasBlob: true,
		name: "t.image", version: "t.digest",
	},
	{
		kind: models.TrashKindDockerTag, table: "docker_tags",
		name: "t.image", version: "t.name",
		// A tag trashed together with its manifest is listed under the manifest.
		listWhere: `NOT EXISTS (SELECT 1 FROM docker_manifests m
			WHERE m.repository_id = t.repository_id AND m.image = t.image
			  AND m.digest = t.manifest_digest AND m.deleted_at = t.deleted_at)`,
	},
}

// dockerSpecByKind returns the docker spec registered for kind.
func dockerSpecByKind(kind models.TrashKind) (*dockerSpec, bool) {
	for _, s := range dockerSpecs {
		if s.kind == kind {
			return s, true
		}
	}

	return nil, false
}

// blobSizeExpr is the size of the blob the row references.
func (s *dockerSpec) blobSizeExpr() string {
	if !s.hasBlob {
		return "0::bigint"
	}

	return "(SELECT COALESCE(SUM(bb.size_bytes), 0)::bigint FROM blobs bb WHERE bb.id = t.blob_id)"
}

// blobSHAExpr aggregates the sha256 of the referenced blob, comma separated.
func (s *dockerSpec) blobSHAExpr() string {
	if !s.hasBlob {
		return "''::text"
	}

	return "COALESCE((SELECT string_agg(DISTINCT bb.sha256, ',') FROM blobs bb WHERE bb.id = t.blob_id), '')"
}

// sqlLiteral quotes s as a SQL string literal. It is only fed registry
// constants (format names, kinds); the quoting keeps a fixture value with an
// apostrophe from breaking the statement.
func sqlLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// trashFormatsSQL renders formats as a SQL list for "r.format IN (...)".
func trashFormatsSQL(formats []string) string {
	lits := make([]string, len(formats))
	for i, f := range formats {
		lits[i] = sqlLiteral(f)
	}

	return strings.Join(lits, ", ")
}

// trashDescriptor returns the storage.Trash descriptor of a package format.
func trashDescriptor(format string) (storage.Trash, error) {
	s, ok := storage.Lookup(format)
	if !ok {
		return storage.Trash{}, fmt.Errorf("%w: format %q has no storage descriptor", ErrTrashUnknownKind, format)
	}

	return s.Trash, nil
}

// trashKindOf is the kind the API shows for the trash items of a generic
// package format.
func trashKindOf(format string) models.TrashKind {
	kind, _ := storage.TrashKindOf(format)

	return models.TrashKind(kind)
}

// genericFormats returns the registered package formats the generic trash
// engine handles, optionally limited to one kind. Docker has no descriptor, so
// it never appears.
func genericFormats(kind models.TrashKind) []string {
	if kind == "" {
		names := storage.Names()
		out := make([]string, 0, len(names))

		for _, n := range names {
			if s, ok := storage.Lookup(n); ok && s.Trash.Kind != "" {
				out = append(out, n)
			}
		}

		return out
	}

	return storage.TrashFormatsOfKind(string(kind))
}

// itemCase renders "CASE r.format WHEN 'f' THEN (expr) ... ELSE def END" over
// the formats whose expression differs from def, or def alone when none does.
func itemCase(formats []string, def string, expr func(storage.Trash) string) string {
	var b strings.Builder

	for _, f := range formats {
		s, ok := storage.Lookup(f)
		if !ok {
			continue
		}

		if e := expr(s.Trash); e != "" && e != def {
			fmt.Fprintf(&b, " WHEN %s THEN (%s)", sqlLiteral(f), e)
		}
	}

	if b.Len() == 0 {
		return def
	}

	return "CASE r.format" + b.String() + " ELSE " + def + " END"
}

// versionScopeFormats returns the registered formats whose trash item is the
// whole version.
func versionScopeFormats() []string {
	var out []string

	for _, n := range genericFormats("") {
		if s, ok := storage.Lookup(n); ok && s.Trash.Scope == storage.ScopeVersion {
			out = append(out, n)
		}
	}

	return out
}
