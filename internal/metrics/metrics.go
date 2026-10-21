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
	BlobDownloadsTotal                  *prometheus.CounterVec
	MetadataRegenerationsTotal          *prometheus.CounterVec
	MetadataRegenerationDurationSeconds *prometheus.HistogramVec
	AuditDroppedTotal                   prometheus.Counter
}

// NewRegistry creates all metrics, registers them on the default Prometheus
// registry, and returns the populated Registry struct.
func NewRegistry() *Registry {
	r := &Registry{
		HTTPRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_http_requests_total",
			Help: "Total number of HTTP requests.",
		}, []string{"method", "path", "status"}),

		HTTPRequestDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "openartifact_http_request_duration_seconds",
			Help:    "HTTP request latency in seconds.",
			Buckets: httpDurationBuckets,
		}, []string{"method", "path"}),

		UploadsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_uploads_total",
			Help: "Total number of artifact uploads.",
		}, []string{"format", "result"}),

		BlobBytesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openartifact_blob_bytes_total",
			Help: "Total bytes transferred to/from the blob store.",
		}, []string{"operation"}),

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
	}

	prometheus.MustRegister(
		r.HTTPRequestsTotal,
		r.HTTPRequestDurationSeconds,
		r.UploadsTotal,
		r.BlobBytesTotal,
		r.BlobDownloadsTotal,
		r.MetadataRegenerationsTotal,
		r.MetadataRegenerationDurationSeconds,
		r.AuditDroppedTotal,
	)

	return r
}
