// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package metrics

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestDBPoolCollector(t *testing.T) {
	sqlStats := SQLPoolStats(sql.DBStats{
		MaxOpenConnections: 20, OpenConnections: 7, InUse: 3, Idle: 4,
		WaitCount: 5, WaitDuration: 1500 * time.Millisecond,
		MaxIdleClosed: 1, MaxIdleTimeClosed: 2, MaxLifetimeClosed: 4,
	})

	c := NewDBPoolCollector(
		PoolSource{Name: PoolSQL, Stats: func() PoolStats { return sqlStats }},
		PoolSource{Name: PoolPgx, Stats: func() PoolStats {
			return PoolStats{
				Max: 30, Open: 10, InUse: 6, Idle: 4, Waits: 9, WaitSeconds: 2.5,
				ClosedMaxLifetime: 1, ClosedMaxIdle: 2,
				Acquire: &AcquireStats{Total: 100, Seconds: 4.25, Canceled: 3, Created: 12},
			}
		}},
	)

	want := `
# HELP openartifact_db_pool_acquire_wait_seconds_total Cumulative time acquisitions spent waiting for a connection.
# TYPE openartifact_db_pool_acquire_wait_seconds_total counter
openartifact_db_pool_acquire_wait_seconds_total{pool="pgx"} 2.5
openartifact_db_pool_acquire_wait_seconds_total{pool="sql"} 1.5
# HELP openartifact_db_pool_acquire_waits_total Connection acquisitions that had to wait because every connection was in use.
# TYPE openartifact_db_pool_acquire_waits_total counter
openartifact_db_pool_acquire_waits_total{pool="pgx"} 9
openartifact_db_pool_acquire_waits_total{pool="sql"} 5
# HELP openartifact_db_pool_connections Established database connections by state: in_use (checked out) or idle.
# TYPE openartifact_db_pool_connections gauge
openartifact_db_pool_connections{pool="pgx",state="idle"} 4
openartifact_db_pool_connections{pool="pgx",state="in_use"} 6
openartifact_db_pool_connections{pool="sql",state="idle"} 4
openartifact_db_pool_connections{pool="sql",state="in_use"} 3
# HELP openartifact_db_pool_connections_closed_total Connections closed by the pool, by reason: max_lifetime or max_idle (connection churn).
# TYPE openartifact_db_pool_connections_closed_total counter
openartifact_db_pool_connections_closed_total{pool="pgx",reason="max_idle"} 2
openartifact_db_pool_connections_closed_total{pool="pgx",reason="max_lifetime"} 1
openartifact_db_pool_connections_closed_total{pool="sql",reason="max_idle"} 3
openartifact_db_pool_connections_closed_total{pool="sql",reason="max_lifetime"} 4
# HELP openartifact_db_pool_max_connections Configured connection limit of the pool.
# TYPE openartifact_db_pool_max_connections gauge
openartifact_db_pool_max_connections{pool="pgx"} 30
openartifact_db_pool_max_connections{pool="sql"} 20
# HELP openartifact_db_pool_open_connections Database connections that exist (in use, idle, and for the pgx pool those being opened).
# TYPE openartifact_db_pool_open_connections gauge
openartifact_db_pool_open_connections{pool="pgx"} 10
openartifact_db_pool_open_connections{pool="sql"} 7
`

	err := testutil.CollectAndCompare(c, strings.NewReader(want),
		"openartifact_db_pool_acquire_wait_seconds_total",
		"openartifact_db_pool_acquire_waits_total",
		"openartifact_db_pool_connections",
		"openartifact_db_pool_connections_closed_total",
		"openartifact_db_pool_max_connections",
		"openartifact_db_pool_open_connections",
	)
	if err != nil {
		t.Error(err)
	}

	// The acquire counters exist for the pgx pool only.
	wantAcquire := `
# HELP openartifact_db_pool_acquire_canceled_total Acquisitions abandoned because the caller's context ended while waiting (pgx pool only).
# TYPE openartifact_db_pool_acquire_canceled_total counter
openartifact_db_pool_acquire_canceled_total{pool="pgx"} 3
# HELP openartifact_db_pool_acquire_seconds_total Cumulative time spent acquiring connections, including waits (pgx pool only).
# TYPE openartifact_db_pool_acquire_seconds_total counter
openartifact_db_pool_acquire_seconds_total{pool="pgx"} 4.25
# HELP openartifact_db_pool_acquires_total Successful connection acquisitions (pgx pool only; database/sql does not count them).
# TYPE openartifact_db_pool_acquires_total counter
openartifact_db_pool_acquires_total{pool="pgx"} 100
# HELP openartifact_db_pool_connections_created_total Connections the pool has opened (pgx pool only).
# TYPE openartifact_db_pool_connections_created_total counter
openartifact_db_pool_connections_created_total{pool="pgx"} 12
`

	err = testutil.CollectAndCompare(c, strings.NewReader(wantAcquire),
		"openartifact_db_pool_acquire_canceled_total",
		"openartifact_db_pool_acquire_seconds_total",
		"openartifact_db_pool_acquires_total",
		"openartifact_db_pool_connections_created_total",
	)
	if err != nil {
		t.Error(err)
	}
}

// The collector reads the pools at scrape time: a later change shows up on the
// next gather without any refresh call.
func TestDBPoolCollectorReadsAtScrape(t *testing.T) {
	inUse := int64(1)
	c := NewDBPoolCollector(PoolSource{Name: PoolSQL, Stats: func() PoolStats { return PoolStats{InUse: inUse} }})

	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	value := func() float64 {
		t.Helper()

		mfs, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}

		for _, mf := range mfs {
			if mf.GetName() != "openartifact_db_pool_connections" {
				continue
			}

			for _, m := range mf.GetMetric() {
				for _, l := range m.GetLabel() {
					if l.GetName() == "state" && l.GetValue() == "in_use" {
						return m.GetGauge().GetValue()
					}
				}
			}
		}

		t.Fatal("in_use series not found")

		return 0
	}

	if got := value(); got != 1 {
		t.Fatalf("in_use = %v, want 1", got)
	}

	inUse = 5

	if got := value(); got != 5 {
		t.Fatalf("in_use after change = %v, want 5", got)
	}
}

// PgxPoolStats maps a real (idle, never connected) pool without panicking.
func TestPgxPoolStats(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:1/db?pool_max_conns=7")
	if err != nil {
		t.Fatal(err)
	}

	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	s := PgxPoolStats(pool.Stat())
	if s.Max != 7 || s.Open != 0 || s.InUse != 0 || s.Idle != 0 {
		t.Errorf("stats = %+v, want max 7 and no connections", s)
	}

	if s.Acquire == nil {
		t.Error("pgx stats must carry the acquire counters")
	}
}
