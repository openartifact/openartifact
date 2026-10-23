// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package metrics

import "github.com/prometheus/client_golang/prometheus"

// ReplicationMetrics are the collectors of asynchronous blob replication
// (docs/operations/replication.md). The op label is copy or delete, the side
// label replica or primary: fixed values, never blob identifiers.
type ReplicationMetrics struct {
	// Backlog is the number of queued operations by op; OldestPendingAge the
	// age of the oldest one (the replication lag, the RPO of the replica).
	Backlog          *prometheus.GaugeVec
	OldestPendingAge prometheus.Gauge

	Replicated      prometheus.Counter
	ReplicatedBytes prometheus.Counter
	Failures        *prometheus.CounterVec
	EnqueueFailures prometheus.Counter
	// MissingPrimary counts copy operations whose blob was not on the primary.
	MissingPrimary prometheus.Counter
	FallbackReads  *prometheus.CounterVec

	VerifyChecked    prometheus.Counter
	VerifyMissing    *prometheus.CounterVec
	VerifyMismatched prometheus.Counter
	VerifyRepaired   *prometheus.CounterVec
	LastVerify       prometheus.Gauge
}

// NewReplicationMetrics creates the collectors without registering them.
func NewReplicationMetrics() *ReplicationMetrics {
	return &ReplicationMetrics{
		Backlog: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "openartifact_replication_backlog",
			Help: "Blob replication operations waiting in the queue, by op (copy, delete).",
		}, []string{"op"}),
		OldestPendingAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openartifact_replication_oldest_pending_age_seconds",
			Help: "Age of the oldest queued replication operation (the replication lag); 0 when the queue is empty.",
		}),
		Replicated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openartifact_replication_replicated_total",
			Help: "Blobs copied to the replica.",
		}),
		ReplicatedBytes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openartifact_replication_replicated_bytes_total",
			Help: "Bytes copied to the replica.",
		}),
		Failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_replication_failures_total",
			Help: "Failed replication attempts (they are retried with backoff), by op.",
		}, []string{"op"}),
		EnqueueFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openartifact_replication_enqueue_failures_total",
			Help: "Replication operations that could not be queued; the verify pass catches such blobs.",
		}),
		MissingPrimary: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openartifact_replication_missing_primary_total",
			Help: "Queued copies whose blob was not on the primary store.",
		}),
		FallbackReads: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_replication_fallback_reads_total",
			Help: "Reads answered by the replica because the primary could not, by call (get, size, exists, metadata).",
		}, []string{"call"}),
		VerifyChecked: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openartifact_replication_verify_checked_total",
			Help: "Blobs examined by verify passes.",
		}),
		VerifyMissing: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_replication_verify_missing_total",
			Help: "Blobs verify passes found missing, by side (replica, primary).",
		}, []string{"side"}),
		VerifyMismatched: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openartifact_replication_verify_mismatched_total",
			Help: "Blobs verify passes found with different sizes (or contents, with verifyDeep) on the two sides.",
		}),
		VerifyRepaired: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_replication_verify_repaired_total",
			Help: "Blobs verify passes and read repairs fixed, by side repaired (replica, primary).",
		}, []string{"side"}),
		LastVerify: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openartifact_replication_last_verify_timestamp_seconds",
			Help: "Unix time the last verify pass finished (0 = never).",
		}),
	}
}

// Collectors lists every collector for registration.
func (m *ReplicationMetrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{
		m.Backlog, m.OldestPendingAge, m.Replicated, m.ReplicatedBytes, m.Failures, m.EnqueueFailures,
		m.MissingPrimary, m.FallbackReads, m.VerifyChecked, m.VerifyMissing, m.VerifyMismatched,
		m.VerifyRepaired, m.LastVerify,
	}
}
