// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"net/url"
	"strings"
	"time"
)

// Read fallback modes of a replica (ReplicaConfig.ReadFallback).
const (
	// ReadFallbackNotFound serves a read from the replica only when the primary
	// answers that the blob does not exist (the default).
	ReadFallbackNotFound = "notfound"
	// ReadFallbackError also falls back on any other primary error (outage).
	ReadFallbackError = "error"
	// ReadFallbackOff never reads from the replica: it only protects against
	// loss, serving from it needs a manual cutover.
	ReadFallbackOff = "off"
)

// ReplicaConfig configures asynchronous replication of every blob to a
// secondary backend (docs/operations/replication.md). The backend itself is
// described by the same keys as the primary one (storage.type, storage.path,
// storage.bucket, ...); the presign and upload-relay keys are ignored: a
// replica is never presigned or uploaded to by clients. Replication is active
// when Type is not empty.
type ReplicaConfig struct {
	StorageConfig `yaml:",inline"`

	// ReadFallback is what a read does when the primary cannot answer:
	// ReadFallbackNotFound (default), ReadFallbackError or ReadFallbackOff.
	ReadFallback string `yaml:"readFallback"`
	// Interval is the pause between replication passes (queued blobs are also
	// picked up as soon as they are enqueued).
	Interval time.Duration `yaml:"interval"`
	// Batch is how many queued operations one claim takes.
	Batch int `yaml:"batch"`
	// Concurrency is how many blobs are copied at once.
	Concurrency int `yaml:"concurrency"`
	// VerifyInterval is the pause between verify passes; 0 = only on demand
	// (POST /api/v1/admin/storage/replication/verify).
	VerifyInterval time.Duration `yaml:"verifyInterval"`
	// VerifyDeep also hashes the bytes of both copies during a verify pass,
	// instead of comparing existence and size only.
	VerifyDeep bool `yaml:"verifyDeep"`
	// MaxBackoff caps the retry delay of a failing operation.
	MaxBackoff time.Duration `yaml:"maxBackoff"`
}

// DefaultReplicaConfig returns the replica settings used when none are given;
// the backend part is empty, i.e. replication is off.
func DefaultReplicaConfig() ReplicaConfig {
	return ReplicaConfig{
		ReadFallback:   ReadFallbackNotFound,
		Interval:       5 * time.Second,
		Batch:          100,
		Concurrency:    4,
		VerifyInterval: 24 * time.Hour,
		MaxBackoff:     time.Hour,
	}
}

// Enabled reports whether a replica is configured.
func (c ReplicaConfig) Enabled() bool { return c.Type != "" }

// Summary describes where the replica keeps its blobs without any credential:
// the backend kind and its directory, bucket, container or endpoint host.
func (c ReplicaConfig) Summary() string {
	switch c.Type {
	case "filesystem":
		return c.Path
	case "s3", "garage", "seaweedfs":
		return joinLocation(hostOf(c.Endpoint), c.Bucket)
	case "gcs":
		bucket := c.GCSBucket
		if bucket == "" {
			bucket = c.Bucket
		}

		return bucket
	case "azure":
		return joinLocation(c.AzureAccountName, c.AzureContainerName)
	case "oci":
		return joinLocation(hostOf(c.Endpoint), c.Bucket)
	default:
		return ""
	}
}

func joinLocation(parts ...string) string {
	var out []string

	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}

	return strings.Join(out, "/")
}

// hostOf returns the host[:port] of an endpoint URL (no scheme, userinfo,
// path or query), or the input when it is not a URL.
func hostOf(endpoint string) string {
	if endpoint == "" {
		return ""
	}

	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		if _, rest, ok := strings.Cut(endpoint, "@"); ok {
			return rest
		}

		return endpoint
	}

	return u.Host
}
