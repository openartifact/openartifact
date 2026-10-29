// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

// newPostgresDB wires one store per domain around the shared core c.
func newPostgresDB(c *core) *PostgresDB {
	return &PostgresDB{
		core: c,
		// STORES-INIT
	}
}
