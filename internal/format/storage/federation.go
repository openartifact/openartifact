// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

// FederationUnit is the grain of a federated unit of a format: what one unit
// key stands for in the generic tables.
type FederationUnit uint8

const (
	// UnitVersion is the default: a unit is a version with all its live files
	// (a Go module: the zip and the go.mod). The unit key is carried by the
	// version's primary file (package_files.unit_key).
	UnitVersion FederationUnit = iota
	// UnitFile makes a unit one file: it travels with its package and version
	// rows, but the version's other files are units of their own (a PyPI
	// release holds several distribution files, a Maven version several
	// artifacts, a raw path is one file). The unit key is the file's
	// package_files.unit_key.
	UnitFile
)

// Federation describes how a federated format's units live in the generic
// tables; the federation engine drives its one generic codec from it. The unit
// keys themselves are written by the format's store (package_files.unit_key on
// the primary file of a unit, package_tags.unit_key on a tag) and are the
// keys of docs/design/federation.md; the capture triggers of the baseline log
// a unit whenever such a row changes, with the blob digest of the keyed file
// (or the tag's version) as the content reference.
type Federation struct {
	// Unit is the grain of an artifact unit; the zero value is UnitVersion.
	Unit FederationUnit
	// Tags makes the format's package_tags rows (with a unit_key) ref units that
	// point at a version unit (npm dist-tags).
	Tags bool
	// TouchPackage makes applying or deleting a unit bump packages.updated_at in
	// the same transaction: the format uses it as a validator (the npm packument
	// ETag).
	TouchPackage bool
	// StaleAttr is the key of packages.attrs the format's metadata worker reads
	// as "regenerate me" (npm's packument_stale). When set, every applied or
	// deleted unit sets it to true on its package in the same transaction, so
	// the regeneration survives a crash between the commit and the notification.
	StaleAttr string
	// LocalPackageAttrs are the keys of packages.attrs that hold state local to
	// each instance (worker output such as npm's latest_version and
	// packument_stale). They are never replicated: a package created by a
	// replicated unit starts without them.
	LocalPackageAttrs []string
}
