// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/tiering"
	"go.yaml.in/yaml/v3"
)

var (
	errReplicaType      = errors.New("storage.replica.type must be one of filesystem, s3, garage, seaweedfs, gcs, azure, oci")
	errReplicaFallback  = errors.New("storage.replica.readFallback must be notfound, error or off")
	errReplicaBatch     = errors.New("storage.replica.batch and storage.replica.concurrency must be at least 1")
	errReplicaIntervals = errors.New("storage.replica.interval and storage.replica.maxBackoff must be positive and storage.replica.verifyInterval must not be negative")
	errReplicaPool      = errors.New("database.maxOpenConns is too small for storage.replica: each blob being copied or verified pins a connection for its blob lock and needs another to run its queries, plus one for the verify lock; use 0 (driver default) or at least 2 * storage.replica.concurrency + 2")
	errReplicaLocation  = errors.New("storage.replica needs the location of the backend: path (filesystem), bucket (s3, garage, seaweedfs), gcsBucket or bucket (gcs), azureAccountName and azureContainerName (azure), endpoint (oci)")
)

// replicaStorageKinds are the backends a replica can use (the primary's list).
//
//nolint:gochecknoglobals // immutable lookup table
var replicaStorageKinds = []string{"filesystem", "s3", "garage", "seaweedfs", "gcs", "azure", "oci"}

// loadReplicaFromYAML reads the storage.replica block, which nests under
// storage: next to the primary's keys that storage.StorageConfig decodes, so it
// is decoded separately into cfg.StorageReplica (starting from its defaults).
func loadReplicaFromYAML(data []byte, cfg *Config) error {
	doc := struct {
		Storage struct {
			Replica *storage.ReplicaConfig `yaml:"replica"`
		} `yaml:"storage"`
	}{}
	doc.Storage.Replica = &cfg.StorageReplica

	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse storage.replica: %w", err)
	}

	return nil
}

// applyReplicaEnvOverrides applies the OPENARTIFACT_STORAGE_REPLICA_* variables.
// Every storage.replica key has one; the backend keys mirror the primary's.
func applyReplicaEnvOverrides(cfg *Config) {
	r := &cfg.StorageReplica
	const p = "OPENARTIFACT_STORAGE_REPLICA_"

	overrideString(&r.Type, p+"TYPE")
	overrideString(&r.Endpoint, p+"ENDPOINT")
	overrideString(&r.Bucket, p+"BUCKET")
	overrideString(&r.Region, p+"REGION")
	overrideString(&r.AccessKey, p+"ACCESS_KEY")
	overrideString(&r.SecretKey, p+"SECRET_KEY")
	overrideString(&r.Path, p+"PATH")
	overrideKeyValues(&r.Options, p+"OPTIONS")
	overrideBool(&r.ForcePathStyle, p+"FORCE_PATH_STYLE")
	overrideString(&r.GCSBucket, p+"GCS_BUCKET")
	overrideString(&r.GCSHMACKey, p+"GCS_HMAC_KEY")
	overrideString(&r.GCSHMACSecret, p+"GCS_HMAC_SECRET")
	overrideString(&r.AzureAccountName, p+"AZURE_ACCOUNT_NAME")
	overrideString(&r.AzureAccountKey, p+"AZURE_ACCOUNT_KEY")
	overrideString(&r.AzureContainerName, p+"AZURE_CONTAINER_NAME")
	overrideString(&r.AzureEndpoint, p+"AZURE_ENDPOINT")
	overrideString(&r.OCIUsername, p+"OCI_USERNAME")
	overrideString(&r.OCIPassword, p+"OCI_PASSWORD")
	overrideBool(&r.OCIInsecure, p+"OCI_INSECURE")

	overrideString(&r.ReadFallback, p+"READ_FALLBACK")
	overrideDuration(&r.Interval, p+"INTERVAL")
	overrideInt(&r.Batch, p+"BATCH")
	overrideInt(&r.Concurrency, p+"CONCURRENCY")
	overrideDuration(&r.VerifyInterval, p+"VERIFY_INTERVAL")
	overrideBool(&r.VerifyDeep, p+"VERIFY_DEEP")
	overrideDuration(&r.MaxBackoff, p+"MAX_BACKOFF")
}

// validateReplica checks storage.replica. Nothing is checked while no replica
// is configured, so a stray tuning key cannot break a server that does not use
// replication.
func (c *Config) validateReplica() error {
	r := c.StorageReplica
	if !r.Enabled() {
		return nil
	}

	if !slices.Contains(replicaStorageKinds, r.Type) {
		return fmt.Errorf("%w (got %q)", errReplicaType, r.Type)
	}

	if !slices.Contains([]string{storage.ReadFallbackNotFound, storage.ReadFallbackError, storage.ReadFallbackOff}, r.ReadFallback) {
		return fmt.Errorf("%w (got %q)", errReplicaFallback, r.ReadFallback)
	}

	if r.Batch < 1 || r.Concurrency < 1 {
		return errReplicaBatch
	}

	if r.Interval <= 0 || r.MaxBackoff <= 0 || r.VerifyInterval < 0 {
		return errReplicaIntervals
	}

	if r.Summary() == "" || (r.Type == "azure" && (r.AzureAccountName == "" || r.AzureContainerName == "")) ||
		(r.Type == "oci" && strings.TrimSpace(r.Endpoint) == "") {
		return errReplicaLocation
	}

	if c.Database.MaxOpenConns > 0 && c.Database.MaxOpenConns < 2*r.Concurrency+2 {
		return errReplicaPool
	}

	// The replica must hold a second copy: a backend at the same place as the
	// primary store would "replicate" a blob onto itself and then delete the
	// only copy when the deletion is replicated.
	if err := tiering.CheckDistinct(c.Storage, r.StorageConfig); err != nil {
		return fmt.Errorf("storage.replica: %w", err)
	}

	return nil
}

// defaultReplicaConfig is the replica section of defaultConfig: no backend
// (replication off), the primary's default region, and the tuning defaults.
func defaultReplicaConfig() storage.ReplicaConfig {
	r := storage.DefaultReplicaConfig()
	r.Region = "us-east-1"
	r.Options = make(map[string]string)

	return r
}
