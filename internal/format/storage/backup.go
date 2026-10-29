// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

// Backup is how backup and restore carry a format's rows beyond the generic
// tables. Every format's packages, package_versions, package_files and
// package_tags rows and its SideTables are archived without a descriptor; a
// format states only what restore needs on top of them.
type Backup struct {
	// Companions are the repository-scoped tables the format's side tables
	// reference (the deb distribution graph). They are archived before the
	// package rows and restored first, parents before children.
	Companions []Companion
	// Regen is the generated metadata a restored hosted repository needs queued
	// or marked stale, because archives carry rows and never the derived
	// indexes. Empty for formats whose indexes are computed live.
	Regen RegenKind
}

// Companion is a repository-scoped table that side-table rows point at.
//
// Restore gives every companion row a new id derived from the archived one and
// the new repository id, so rows of a restored repository never collide with
// the ids of the repository it was copied from, and rows that point at each
// other (Refs, SideTable.Refs) are rewritten the same way.
type Companion struct {
	// Name is the table name (a plain identifier).
	Name string
	// Scope is a compile-time constant SQL condition over the alias t that
	// selects the rows of repository $1, e.g. "t.repository_id = $1".
	Scope string
	// RepoColumn is the column that holds the repository id (set to the new
	// repository on restore); empty for a table scoped through a parent.
	RepoColumn string
	// Refs lists the uuid columns that reference the id of another companion
	// (debian_components.distribution_id).
	Refs []string
}
