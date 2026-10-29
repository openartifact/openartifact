// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

// TrashScope says which package_files rows one trash item stands for.
type TrashScope uint8

const (
	// ScopeFile is the default: the trash item is one listed package_files row
	// (its id is the trash reference id) and nothing else is stamped with it.
	ScopeFile TrashScope = iota
	// ScopeVersion makes the item the whole version: trashing, restoring or
	// purging the listed file does the same to every other file of its version
	// that was trashed with it (gomod: the zip drags the unlisted go.mod along).
	ScopeVersion
)

// Trash is how a format's files are soft-deleted, restored and purged by the
// generic trash engine. The trash unit is the LISTED package_files row of the
// format (an unlisted companion file is never an item of its own); its id is the
// reference id the API and the purge worker use.
//
// Every SQL fragment is a compile-time constant of the format, never user input.
// Fragments see the aliases f (package_files), v (package_versions) and
// p (packages) of the item.
type Trash struct {
	// Kind is the string the API shows as the trash kind of the format's items
	// (models.TrashKind*); one per format. It must be unique across the registry.
	Kind string
	// Scope is ScopeFile (zero value) or ScopeVersion.
	Scope TrashScope
	// NameExpr is the text expression the trash list shows as the item name.
	// Empty means p.name.
	NameExpr string
	// VersionExpr is the text expression the trash list shows as the item
	// version. Empty means v.version.
	VersionExpr string
	// RegenKeys is a select list of two text expressions (for example
	// "p.attrs->>'group_id', p.attrs->>'artifact_id'") naming what a metadata
	// regeneration after a restore or purge needs to know about the item. Empty
	// means the format has no keyed regeneration and both keys read as ''.
	RegenKeys string
	// OnTrash are statements run once per newly trashed trash unit, in the
	// transaction that stamps it, after the files and version are stamped.
	// Each statement takes one parameter: $1 = the id of the listed file.
	OnTrash []string
	// OnRestore are statements run once per restored trash unit, after the
	// files are un-stamped; $1 = the id of the listed file.
	OnRestore []string
	// OnPurge are statements run once per purged trash unit BEFORE its files are
	// deleted (so they can still read the version); $1 = the id of the listed
	// file. The retention worker runs them too when it deletes a live version
	// for good.
	OnPurge []string
}

// TrashKindOf returns the trash kind the format registers, and whether the
// format has a storage descriptor at all.
func TrashKindOf(format string) (string, bool) {
	s, ok := Lookup(format)
	if !ok {
		return "", false
	}

	return s.Trash.Kind, true
}

// TrashFormatsOfKind returns the registered formats whose trash items carry
// kind, in registry order.
func TrashFormatsOfKind(kind string) []string {
	var out []string

	for _, name := range Names() {
		if s, ok := Lookup(name); ok && s.Trash.Kind == kind {
			out = append(out, name)
		}
	}

	return out
}

// NameSQL is NameExpr with the generic default applied.
func (t Trash) NameSQL() string {
	if t.NameExpr != "" {
		return t.NameExpr
	}

	return "p.name"
}

// VersionSQL is VersionExpr with the generic default applied.
func (t Trash) VersionSQL() string {
	if t.VersionExpr != "" {
		return t.VersionExpr
	}

	return "v.version"
}

// RegenKeysSQL is RegenKeys with the default (two empty keys) applied.
func (t Trash) RegenKeysSQL() string {
	if t.RegenKeys != "" {
		return t.RegenKeys
	}

	return "''::text, ''::text"
}
