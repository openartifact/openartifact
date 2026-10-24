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
	// Type selects the storage backend: filesystem, s3, gcs, azure, garage,
	// seaweedfs or oci. garage and seaweedfs reuse the S3 client with
	// path-style addressing forced on.
	Type string `yaml:"type"`
	// Endpoint is the endpoint URL of an S3-compatible service (s3, garage,
	// seaweedfs) or the URL of the OCI registry.
	Endpoint string `yaml:"endpoint"`
	// Bucket is the bucket name (s3, garage, seaweedfs) or the OCI repository
	// prefix.
	Bucket string `yaml:"bucket"`
	// Region is the AWS region or equivalent.
	Region string `yaml:"region"`
	// AccessKey is the S3 access key. It is used only when both keys are set,
	// otherwise the AWS credential chain applies.
	AccessKey string `yaml:"accessKey"`
	// SecretKey is the S3 secret key.
	SecretKey string `yaml:"secretKey"`
	// Path is the directory of the filesystem backend.
	Path string `yaml:"path"`
	// Options are free-form backend options. The environment form is
	// comma-separated key=value pairs, merged over the YAML keys. None of the
	// built-in backends reads any key yet.
	Options map[string]string `yaml:"options"`
	// ForcePathStyle uses path-style S3 addressing (required for MinIO, Garage
	// and SeaweedFS).
	ForcePathStyle bool `yaml:"forcePathStyle"`
	// PresignTTL is the lifetime of presigned download URLs (s3, gcs), clamped
	// to 1m-1h. It only applies while presignDownloads is on.
	PresignTTL time.Duration `yaml:"presignTtl"`
	// PresignUploadTTL is the lifetime of presigned PUT upload URLs, clamped to
	// 1m-12h.
	PresignUploadTTL time.Duration `yaml:"presignUploadTtl"`
	// PresignMaxPendingUploads is how many outstanding (not completed, not
	// expired) presigned uploads one user may hold; 0 means unlimited. Beyond
	// it, creating an upload answers 429.
	PresignMaxPendingUploads int `yaml:"presignMaxPendingUploads"`
	// PresignDownloads answers blob downloads with a 307 redirect to a
	// presigned URL (s3 and gcs only) instead of streaming them. A repository
	// can override it with presign_downloads.
	PresignDownloads bool `yaml:"presignDownloads"`
	// PresignMinSize is the size in bytes below which blobs are always
	// streamed; 0 redirects everything and a negative value is rejected at
	// startup.
	PresignMinSize int64 `yaml:"presignMinSize"`
	// PresignEndpoint is the public base URL (absolute http(s), no query or
	// fragment) that presigned URLs are signed against (S3 family); empty uses
	// the endpoint. Set it when the endpoint is not reachable by clients.
	PresignEndpoint string `yaml:"presignEndpoint"`
	// GCSBucket is the GCS bucket (reached through the S3-compatible XML API).
	GCSBucket string `yaml:"gcsBucket"`
	// GCSHMACKey is the GCS HMAC access key (required for gcs).
	GCSHMACKey string `yaml:"gcsHmacKey"`
	// GCSHMACSecret is the GCS HMAC secret.
	GCSHMACSecret string `yaml:"gcsHmacSecret"`
	// AzureAccountName is the Azure storage account name.
	AzureAccountName string `yaml:"azureAccountName"`
	// AzureAccountKey is the Azure storage account key.
	AzureAccountKey string `yaml:"azureAccountKey"`
	// AzureContainerName is the Azure container name.
	AzureContainerName string `yaml:"azureContainerName"`
	// AzureEndpoint is the blob service URL, for Azurite or a sovereign cloud;
	// empty uses https://<account>.blob.core.windows.net.
	AzureEndpoint string `yaml:"azureEndpoint"`
	// StagingPath is the spool directory for server-relayed presigned uploads
	// on oci storage (filesystem uses <path>/staging); empty means
	// <tmp>/openartifact-staging.
	StagingPath string `yaml:"stagingPath"`
	// PublicURL is the public base URL of this server (BASE_URL), set by the
	// config loader; server-relayed upload URLs point at it.
	PublicURL string `yaml:"-"`
	// OCIInsecure allows plain HTTP to the OCI registry (dev and testing only).
	OCIInsecure bool `yaml:"ociInsecure"`
	// OCIUsername is the OCI registry user.
	OCIUsername string `yaml:"ociUsername"`
	// OCIPassword is the OCI registry password.
	OCIPassword string `yaml:"ociPassword"`
}
