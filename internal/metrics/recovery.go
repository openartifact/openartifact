// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package metrics

import "github.com/prometheus/client_golang/prometheus"

// RecoveryMetrics are the disaster-recovery row journal metrics (see
// docs/operations/rebuild-from-storage.md). Labels are fixed small sets.
type RecoveryMetrics struct {
	// JournalBacklog is the number of changed rows waiting for their sidecar.
	JournalBacklog prometheus.Gauge
	// OldestDirtyAgeSeconds is the age of the oldest waiting mark (0 when none).
	OldestDirtyAgeSeconds prometheus.Gauge
	// SidecarsWrittenTotal counts sidecar blobs written.
	SidecarsWrittenTotal prometheus.Counter
	// SidecarsDeletedTotal counts sidecar blobs deleted (row gone or superseded).
	SidecarsDeletedTotal prometheus.Counter
	// FailuresTotal counts journal operations that failed and will be retried.
	FailuresTotal prometheus.Counter
	// ReconcileMarkedTotal counts rows the reconcile marked dirty (backfill and stale sweeps).
	ReconcileMarkedTotal prometheus.Counter
}

// newRecoveryMetrics builds the journal metrics and registers them on reg.
func newRecoveryMetrics(reg prometheus.Registerer) RecoveryMetrics {
	m := RecoveryMetrics{
		JournalBacklog: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openartifact_recovery_journal_backlog",
			Help: "Changed rows waiting for their disaster-recovery sidecar to be written or deleted.",
		}),
		OldestDirtyAgeSeconds: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openartifact_recovery_oldest_dirty_age_seconds",
			Help: "Age of the oldest changed row still waiting for its sidecar (0 when the journal is drained).",
		}),
		SidecarsWrittenTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openartifact_recovery_sidecars_written_total",
			Help: "Disaster-recovery sidecar blobs written to the blob store.",
		}),
		SidecarsDeletedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openartifact_recovery_sidecars_deleted_total",
			Help: "Disaster-recovery sidecar blobs deleted (their row is gone, or a newer sidecar superseded them).",
		}),
		FailuresTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openartifact_recovery_failures_total",
			Help: "Journal operations that failed and will be retried with backoff.",
		}),
		ReconcileMarkedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openartifact_recovery_reconcile_marked_total",
			Help: "Rows the journal reconcile marked dirty (rows without a sidecar, sidecars without a row).",
		}),
	}

	reg.MustRegister(m.JournalBacklog, m.OldestDirtyAgeSeconds, m.SidecarsWrittenTotal,
		m.SidecarsDeletedTotal, m.FailuresTotal, m.ReconcileMarkedTotal)

	return m
}
