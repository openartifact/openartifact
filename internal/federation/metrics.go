// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are the Prometheus series of federation, all labelled repo and peer
// (both bounded by the operator's configuration, never by client input).
//
// The counters are incremented by the pull workers; the gauges are computed at
// scrape time from the peers' stored state, so they are right on every
// replica and after a restart.
type Metrics struct {
	Applied    *prometheus.CounterVec
	Conflicts  *prometheus.CounterVec
	PullErrors *prometheus.CounterVec
	// Resumed counts blob transfers continued with a Range request from a
	// partial download instead of restarted.
	Resumed *prometheus.CounterVec
	// partialBytes reads the size of the engine's partial downloads at scrape
	// time; it is bound when the engine is built.
	partialBytes atomic.Pointer[func() int64]
	collector    *peerCollector
	registerers  []prometheus.Collector
}

// peerGaugeQueryTimeout bounds the database read a scrape performs.
const peerGaugeQueryTimeout = 5 * time.Second

// NewMetrics creates the federation metrics and registers them on reg. The
// gauges read through store.
func NewMetrics(reg prometheus.Registerer, store *Store) (*Metrics, error) {
	m := &Metrics{
		Applied: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_federation_applied_total",
			Help: "Log entries pulled from a peer, by result: applied, skipped (already up to date), deferred (set aside for retry) or failed.",
		}, []string{"repo", "peer", "result"}),
		Conflicts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_federation_conflicts_total",
			Help: "Local changes superseded by a different state of the same unit from a peer (last writer wins).",
		}, []string{"repo", "peer"}),
		PullErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_federation_pull_errors_total",
			Help: "Pull attempts of a peer that ended in an error.",
		}, []string{"repo", "peer"}),
	}

	m.Resumed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "openartifact_federation_resumed_total",
		Help: "Blob transfers from a peer resumed with a Range request after the connection dropped (or from a partial download of an earlier pull).",
	}, []string{"repo", "peer"})

	partial := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "openartifact_federation_partial_bytes",
		Help: "Bytes of interrupted blob downloads kept in the federation spool for resuming.",
	}, func() float64 {
		if fn := m.partialBytes.Load(); fn != nil {
			return float64((*fn)())
		}

		return 0
	})

	m.collector = &peerCollector{
		store: store,
		lag: prometheus.NewDesc("openartifact_federation_lag_seconds",
			"Age of the oldest change of a peer that has not been applied yet (0 when caught up).", []string{"repo", "peer"}, nil),
		backlog: prometheus.NewDesc("openartifact_federation_backlog_units",
			"Log positions of a peer that have not been pulled yet.", []string{"repo", "peer"}, nil),
		lastSuccess: prometheus.NewDesc("openartifact_federation_last_success_timestamp_seconds",
			"Unix time of the last successful pull of a peer (0 when it never succeeded).", []string{"repo", "peer"}, nil),
	}

	m.registerers = []prometheus.Collector{m.Applied, m.Conflicts, m.PullErrors, m.Resumed, partial, m.collector}

	for _, c := range m.registerers {
		if err := reg.Register(c); err != nil {
			return nil, err //nolint:wrapcheck // registration error is self-explanatory
		}
	}

	return m, nil
}

type peerCollector struct {
	store                     *Store
	lag, backlog, lastSuccess *prometheus.Desc
}

func (c *peerCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.lag

	ch <- c.backlog

	ch <- c.lastSuccess
}

func (c *peerCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), peerGaugeQueryTimeout)
	defer cancel()

	rows, err := c.store.PeerMetrics(ctx)
	if err != nil {
		return
	}

	for _, r := range rows {
		ch <- prometheus.MustNewConstMetric(c.lag, prometheus.GaugeValue, r.Lag.Seconds(), r.Repo, r.Peer)

		ch <- prometheus.MustNewConstMetric(c.backlog, prometheus.GaugeValue, float64(r.Backlog), r.Repo, r.Peer)

		last := 0.0
		if r.LastSuccess != nil {
			last = float64(r.LastSuccess.Unix())
		}

		ch <- prometheus.MustNewConstMetric(c.lastSuccess, prometheus.GaugeValue, last, r.Repo, r.Peer)
	}
}

// PeerMetric is one peer's gauge values.
type PeerMetric struct {
	Repo        string
	Peer        string
	Lag         time.Duration
	Backlog     int64
	LastSuccess *time.Time
}

// PeerMetrics returns the gauge values of every enabled peer.
func (s *Store) PeerMetrics(ctx context.Context) ([]PeerMetric, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT r.name, p.name, GREATEST(p.remote_head_seq - p.cursor_seq, 0),
		        CASE WHEN p.remote_head_seq > p.cursor_seq AND p.oldest_pending_at IS NOT NULL
		             THEN GREATEST(extract(epoch FROM now() - p.oldest_pending_at), 0) ELSE 0 END,
		        p.last_success_at
		   FROM federation_peers p JOIN repositories r ON r.id = p.repo_id
		  WHERE p.enabled`)
	if err != nil {
		return nil, err //nolint:wrapcheck // read by the collector only
	}
	defer rows.Close()

	var out []PeerMetric

	for rows.Next() {
		var (
			m    PeerMetric
			secs float64
		)

		if err := rows.Scan(&m.Repo, &m.Peer, &m.Backlog, &secs, &m.LastSuccess); err != nil {
			return nil, err //nolint:wrapcheck // read by the collector only
		}

		m.Lag = time.Duration(secs * float64(time.Second))

		out = append(out, m)
	}

	return out, rows.Err() //nolint:wrapcheck // read by the collector only
}
