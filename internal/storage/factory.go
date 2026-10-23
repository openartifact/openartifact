// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"fmt"
	"path/filepath"
)

// NewBlobStore creates a BlobStore from the given config, selecting the backend by Type.
func NewBlobStore(cfg StorageConfig) (BlobStore, error) {
	switch cfg.Type {
	case "s3":
		return NewS3BlobStore(cfg)
	case "garage":
		// Garage is an S3-compatible distributed object store.
		// It reuses the S3 backend; ForcePathStyle is required by Garage.
		cfg.ForcePathStyle = true
		return NewS3BlobStore(cfg)
	case "seaweedfs":
		// SeaweedFS exposes an S3-compatible API.
		// It reuses the S3 backend; ForcePathStyle is required by SeaweedFS.
		cfg.ForcePathStyle = true
		return NewS3BlobStore(cfg)
	case "gcs":
		return NewGCSBlobStore(cfg)
	case "azure":
		return NewAzureBlobStore(cfg)
	case "oci":
		return NewOCIBlobStore(cfg)
	case "filesystem", "":
		return NewFilesystemBlobStore(cfg)
	default:
		return nil, fmt.Errorf("unknown storage type: %q", cfg.Type) //nolint:err113 // dynamic error with runtime context
	}
}

// NewFilesystemBlobStore creates a FilesystemStore from the given config.
func NewFilesystemBlobStore(cfg StorageConfig) (*FilesystemStore, error) {
	store, err := NewFilesystemStore(cfg.Path)
	if err != nil {
		return nil, err
	}

	store.relay = newRelay(filepath.Join(cfg.Path, filesystemStagingDir), cfg.PublicURL)

	return store, nil
}
