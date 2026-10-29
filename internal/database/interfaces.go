// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// ErrLastAdmin is returned when deleting the last enabled admin user.
var ErrLastAdmin = errors.New("cannot delete the last enabled admin")

// ErrDuplicate is returned by Create*Package when an insert hits the unique
// constraint (ON CONFLICT), so callers can distinguish an idempotent duplicate
// upload (map to HTTP 409) from a real failure. This closes the check-then-insert
// TOCTOU: concurrent identical uploads race the existence check, but exactly one
// INSERT wins and the losers observe ErrDuplicate.
var ErrDuplicate = errors.New("artifact already exists")

// ErrProjectRepoLimit is returned when creating or moving a repository would
// exceed the project's max_repositories.
var ErrProjectRepoLimit = errors.New("project repository limit reached")

// ErrRepositoryNotFound is returned when a repository to modify does not exist (or is trashed).
var ErrRepositoryNotFound = errors.New("repository not found")

// Transaction defines the interface for database transactions.
type Transaction interface {
	// Commit commits the transaction
	Commit() error

	// Rollback rolls back the transaction
	Rollback() error

	// Exec executes a query without returning rows
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)

	// Query executes a query that returns rows
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)

	// QueryRow executes a query that returns a single row
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DatabaseConfig contains database connection configuration.
type DatabaseConfig struct {
	// Driver names the database driver. It must be non-empty; PostgreSQL is
	// the only supported database and the server always connects to it.
	Driver string `yaml:"driver"`
	// Host is the database host.
	Host string `yaml:"host"`
	// Port is the database port.
	Port int `yaml:"port"`
	// User is the database user.
	User string `yaml:"user"`
	// Password is the database password. The CHANGE_ME placeholder of the
	// example files is rejected at startup.
	Password string `yaml:"password"`
	// Database is the database name.
	Database string `yaml:"database"`
	// SSLMode is the PostgreSQL sslmode; disable only for local development
	// without TLS.
	SSLMode string `yaml:"sslMode"`
	// Options are extra libpq connection parameters. The environment form is
	// comma-separated key=value pairs, merged over the YAML keys.
	Options map[string]string `yaml:"options"`
	// MaxOpenConns is the size of the connection pool. 0 means the driver
	// default; otherwise at least 2 (a blob-lock transaction pins one
	// connection and its callback needs another), and at least 4 while
	// tiering.enabled is true.
	MaxOpenConns int `yaml:"maxOpenConns"`
	// MaxIdleConns is how many idle connections the pool keeps warm.
	MaxIdleConns int `yaml:"maxIdleConns"`
	// ConnMaxLifetimeMinutes is the maximum lifetime of a connection, in minutes.
	ConnMaxLifetimeMinutes int `yaml:"connMaxLifetimeMinutes"`
	// AuthCacheTTLMillis is how long, in milliseconds, per-request
	// authentication lookups (token, user, groups, permission targets,
	// repository by name) are served from memory. Writes through this process
	// invalidate immediately; other replicas observe a change after at most
	// this long. 0 disables the cache.
	AuthCacheTTLMillis int `yaml:"authCacheTtlMs"`
	// ConnectTimeout is how long startup keeps retrying the initial database
	// connection (capped exponential backoff) before giving up. 0 fails at the
	// first error.
	ConnectTimeout time.Duration `yaml:"connectTimeout"`
}

// ComponentListOptions selects and orders a repository's package listing.
type ComponentListOptions struct {
	// Format restricts the listing to one package format ("" = all).
	Format string
	// Name restricts it to rows whose listed name equals Name exactly.
	Name string
	// Query keeps rows whose listed name or version contains Query, ignoring case.
	Query string
	// SortByName orders by (name, version) instead of newest first.
	SortByName bool
	// After, when set, continues the newest-first listing strictly after that row
	// (keyset paging): Page is then ignored. It cannot be combined with SortByName.
	After *ComponentCursor
	// NoTotal skips counting the matching rows; the returned total is then 0.
	NoTotal bool
	Page    int
	Limit   int
}

// ComponentCursor is a position in the newest-first package listing, the
// (created_at, id) of a row: the listing order is created_at DESC, id ASC.
type ComponentCursor struct {
	CreatedAt time.Time
	ID        string
}

// ComponentSearcher is implemented by stores that can filter and order a
// repository's package listing (GET /api/v1/repositories/{name}/packages with
// package_name, q or sort). Kept separate from the main interface so only the
// real store has to provide it. It returns the requested page and the total
// number of matching rows.
type ComponentSearcher interface {
	SearchComponents(ctx context.Context, repoID string, opts ComponentListOptions) ([]*models.Component, int, error)
}
