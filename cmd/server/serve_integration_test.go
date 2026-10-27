// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package main

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const strongPassword = "correct-horse-battery-staple-91"

// freshDatabase creates an empty database on the server named by
// TEST_DATABASE_URL and points the DB_* environment of the server at it.
// It returns a handle to it for inspecting what startup left behind.
func freshDatabase(t *testing.T) *sql.DB {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL not set in CI; integration services are required")
		}

		t.Skip("TEST_DATABASE_URL not set")
	}

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = admin.Close() })

	name := "oa_server_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE `+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})

	pass, _ := u.User.Password()

	resetStartupEnv(t)
	t.Setenv("DB_HOST", u.Hostname())
	t.Setenv("DB_PORT", u.Port())
	t.Setenv("DB_USER", u.User.Username())
	t.Setenv("DB_PASSWORD", pass)
	t.Setenv("DB_NAME", name)
	t.Setenv("DB_SSLMODE", "disable")
	t.Setenv("STORAGE_TYPE", "filesystem")
	t.Setenv("STORAGE_PATH", t.TempDir())
	t.Setenv("LOG_OUTPUT", filepath.Join(t.TempDir(), "server.log"))
	t.Setenv("LOG_FORMAT", "json")
	t.Setenv("SERVER_HOST", "127.0.0.1")

	u.Path = "/" + name

	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = db.Close() })

	return db
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()

	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}

	return n
}

func TestServeFailsWhenThePortIsTaken(t *testing.T) {
	db := freshDatabase(t)

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = l.Close() }()

	t.Setenv("SERVER_PORT", strconv.Itoa(l.Addr().(*net.TCPAddr).Port))
	t.Setenv("OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD", strongPassword)

	err = serve(options{})
	if err == nil {
		t.Fatal("serve succeeded on a port that is already bound")
	}

	var re reportedError
	if !errors.As(err, &re) || !strings.Contains(err.Error(), "failed to start server") {
		t.Errorf("err = %v, want a reported \"failed to start server\" error", err)
	}

	if strings.Contains(err.Error(), strongPassword) {
		t.Errorf("error leaks the bootstrap password: %v", err)
	}

	// The occupant must be untouched: still accepting.
	c, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(t.Context(), "tcp", l.Addr().String())
	if err != nil {
		t.Fatalf("the original listener was disturbed: %v", err)
	}

	_ = c.Close()

	// The schema was migrated and the admin created before the bind failed;
	// a second start must therefore work against the same database.
	if n := countRows(t, db, "auth_users"); n != 1 {
		t.Errorf("users = %d, want the single bootstrap admin", n)
	}
}

func TestServeBootstrapFailures(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) map[string]string
		leak  string
	}{
		{
			name: "password file does not exist",
			setup: func(t *testing.T) map[string]string {
				t.Helper()

				return map[string]string{"OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD_FILE": filepath.Join(t.TempDir(), "absent")}
			},
		},
		{
			name: "password file holds a too-short password",
			setup: func(t *testing.T) map[string]string {
				t.Helper()

				return map[string]string{"OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD_FILE": writeFile(t, "pw", "tiny\n")}
			},
			leak: "tiny",
		},
		{
			name: "token file holds a malformed token",
			setup: func(t *testing.T) map[string]string {
				t.Helper()

				return map[string]string{
					"OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD":   strongPassword,
					"OPENARTIFACT_BOOTSTRAP_ADMIN_TOKEN_FILE": writeFile(t, "tok", "not-a-token-"+leakProbe),
				}
			},
			leak: leakProbe,
		},
		{
			name: "negative token ttl",
			setup: func(*testing.T) map[string]string {
				return map[string]string{
					"OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD":  strongPassword,
					"OPENARTIFACT_BOOTSTRAP_ADMIN_TOKEN_TTL": "-1h",
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := freshDatabase(t)
			t.Setenv("SERVER_PORT", "18099")

			for k, v := range tc.setup(t) {
				t.Setenv(k, v)
			}

			err := serve(options{})
			if err == nil {
				t.Fatal("serve succeeded with an unusable bootstrap configuration")
			}

			if !strings.Contains(err.Error(), "bootstrap failed") {
				t.Errorf("err = %q, want \"bootstrap failed\"", err)
			}

			for _, secret := range []string{tc.leak, strongPassword} {
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Errorf("error leaks %q: %v", secret, err)
				}
			}

			// A failed bootstrap must not leave a half-created admin behind.
			if n := countRows(t, db, "auth_users"); n != 0 {
				t.Errorf("users = %d after a failed bootstrap, want 0", n)
			}
		})
	}
}

func TestServeMigrateOnlyAppliesSchemaAndStops(t *testing.T) {
	db := freshDatabase(t)

	if err := serve(options{migrate: true}); err != nil {
		t.Fatalf("serve -migrate: %v", err)
	}

	if n := countRows(t, db, "schema_migrations"); n == 0 {
		t.Error("-migrate applied no migration")
	}

	// -migrate must not run the rest of startup: no admin is bootstrapped.
	if n := countRows(t, db, "auth_users"); n != 0 {
		t.Errorf("users = %d after -migrate, want 0", n)
	}
}

func TestServeMigrateOnStartDisabledLeavesSchemaAlone(t *testing.T) {
	db := freshDatabase(t)
	t.Setenv("MIGRATE_ON_START", "false")
	t.Setenv("SERVER_PORT", "18099")

	// With no schema, the first query of startup fails instead of migrating.
	if err := serve(options{}); err == nil {
		t.Fatal("serve started against an unmigrated database with MIGRATE_ON_START=false")
	}

	var tables int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM information_schema.tables WHERE table_schema='public'`).
		Scan(&tables); err != nil {
		t.Fatal(err)
	}

	if tables != 0 {
		t.Errorf("%d tables exist; MIGRATE_ON_START=false must not migrate", tables)
	}
}
