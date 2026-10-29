// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

// RegenKind names generated metadata (indexes a worker renders from the rows)
// that a bulk write makes stale. A format whose indexes are computed live from
// the tables has none.
type RegenKind string

// The generated-metadata kinds.
const (
	RegenDeb    RegenKind = "deb"
	RegenRPM    RegenKind = "rpm"
	RegenApk    RegenKind = "apk"
	RegenMaven  RegenKind = "maven"
	RegenNPM    RegenKind = "npm"
	RegenPacman RegenKind = "pacman"
)

// Promotion is how the promotion engine copies a format's rows between two
// repositories. The engine finds the live source files that store a bundle
// item's content and copies their package, version and file rows (and side
// rows) into the destination; the zero value is a format where one file is
// one version and nothing else needs doing.
//
// Every SQL string is a compile-time constant; request data only ever travels
// as bind parameters. Fragments are spliced into statements of the engine and
// may use the aliases and parameters their field documents:
//
//	s, sv, sp   the source file, its version and its package
//	d, dv, dp   a destination file, its version and its package
//	$1          the destination repository id (uuid)
type Promotion struct {
	// WholeVersion copies every live file of the matched file's version, not only
	// the matched file: a Go module is a zip and its go.mod, a conan recipe
	// revision is all its files and binary packages.
	WholeVersion bool
	// SharedVersion says a version holds independent files (maven classifiers,
	// pypi wheels): adding one file to a destination version that already has
	// others is legitimate. By default a destination version that has a live
	// file is a collision with the incoming one.
	SharedVersion bool
	// Tags copies the package_tags of the source package that name a copied
	// version (npm dist-tags), keeping existing destination tags.
	Tags bool
	// Regen is the generated metadata the destination needs refreshed after a copy.
	Regen RegenKind
	// Licenses is a SQL expression over sv and s yielding text[]: the licenses a
	// copied version declares, checked against the destination's license policies.
	Licenses string
	// PackageAttrs is a SQL expression over sp yielding the jsonb attrs of a
	// destination package the copy creates; the default is sp.attrs.
	PackageAttrs string
	// VersionKey is a SQL expression over s, sv, sp yielding the version_key of the
	// destination version; the default is sv.version_key. It exists for keys that
	// embed ids of the source repository (deb: the component id).
	VersionKey string
	// Part is a SQL expression over s, sv, sp yielding package_files.part of the
	// copy; the default is s.part. It exists for partitions that embed ids of
	// the source repository (deb: the distribution id).
	Part string
	// Collide is an optional SQL predicate over s, sv, sp, d, dv, dp that is true
	// when destination file d serves the same download as source file s although
	// its path differs (deb and rpm resolve a download by file name alone).
	Collide string
	// Prepare lists statements run once per source file before the destination is
	// probed, with $1 = destination repository id and $2 = source file id: they
	// create the destination rows the copy hangs on (deb distributions).
	Prepare []string
	// AfterCopy lists statements run once per destination version a copy wrote
	// to, with $1 = destination repository id, $2 = destination package id and
	// $3 = destination version id: format state that depends on the copy (the
	// npm latest tag and packument stamp, the rpm stale flag).
	AfterCopy []string
	// SideSet maps a side table name to a SQL expression over x (the source side
	// row), s and $1 yielding a jsonb object of columns to override on the copied
	// side row (deb: the destination's distribution and component ids).
	SideSet map[string]string
}
