// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package storage is the leaf descriptor package of the format plugins: what a
// format's rows in the generic package tables (packages, package_versions,
// package_files and their side tables) need from the subsystems that work on
// every format the same way: trash, retention cleanup, federation, the artifact
// detail, promotion, backup and the recovery journal.
//
// It imports nothing from the rest of the repository (only the standard library
// and internal/registry), so internal/database, the workers, search, promote,
// federation, backup and recovery can all consult it without an import cycle.
// internal/format/plugin fills the registry from each format's registration at
// startup (formats.Register); a format that is not registered has no storage
// descriptor and the generic engines reject it.
//
// Docker keeps dedicated tables and is handled by the engines explicitly; it has
// no Storage value.
package storage

// Storage is the per-format description of how the generic engines treat the
// format's package rows. The zero value of every field is the generic default,
// so a format only states what differs.
type Storage struct {
	// Trash is how the format's files are soft-deleted, restored and purged.
	Trash Trash
	// Cleanup is how the retention worker ranks and protects the format's versions.
	Cleanup Cleanup
	// Federation describes the federated units of the format, nil for formats that
	// are not federated (repotype.Spec.Federation is false).
	Federation *Federation
	// Detail is how the artifact detail endpoint decodes the format's metadata.
	Detail Detail
	// Promotion is how the promotion engine copies the format's rows between repositories.
	Promotion Promotion
	// Backup is how backup and restore carry the format's rows beyond the generic tables.
	Backup Backup
	// SideTables are the format's tables that extend package_files (one row per
	// file, primary key = package_files.id), journaled and backed up with the
	// generic tables.
	SideTables []SideTable
}
