// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package audit_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

func newMigratedDB(t *testing.T) *database.PostgresDB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL not set in CI; integration services are required")
		}

		t.Skip("TEST_DATABASE_URL not set")
	}

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}

	t.Cleanup(func() { _ = admin.Close() })

	dbName := fmt.Sprintf("oa_audit_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE `+dbName); err != nil {
		t.Fatalf("create database: %v", err)
	}

	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`)
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

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}

	pg, err := database.NewPostgresDB(database.DatabaseConfig{
		Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pw,
		Database: dbName, SSLMode: sslmode, MaxOpenConns: 2, MaxIdleConns: 2,
	}, log)
	if err != nil {
		t.Fatalf("NewPostgresDB: %v", err)
	}

	t.Cleanup(func() { _ = pg.Close() })

	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	return pg
}

// TestKnownActionsAcceptedByDatabase inserts every action in
// audit.KnownActions into a freshly migrated database: an action the
// chk_audit_action constraint rejects (SQLSTATE 23514) fails the test. Emitters
// take their action names from the audit.Action* constants listed there.
func TestKnownActionsAcceptedByDatabase(t *testing.T) {
	pg := newMigratedDB(t)
	ctx := context.Background()

	for _, action := range audit.KnownActions {
		if err := pg.InsertAuditLog(ctx, &models.AuditLog{Action: action, DetailsJSON: json.RawMessage("{}")}); err != nil {
			t.Errorf("action %q rejected by audit_logs: %v", action, err)
		}
	}

	// The constraint is still enforced: an undeclared action must be rejected.
	if err := pg.InsertAuditLog(ctx, &models.AuditLog{Action: "bogus.action", DetailsJSON: json.RawMessage("{}")}); err == nil {
		t.Error("undeclared action was accepted; chk_audit_action no longer enforced")
	}
}

func TestKnownActionsUnique(t *testing.T) {
	seen := map[string]bool{}

	for _, a := range audit.KnownActions {
		if seen[a] {
			t.Errorf("duplicate action %q in KnownActions", a)
		}

		seen[a] = true
	}
}
