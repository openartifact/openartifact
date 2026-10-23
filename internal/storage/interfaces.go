// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrPresignNotSupported is returned by storage backends that do not support presigned URLs.
var ErrPresignNotSupported = errors.New("presign URL not supported by this storage backend")

// ErrBlobNotFound is wrapped by every backend when a blob does not exist.
var ErrBlobNotFound = errors.New("blob not found")

// ErrListNotSupported is returned by List on backends that cannot enumerate
// their blobs (OCI registries expose no flat blob listing).
var ErrListNotSupported = errors.New("blob listing is not supported by this storage backend")

// BlobStore defines the interface for blob storage operations.
type BlobStore interface {
	// Put stores a blob and returns its identifier
	Put(ctx context.Context, data io.Reader, metadata map[string]string) (string, error)

	// Get retrieves a blob by its identifier
	Get(ctx context.Context, id string) (io.ReadCloser, error)

	// Delete removes a blob by its identifier
	Delete(ctx context.Context, id string) error

	// Exists checks if a blob exists
	Exists(ctx context.Context, id string) (bool, error)

	// GetMetadata retrieves metadata for a blob
	GetMetadata(ctx context.Context, id string) (map[string]string, error)

	// List returns up to limit blob identifiers (sha256 hex, ascending) that
	// follow the opaque continuation token. An empty token starts at the
	// beginning; the returned next token is empty once the listing is
	// exhausted. Cost per call is independent of how many blobs precede the
	// token. Backends that cannot enumerate return ErrListNotSupported.
	List(ctx context.Context, token string, limit int) (ids []string, next string, err error)

	// GetSize returns the size of a blob in bytes
	GetSize(ctx context.Context, id string) (int64, error)

	// ExistsBySHA256 checks if a blob with the given SHA-256 hash exists.
	// Returns the blobID and true if found, or ("", false, nil) if not found.
	ExistsBySHA256(ctx context.Context, sha256 string) (blobID string, exists bool, err error)

	// PresignURL returns a presigned URL for direct blob download.
	// Returns ErrPresignNotSupported if the backend does not support presigned URLs.
	PresignURL(ctx context.Context, sha256 string, ttl time.Duration) (string, error)
}

// StorageConfig contains configuration for blob storage.
type StorageConfig struct {
	// Type selects the storage backend.
	// Supported values: "filesystem", "s3", "gcs", "azure", "garage", "seaweedfs", "oci".
	Type                     string            `yaml:"type"`                     // "s3", "filesystem", "gcs", "azure", "garage", "seaweedfs", "oci"
	Endpoint                 string            `yaml:"endpoint"`                 // For S3-compatible services (s3, garage, seaweedfs) or OCI registry URL
	Bucket                   string            `yaml:"bucket"`                   // Bucket/container name (s3, garage, seaweedfs) or OCI repository prefix
	Region                   string            `yaml:"region"`                   // AWS region or equivalent
	AccessKey                string            `yaml:"accessKey"`                // Access credentials
	SecretKey                string            `yaml:"secretKey"`                // Secret credentials
	Path                     string            `yaml:"path"`                     // Local filesystem path
	Options                  map[string]string `yaml:"options"`                  // Additional options
	ForcePathStyle           bool              `yaml:"forcePathStyle"`           // Use path-style S3 addressing (required for MinIO, Garage, SeaweedFS)
	PresignTTL               time.Duration     `yaml:"presignTtl"`               // Lifetime of presigned download URLs (clamped to 1m-1h)
	PresignUploadTTL         time.Duration     `yaml:"presignUploadTtl"`         // Lifetime of presigned upload URLs (clamped to 1m-12h)
	PresignMaxPendingUploads int               `yaml:"presignMaxPendingUploads"` // Outstanding presigned uploads allowed per user; 0 = unlimited
	PresignDownloads         bool              `yaml:"presignDownloads"`         // Redirect blob downloads (307) to presigned URLs
	PresignMinSize           int64             `yaml:"presignMinSize"`           // Blobs smaller than this many bytes are always streamed
	PresignEndpoint          string            `yaml:"presignEndpoint"`          // Public base URL presigned URLs are signed against (S3 family); empty = Endpoint
	GCSBucket                string            `yaml:"gcsBucket"`                // GCS bucket name
	GCSHMACKey               string            `yaml:"gcsHmacKey"`               // GCS HMAC access key
	GCSHMACSecret            string            `yaml:"gcsHmacSecret"`            // GCS HMAC secret key
	AzureAccountName         string            `yaml:"azureAccountName"`         // Azure storage account name
	AzureAccountKey          string            `yaml:"azureAccountKey"`          // Azure storage account key
	AzureContainerName       string            `yaml:"azureContainerName"`       // Azure container name
	AzureEndpoint            string            `yaml:"azureEndpoint"`            // Azure blob service URL (Azurite or a sovereign cloud); empty = https://<account>.blob.core.windows.net
	StagingPath              string            `yaml:"stagingPath"`              // Spool directory for server-relayed presigned uploads on oci storage (filesystem uses <path>/staging); empty = <tmp>/openartifact-staging
	PublicURL                string            `yaml:"-"`                        // Public base URL of this server (BASE_URL), set by the config loader; server-relayed upload URLs point at it
	// OCI-specific fields
	OCIInsecure bool   `yaml:"ociInsecure"` // Allow plain HTTP for OCI registry (dev/testing only)
	OCIUsername string `yaml:"ociUsername"` // OCI registry username
	OCIPassword string `yaml:"ociPassword"` // OCI registry password
}
