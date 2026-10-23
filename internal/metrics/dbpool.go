// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package metrics

import (
	"database/sql"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Pool label values of the openartifact_db_pool_* metrics. The server holds two
// connection pools to the same database: the pgx pool the sqlc queries run on
// and the database/sql pool (per-request authentication lookups, migrations,
// workers).
const (
	PoolPgx = "pgx"
	PoolSQL = "sql"
)

// PoolStats is a point-in-time reading of one connection pool, in the units of
// the exported metrics.
type PoolStats struct {
	// Max is the configured connection limit, Open the connections that
	// exist (in use + idle, and for pgx those being built), InUse and Idle the
	// split of the established ones.
	Max, Open, InUse, Idle int64

	// Waits is how many acquisitions had to wait for a connection, WaitSeconds
	// the time they spent waiting. Both are cumulative.
	Waits       int64
	WaitSeconds float64

	// ClosedMaxLifetime and ClosedMaxIdle count connections the pool closed
	// because they outlived the lifetime or idle limit (connection churn).
	ClosedMaxLifetime, ClosedMaxIdle int64

	// Acquire is only reported by pools that count acquisitions (pgx).
	Acquire *AcquireStats
}

// AcquireStats are the counters only pgxpool keeps.
type AcquireStats struct {
	// Total acquisitions, and the time spent in the successful ones.
	Total   int64
	Seconds float64
	// Canceled acquisitions (the caller's context ended while waiting).
	Canceled int64
	// Created is how many connections the pool has opened.
	Created int64
}

// PgxPoolStats converts a pgxpool reading. Waits are the pool's empty acquires:
// acquisitions that found no idle connection and had to wait or open one.
func PgxPoolStats(s *pgxpool.Stat) PoolStats {
	return PoolStats{
		Max:               int64(s.MaxConns()),
		Open:              int64(s.TotalConns()),
		InUse:             int64(s.AcquiredConns()),
		Idle:              int64(s.IdleConns()),
		Waits:             s.EmptyAcquireCount(),
		WaitSeconds:       s.EmptyAcquireWaitTime().Seconds(),
		ClosedMaxLifetime: s.MaxLifetimeDestroyCount(),
		ClosedMaxIdle:     s.MaxIdleDestroyCount(),
		Acquire: &AcquireStats{
			Total:    s.AcquireCount(),
			Seconds:  s.AcquireDuration().Seconds(),
			Canceled: s.CanceledAcquireCount(),
			Created:  s.NewConnsCount(),
		},
	}
}

// SQLPoolStats converts a database/sql reading.
func SQLPoolStats(s sql.DBStats) PoolStats {
	return PoolStats{
		Max:               int64(s.MaxOpenConnections),
		Open:              int64(s.OpenConnections),
		InUse:             int64(s.InUse),
		Idle:              int64(s.Idle),
		Waits:             s.WaitCount,
		WaitSeconds:       s.WaitDuration.Seconds(),
		ClosedMaxLifetime: s.MaxLifetimeClosed,
		ClosedMaxIdle:     s.MaxIdleClosed + s.MaxIdleTimeClosed,
	}
}

// PoolSource is one named connection pool and the function that reads it.
type PoolSource struct {
	Name  string
	Stats func() PoolStats
}

// DBPoolCollector exports connection pool statistics. It reads the pools at
// scrape time, so there is no polling goroutine and nothing to keep in sync.
type DBPoolCollector struct {
	sources []PoolSource

	connections     *prometheus.Desc
	open            *prometheus.Desc
	max             *prometheus.Desc
	waits           *prometheus.Desc
	waitSeconds     *prometheus.Desc
	closed          *prometheus.Desc
	acquires        *prometheus.Desc
	acquireSeconds  *prometheus.Desc
	acquireCanceled *prometheus.Desc
	created         *prometheus.Desc
}

// NewDBPoolCollector builds a collector over the given pools; the pool label of
// each series is the source's name.
func NewDBPoolCollector(sources ...PoolSource) *DBPoolCollector {
	labels := []string{"pool"}

	return &DBPoolCollector{
		sources: sources,
		connections: prometheus.NewDesc("openartifact_db_pool_connections",
			"Established database connections by state: in_use (checked out) or idle.",
			[]string{"pool", "state"}, nil),
		open: prometheus.NewDesc("openartifact_db_pool_open_connections",
			"Database connections that exist (in use, idle, and for the pgx pool those being opened).", labels, nil),
		max: prometheus.NewDesc("openartifact_db_pool_max_connections",
			"Configured connection limit of the pool.", labels, nil),
		waits: prometheus.NewDesc("openartifact_db_pool_acquire_waits_total",
			"Connection acquisitions that had to wait because every connection was in use.", labels, nil),
		waitSeconds: prometheus.NewDesc("openartifact_db_pool_acquire_wait_seconds_total",
			"Cumulative time acquisitions spent waiting for a connection.", labels, nil),
		closed: prometheus.NewDesc("openartifact_db_pool_connections_closed_total",
			"Connections closed by the pool, by reason: max_lifetime or max_idle (connection churn).",
			[]string{"pool", "reason"}, nil),
		acquires: prometheus.NewDesc("openartifact_db_pool_acquires_total",
			"Successful connection acquisitions (pgx pool only; database/sql does not count them).", labels, nil),
		acquireSeconds: prometheus.NewDesc("openartifact_db_pool_acquire_seconds_total",
			"Cumulative time spent acquiring connections, including waits (pgx pool only).", labels, nil),
		acquireCanceled: prometheus.NewDesc("openartifact_db_pool_acquire_canceled_total",
			"Acquisitions abandoned because the caller's context ended while waiting (pgx pool only).", labels, nil),
		created: prometheus.NewDesc("openartifact_db_pool_connections_created_total",
			"Connections the pool has opened (pgx pool only).", labels, nil),
	}
}

// Describe implements prometheus.Collector.
func (c *DBPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		c.connections, c.open, c.max, c.waits, c.waitSeconds, c.closed,
		c.acquires, c.acquireSeconds, c.acquireCanceled, c.created,
	} {
		ch <- d
	}
}

// Collect implements prometheus.Collector.
func (c *DBPoolCollector) Collect(ch chan<- prometheus.Metric) {
	for _, src := range c.sources {
		s := src.Stats()
		pool := src.Name

		gauge := func(d *prometheus.Desc, v int64, extra ...string) {
			ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, float64(v), append([]string{pool}, extra...)...)
		}
		counter := func(d *prometheus.Desc, v float64, extra ...string) {
			ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, v, append([]string{pool}, extra...)...)
		}

		gauge(c.connections, s.InUse, "in_use")
		gauge(c.connections, s.Idle, "idle")
		gauge(c.open, s.Open)
		gauge(c.max, s.Max)
		counter(c.waits, float64(s.Waits))
		counter(c.waitSeconds, s.WaitSeconds)
		counter(c.closed, float64(s.ClosedMaxLifetime), "max_lifetime")
		counter(c.closed, float64(s.ClosedMaxIdle), "max_idle")

		if a := s.Acquire; a != nil {
			counter(c.acquires, float64(a.Total))
			counter(c.acquireSeconds, a.Seconds)
			counter(c.acquireCanceled, float64(a.Canceled))
			counter(c.created, float64(a.Created))
		}
	}
}

// RegisterDBPools exports the two connection pools on the default registry.
func RegisterDBPools(pgx func() *pgxpool.Stat, db func() sql.DBStats) {
	prometheus.MustRegister(NewDBPoolCollector(
		PoolSource{Name: PoolPgx, Stats: func() PoolStats { return PgxPoolStats(pgx()) }},
		PoolSource{Name: PoolSQL, Stats: func() PoolStats { return SQLPoolStats(db()) }},
	))
}
