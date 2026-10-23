// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package federation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/pkg/logger"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

var dbSeq atomic.Int64 //nolint:gochecknoglobals // unique database names within the test binary

// freshDB creates an empty database on the TEST_DATABASE_URL server, runs the
// embedded migrations on it and returns the PostgresDB (use .GetDB() for the
// *sql.DB). The database is dropped when the test ends.
func freshDB(t *testing.T) *database.PostgresDB {
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

	name := fmt.Sprintf("oa_fed_%d_%d", os.Getpid(), dbSeq.Add(1))

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

	sslmode := u.Query().Get("sslmode")
	if sslmode == "" {
		sslmode = "disable"
	}

	log, err := logger.New("warn", "json", "stderr")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}

	pg, err := database.NewPostgresDB(database.DatabaseConfig{
		Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pw,
		Database: name, SSLMode: sslmode, MaxOpenConns: 10, MaxIdleConns: 2, ConnMaxLifetimeMinutes: 5,
	}, log)
	if err != nil {
		t.Fatalf("NewPostgresDB: %v", err)
	}

	t.Cleanup(func() { _ = pg.Close() })

	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	return pg
}

// seedRepo inserts a hosted repository of format and returns its id.
func seedRepo(t *testing.T, db *sql.DB, name, format string) string {
	t.Helper()

	var id string

	err := db.QueryRowContext(context.Background(),
		`INSERT INTO repositories (name, format, repo_type, project_id)
		 VALUES ($1, $2, 'hosted', (SELECT id FROM projects ORDER BY created_at LIMIT 1)) RETURNING id::text`,
		name, format).Scan(&id)
	if err != nil {
		t.Fatalf("seed repository %s: %v", name, err)
	}

	return id
}

// seedBlob inserts a blobs row for content and returns its id and sha256.
func seedBlob(t *testing.T, db *sql.DB, content string) (string, string) {
	t.Helper()

	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])

	var id string

	err := db.QueryRowContext(context.Background(),
		`INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES ($1, $2, $3)
		 ON CONFLICT (sha256) DO UPDATE SET size_bytes = EXCLUDED.size_bytes RETURNING id::text`,
		sha, len(content), "blobs/"+sha).Scan(&id)
	if err != nil {
		t.Fatalf("seed blob: %v", err)
	}

	return id, sha
}

// enableLog makes repoID a federated repository (the change log starts empty;
// call Backfill via the codec to register existing rows).
func enableLog(t *testing.T, db *sql.DB, repoID string) {
	t.Helper()

	if _, err := db.ExecContext(context.Background(), `INSERT INTO federation_repos (repo_id) VALUES ($1)`, repoID); err != nil {
		t.Fatalf("enable federation log: %v", err)
	}
}

// loggedUnits returns unit_key -> "event/tombstone" of the repository's log.
func loggedUnits(t *testing.T, db *sql.DB, repoID string) map[string]string {
	t.Helper()

	rows, err := db.QueryContext(context.Background(),
		`SELECT unit_key, event || '/' || tombstone::text FROM federation_units WHERE repo_id = $1`, repoID)
	if err != nil {
		t.Fatalf("list log: %v", err)
	}
	defer rows.Close()

	out := map[string]string{}

	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatalf("scan log: %v", err)
		}

		out[k] = v
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("list log: %v", err)
	}

	return out
}
