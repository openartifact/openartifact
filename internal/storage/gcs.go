// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"errors"

	"github.com/aws/aws-sdk-go-v2/credentials"
)

// Sentinel errors for GCS Blob Store.
var (
	ErrGCSBucketRequired     = errors.New("gcs: GCSBucket is required")
	ErrGCSHMACKeyRequired    = errors.New("gcs: GCSHMACKey is required")
	ErrGCSHMACSecretRequired = errors.New("gcs: GCSHMACSecret is required")
)

// NewGCSBlobStore creates a new GCS BlobStore using the S3-compatible XML API.
// GCS has an S3-compatible XML API that can be accessed via the standard S3 client.
func NewGCSBlobStore(cfg StorageConfig) (*S3BlobStore, error) {
	if cfg.GCSBucket == "" {
		return nil, ErrGCSBucketRequired
	}

	if cfg.GCSHMACKey == "" {
		return nil, ErrGCSHMACKeyRequired
	}

	if cfg.GCSHMACSecret == "" {
		return nil, ErrGCSHMACSecretRequired
	}

	// Create a modified config for S3 client to use GCS XML API
	gcsCfg := cfg
	gcsCfg.Endpoint = "https://storage.googleapis.com"
	gcsCfg.Bucket = cfg.GCSBucket
	gcsCfg.AccessKey = cfg.GCSHMACKey
	gcsCfg.SecretKey = cfg.GCSHMACSecret
	gcsCfg.Region = "auto"
	gcsCfg.ForcePathStyle = true

	store, err := NewS3BlobStore(gcsCfg)
	if err != nil {
		return nil, err
	}

	store.gcs = true
	store.gcsCompose = newGCSComposer(cfg.GCSBucket, credentials.NewStaticCredentialsProvider(cfg.GCSHMACKey, cfg.GCSHMACSecret, ""))

	return store, nil
}
