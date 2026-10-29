// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

// PostgresDB is the PostgreSQL database: the composition root that embeds one
// store per domain (see stores.go). Consumers never depend on this type but on
// the small interfaces they declare themselves; PostgresDB satisfies them
// through the promoted store methods.
type PostgresDB struct {
	*core
	*BlobRefStore
	// STORES-STRUCT
}

// newPostgresDB wires one store per domain around the shared core c.
func newPostgresDB(c *core) *PostgresDB {
	return &PostgresDB{
		core:         c,
		BlobRefStore: &BlobRefStore{core: c},
		// STORES-INIT
	}
}
