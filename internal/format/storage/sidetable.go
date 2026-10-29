// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

// SideTable is a table that extends package_files for one format: one row per
// file, its primary key id references package_files(id) ON DELETE CASCADE.
// Every generic engine that copies, exports or journals file rows carries the
// side rows of the file with it (promotion copies them, backup archives and
// restores them, the recovery journal records them).
type SideTable struct {
	// Name is the table name. Descriptors are compile-time constants; engines
	// splice the name into SQL after checking it is a plain identifier.
	Name string
	// Exclude lists the columns the recovery journal does not record because
	// they are volatile or derivable from the blob. Backup and promotion always
	// carry every column: a restore must not lose a derived value that costs a
	// blob scan to rebuild (rpm_extra, pacman file_list).
	Exclude []string
	// Refs lists the uuid columns that reference rows of the format's
	// Backup.Companions (deb_files.distribution_id, component_id). Restore
	// derives their new values together with the companion rows' own ids.
	Refs []string
}
