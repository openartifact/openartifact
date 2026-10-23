// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openartifact/openartifact/internal/telemetry"
)

const defaultPostgresPort = 5432

// ErrInvalidConnOption is returned when a database option key is not a valid
// PostgreSQL runtime parameter name.
var ErrInvalidConnOption = errors.New("invalid database option key")

var connOptionKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

// quoteConnValue quotes v for a libpq-style key/value connection string so a
// password or option containing spaces, quotes or backslashes can neither break
// the string nor inject additional keywords.
func quoteConnValue(v string) string {
	var b strings.Builder

	b.Grow(len(v) + 2)
	b.WriteByte('\'')

	for _, r := range v {
		if r == '\\' || r == '\'' {
			b.WriteByte('\\')
		}

		b.WriteRune(r)
	}

	b.WriteByte('\'')

	return b.String()
}

// connString builds the key/value connection string for cfg with every value
// quoted. Options keys are validated; they are emitted in sorted order so the
// result is deterministic.
func connString(cfg DatabaseConfig) (string, error) {
	port := cfg.Port
	if port == 0 {
		port = defaultPostgresPort
	}

	parts := []string{
		"host=" + quoteConnValue(cfg.Host),
		"port=" + strconv.Itoa(port),
		"user=" + quoteConnValue(cfg.User),
		"password=" + quoteConnValue(cfg.Password),
		"dbname=" + quoteConnValue(cfg.Database),
	}

	if cfg.SSLMode != "" {
		parts = append(parts, "sslmode="+quoteConnValue(cfg.SSLMode))
	}

	keys := make([]string, 0, len(cfg.Options))
	for k := range cfg.Options {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		if !connOptionKeyRe.MatchString(k) {
			return "", fmt.Errorf("%w: %q", ErrInvalidConnOption, k)
		}

		parts = append(parts, k+"="+quoteConnValue(cfg.Options[k]))
	}

	return strings.Join(parts, " "), nil
}

// poolConfig parses cfg into a pgxpool configuration honouring the configured
// connection limits (DB_MAX_OPEN_CONNS, DB_MAX_IDLE_CONNS and
// DB_CONN_MAX_LIFETIME_MINUTES). The same pgx connection settings back both
// the pgx pool and the database/sql handle, so each of them is bounded by
// MaxOpenConns.
func poolConfig(cfg DatabaseConfig) (*pgxpool.Config, error) {
	dsn, err := connString(cfg)
	if err != nil {
		return nil, err
	}

	pc, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database config: %w", err)
	}

	// Statement-name spans under a request span; inert while telemetry is off.
	// Both the pgx pool and the database/sql handle are built from this config.
	pc.ConnConfig.Tracer = telemetry.DBTracer{}

	if cfg.MaxOpenConns > 0 {
		pc.MaxConns = int32(min(cfg.MaxOpenConns, 1<<30))
	}

	if cfg.MaxIdleConns > 0 {
		pc.MinConns = min(int32(min(cfg.MaxIdleConns, 1<<30)), pc.MaxConns)
	}

	if cfg.ConnMaxLifetimeMinutes > 0 {
		pc.MaxConnLifetime = time.Duration(cfg.ConnMaxLifetimeMinutes) * time.Minute
	}

	return pc, nil
}
