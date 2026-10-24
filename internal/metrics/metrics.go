// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

//nolint:gochecknoglobals // immutable Prometheus histogram buckets
var httpDurationBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

// Registry holds all Prometheus metric variables for openartifact.
type Registry struct {
	HTTPRequestsTotal                   *prometheus.CounterVec
	HTTPRequestDurationSeconds          *prometheus.HistogramVec
	UploadsTotal                        *prometheus.CounterVec
	BlobBytesTotal                      *prometheus.CounterVec
	BlobOperationDurationSeconds        *prometheus.HistogramVec
	BlobDownloadsTotal                  *prometheus.CounterVec
	MetadataRegenerationsTotal          *prometheus.CounterVec
	MetadataRegenerationDurationSeconds *prometheus.HistogramVec
	AuditDroppedTotal                   prometheus.Counter
	TrashPurgedTotal                    *prometheus.CounterVec

	// Audit log retention (audit.retentionDays): rows deleted past the window
	// and when the last pass finished.
	AuditRetentionDeletedTotal   prometheus.Counter
	AuditRetentionLastRunSeconds prometheus.Gauge

	// Storage tiering. The tier/from/to labels are tier names: a handful of
	// admin-defined values, never blob or repository identifiers.
	TierBlobs           *prometheus.GaugeVec
	TierBytes           *prometheus.GaugeVec
	TierReadsTotal      *prometheus.CounterVec
	TierMovesTotal      *prometheus.CounterVec
	TierMovedBytesTotal *prometheus.CounterVec
	TierMovesPending    prometheus.Gauge

	// Deferred blob deletion: bytes of deleted blobs and retired tier copies
	// wait in a queue until the grace period has passed and no running backup
	// can still need them.
	BlobDeletionsPending prometheus.Gauge
	BlobDeletionsSwept   *prometheus.CounterVec

	// Replication holds the collectors of async blob replication to a replica.
	Replication *ReplicationMetrics

	// ScannerInstallAttemptsTotal counts runtime scanner installs. The scanner
	// label is trivy or grype; result is one of a fixed set (see internal/vuln).
	ScannerInstallAttemptsTotal *prometheus.CounterVec

	// ScannerDBUpdateAttemptsTotal counts vulnerability database refreshes by
	// scanner (trivy, grype) and result (success, failure);
	// ScannerDBLastSuccessTimestamp is when each last succeeded (or, after a
	// restart, the age of the database on disk), in Unix seconds.
	ScannerDBUpdateAttemptsTotal  *prometheus.CounterVec
	ScannerDBLastSuccessTimestamp *prometheus.GaugeVec

	// Recovery holds the disaster-recovery row journal metrics.
	Recovery RecoveryMetrics
}

// NewRegistry creates all metrics, registers them on the default Prometheus
// registry, and returns the populated Registry struct.
func NewRegistry() *Registry {
	return NewRegistryOn(prometheus.DefaultRegisterer)
}

// NewRegistryOn is NewRegistry with an explicit Prometheus registerer, so tests
// can build a Registry (and read it back) without touching the process-wide
// default registry, where a second registration would panic.
func NewRegistryOn(reg prometheus.Registerer) *Registry {
	r := &Registry{
		HTTPRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_http_requests_total",
			Help: "Total number of HTTP requests by method, route template (never the raw URL) and status.",
		}, []string{"method", "path", "status"}),

		HTTPRequestDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "openartifact_http_request_duration_seconds",
			Help:    "HTTP request latency in seconds.",
			Buckets: httpDurationBuckets,
		}, []string{"method", "path"}),

		UploadsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_uploads_total",
			Help: "Hosted upload requests by package format and result (success, error).",
		}, []string{"format", "result"}),

		BlobBytesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_blob_bytes_total",
			Help: "Total bytes transferred to/from the blob store.",
		}, []string{"operation"}),

		// Latency of the object store itself, as seen by the server: the
		// floor under every upload. For Put it covers reading the client's
		// body, so large uploads are bounded by the client's speed; for Get it
		// is the time to open the object, not to stream it.
		//
		//	histogram_quantile(0.95, sum by (le) (rate(openartifact_blob_operation_duration_seconds_bucket{operation="put"}[5m])))
		BlobOperationDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "openartifact_blob_operation_duration_seconds",
			Help:    "Blob store operation latency by operation (put, get, exists, exists_by_sha256, delete, size, metadata) and result (ok, error).",
			Buckets: []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 10, 60},
		}, []string{"operation", "result"}),

		BlobDownloadsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_blob_downloads_total",
			Help: "Blob download GET requests by delivery mode: mode=redirect (307 to a presigned URL) or mode=stream (served by this server); reason says why a download was streamed.",
		}, []string{"mode", "reason"}),

		MetadataRegenerationsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_metadata_regenerations_total",
			Help: "Total number of metadata regeneration cycles completed.",
		}, []string{"format"}),

		MetadataRegenerationDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "openartifact_metadata_regeneration_duration_seconds",
			Help:    "Wall-clock time of metadata regeneration cycles.",
			Buckets: httpDurationBuckets,
		}, []string{"format"}),

		AuditDroppedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openartifact_audit_dropped_total",
			Help: "Total number of audit events dropped due to backpressure.",
		}),

		TrashPurgedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_trash_purged_total",
			Help: "Total number of trashed items permanently removed by the retention purge, by kind.",
		}, []string{"kind"}),

		AuditRetentionDeletedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openartifact_audit_retention_deleted_total",
			Help: "Total number of audit log entries deleted for being older than audit.retentionDays.",
		}),

		AuditRetentionLastRunSeconds: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openartifact_audit_retention_last_run_timestamp_seconds",
			Help: "Unix time the last audit retention pass finished without error on this replica (a pass that found nothing to delete counts).",
		}),

		TierBlobs: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "openartifact_storage_tier_blobs",
			Help: "Blobs placed on each storage tier, refreshed by the tier mover.",
		}, []string{"tier"}),

		TierBytes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "openartifact_storage_tier_bytes",
			Help: "Bytes of blobs placed on each storage tier, refreshed by the tier mover.",
		}, []string{"tier"}),

		TierReadsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_storage_tier_reads_total",
			Help: "Blob reads served from each storage tier.",
		}, []string{"tier"}),

		TierMovesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_storage_tier_moves_total",
			Help: "Blob moves between storage tiers by outcome (moved, failed, skipped).",
		}, []string{"from", "to", "result"}),

		TierMovedBytesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_storage_tier_moved_bytes_total",
			Help: "Bytes of blobs moved between storage tiers.",
		}, []string{"from", "to"}),

		TierMovesPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openartifact_storage_tier_moves_pending",
			Help: "Tier moves left in the journal (interrupted or failed, awaiting recovery).",
		}),

		BlobDeletionsPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openartifact_blob_deletions_pending",
			Help: "Blob copies queued for deletion (in their grace period, or held back by a running backup).",
		}),
		BlobDeletionsSwept: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_blob_deletions_swept_total",
			Help: "Queued blob deletions the sweeper finished, by result: deleted (bytes removed) or kept (the blob was re-uploaded or the copy is authoritative again).",
		}, []string{"result"}),

		Replication: NewReplicationMetrics(),

		ScannerInstallAttemptsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_scanner_install_attempts_total",
			Help: "Runtime installs of the Trivy and Grype binaries, by scanner and result (installed, reused, unsupported_platform, download_failed, checksum_mismatch, extract_failed, noexec, insecure_location, failed).",
		}, []string{"scanner", "result"}),

		ScannerDBUpdateAttemptsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_scanner_db_update_attempts_total",
			Help: "Vulnerability database refreshes of the Trivy and Grype scanners, by scanner and result (success, failure).",
		}, []string{"scanner", "result"}),

		ScannerDBLastSuccessTimestamp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "openartifact_scanner_db_last_success_timestamp_seconds",
			Help: "Unix time of the last successful vulnerability database refresh (or the database's age on disk after a restart), by scanner.",
		}, []string{"scanner"}),
	}

	reg.MustRegister(
		r.HTTPRequestsTotal,
		r.HTTPRequestDurationSeconds,
		r.UploadsTotal,
		r.BlobBytesTotal,
		r.BlobOperationDurationSeconds,
		r.BlobDownloadsTotal,
		r.MetadataRegenerationsTotal,
		r.MetadataRegenerationDurationSeconds,
		r.AuditDroppedTotal,
		r.TrashPurgedTotal,
		r.AuditRetentionDeletedTotal,
		r.AuditRetentionLastRunSeconds,
		r.TierBlobs,
		r.TierBytes,
		r.TierReadsTotal,
		r.TierMovesTotal,
		r.TierMovedBytesTotal,
		r.TierMovesPending,
		r.BlobDeletionsPending,
		r.BlobDeletionsSwept,
		r.ScannerInstallAttemptsTotal,
		r.ScannerDBUpdateAttemptsTotal,
		r.ScannerDBLastSuccessTimestamp,
	)
	reg.MustRegister(r.Replication.Collectors()...)

	r.Recovery = newRecoveryMetrics(reg)

	return r
}
