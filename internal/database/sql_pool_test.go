// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
)

type stubConnector struct{}

func (stubConnector) Connect(context.Context) (driver.Conn, error) { return stubConn{}, nil }
func (stubConnector) Driver() driver.Driver                        { return nil }

type stubConn struct{}

func (stubConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (stubConn) Close() error                        { return nil }
func (stubConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

// A burst of concurrent queries must leave all its connections idle (up to
// MaxOpenConns) instead of closing all but MaxIdleConns, which would force a
// reconnect + auth handshake on the next burst.
func TestConfigureSQLPool_KeepsBurstConnectionsIdle(t *testing.T) {
	db := sql.OpenDB(stubConnector{})
	defer func() { _ = db.Close() }()

	configureSQLPool(db, DatabaseConfig{MaxOpenConns: 8, MaxIdleConns: 2, ConnMaxLifetimeMinutes: 5})

	ctx := context.Background()
	conns := make([]*sql.Conn, 0, 8)

	for range 8 {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}

		conns = append(conns, c)
	}

	for _, c := range conns {
		_ = c.Close()
	}

	if idle := db.Stats().Idle; idle != 8 {
		t.Fatalf("idle connections after a burst = %d, want 8", idle)
	}
}
