// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package formats_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/pkg/logger"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// migratedDB creates an empty migrated database on the TEST_DATABASE_URL server,
// dropped when the test ends.
func migratedDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}

	t.Cleanup(func() { _ = admin.Close() })

	name := fmt.Sprintf("oa_formats_%d", os.Getpid())

	if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE `+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}

	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()

	log, err := logger.New("warn", "json", "stderr")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}

	pg, err := database.NewPostgresDB(database.DatabaseConfig{
		Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pw,
		Database: name, SSLMode: "disable", MaxOpenConns: 4, MaxIdleConns: 2, ConnMaxLifetimeMinutes: 5,
	}, log)
	if err != nil {
		t.Fatalf("NewPostgresDB: %v", err)
	}

	t.Cleanup(func() { _ = pg.Close() })

	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	return pg.GetDB()
}

// TestDescriptorExpressionsAreValidSQL runs the expressions the trash and
// cleanup engines splice into their queries, of every registered descriptor,
// against the real schema: a typo in one format would otherwise fail at the
// first trash, restore or cleanup run of a repository of that format.
func TestDescriptorExpressionsAreValidSQL(t *testing.T) {
	db := migratedDB(t)

	const from = ` FROM package_files f
	  JOIN package_versions v ON v.id = f.version_id
	  JOIN packages p ON p.id = v.package_id LIMIT 0`

	for _, name := range storage.Names() {
		st, _ := storage.Lookup(name)

		queries := map[string]string{
			"trash listing": `SELECT ` + st.Trash.NameSQL() + `, ` + st.Trash.VersionSQL() + `, ` + st.Trash.RegenKeysSQL() + from,
			"cleanup rank": `SELECT ` + strings.Join(st.Cleanup.PartitionExprs(), ", ") + `, ` + st.Cleanup.VersionSQL() +
				`, ` + st.Cleanup.ProtectSQL() + from,
		}

		for what, q := range queries {
			if _, err := db.ExecContext(context.Background(), q); err != nil {
				t.Errorf("%s: %s: %v\n%s", name, what, err, q)
			}
		}
	}
}
