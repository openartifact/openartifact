// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestConnStringQuotesHostileValues(t *testing.T) {
	cfg := DatabaseConfig{
		Host:     "db.example",
		Port:     6543,
		User:     `ro'ot`,
		Password: `p w\d' sslmode=disable host=evil`,
		Database: "my db",
		SSLMode:  "require",
		Options:  map[string]string{"application_name": "a b", "search_path": `x'y`},
	}

	dsn, err := connString(cfg)
	if err != nil {
		t.Fatalf("connString: %v", err)
	}

	pc, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgx.ParseConfig(%q): %v", dsn, err)
	}

	if pc.Host != "db.example" || pc.Port != 6543 {
		t.Errorf("host/port = %s:%d; a password value injected keywords", pc.Host, pc.Port)
	}

	if pc.User != cfg.User || pc.Password != cfg.Password || pc.Database != cfg.Database {
		t.Errorf("values did not round-trip: user=%q password=%q db=%q", pc.User, pc.Password, pc.Database)
	}

	if pc.RuntimeParams["application_name"] != "a b" || pc.RuntimeParams["search_path"] != `x'y` {
		t.Errorf("options did not round-trip: %v", pc.RuntimeParams)
	}
}

func TestConnStringDefaultsPortAndRejectsBadOptionKeys(t *testing.T) {
	dsn, err := connString(DatabaseConfig{Host: "h", User: "u", Database: "d"})
	if err != nil {
		t.Fatal(err)
	}

	pc, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}

	if pc.Port != 5432 {
		t.Errorf("port = %d, want the default 5432", pc.Port)
	}

	_, err = connString(DatabaseConfig{Host: "h", Options: map[string]string{"x=1 host": "evil"}})
	if !errors.Is(err, ErrInvalidConnOption) {
		t.Errorf("option key injection: err = %v, want ErrInvalidConnOption", err)
	}
}

func TestPoolConfigHonoursConfiguredLimits(t *testing.T) {
	pc, err := poolConfig(DatabaseConfig{
		Host: "h", User: "u", Database: "d",
		MaxOpenConns: 7, MaxIdleConns: 3, ConnMaxLifetimeMinutes: 11,
	})
	if err != nil {
		t.Fatal(err)
	}

	if pc.MaxConns != 7 || pc.MinConns != 3 || pc.MaxConnLifetime.Minutes() != 11 {
		t.Errorf("pool limits = max %d min %d lifetime %v, want 7 / 3 / 11m", pc.MaxConns, pc.MinConns, pc.MaxConnLifetime)
	}

	// Idle may never exceed open.
	pc, err = poolConfig(DatabaseConfig{Host: "h", User: "u", Database: "d", MaxOpenConns: 2, MaxIdleConns: 50})
	if err != nil {
		t.Fatal(err)
	}

	if pc.MinConns > pc.MaxConns {
		t.Errorf("MinConns %d > MaxConns %d", pc.MinConns, pc.MaxConns)
	}
}
