// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// FilesystemStore implements the BlobStore interface for filesystem storage.
type FilesystemStore struct {
	basePath string
	aliases  aliasIndex
}

// NewFilesystemStore creates a new filesystem blob store.
func NewFilesystemStore(basePath string) (*FilesystemStore, error) {
	// Create base path if it doesn't exist
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base path: %w", err)
	}

	return &FilesystemStore{
		basePath: basePath,
	}, nil
}

// Put stores a blob and returns its identifier.
// If metadata contains a "sha256" key, that value is used as the blob ID (content-addressed).
// Otherwise the computed SHA-256 of the content is the ID.
func (fs *FilesystemStore) Put(ctx context.Context, data io.Reader, metadata map[string]string) (string, error) {
	var tmpPath string

	if sha, ok := metadata["sha256"]; ok && sha != "" {
		if err := ValidateBlobID(sha); err != nil {
			return "", err
		}
	}

	tmpFile, err := os.CreateTemp(fs.basePath, "blob-upload-*.tmp")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}

	tmpPath = tmpFile.Name()

	hasher := sha256.New()

	written, err := io.Copy(tmpFile, io.TeeReader(data, hasher))
	if err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)

		return "", fmt.Errorf("failed to write blob data: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)

		return "", fmt.Errorf("failed to sync blob file: %w", err)
	}

	_ = tmpFile.Close()

	computedSHA256 := hex.EncodeToString(hasher.Sum(nil))

	var id string
	if sha, ok := metadata["sha256"]; ok && sha != "" {
		id = sha
	} else {
		// No caller-supplied digest: the blob is content-addressed by its computed hash.
		id = computedSHA256
	}

	blobPath, err := fs.getBlobPath(id)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}

	dir := filepath.Dir(blobPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to create blob directory: %w", err)
	}

	if err := os.Rename(tmpPath, blobPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to move blob to final path: %w", err)
	}

	if metadata != nil {
		metadata["_size"] = strconv.FormatInt(written, 10)
		metadata["_sha256"] = computedSHA256
		metaPath := blobPath + ".meta"

		metaData, err := json.Marshal(metadata)
		if err == nil {
			_ = os.WriteFile(metaPath, metaData, 0600)
		}

		if computedSHA256 != id {
			fs.aliases.add(computedSHA256, id)
		}
	}

	return id, nil
}

// Get retrieves a blob by its identifier.
func (fs *FilesystemStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	if err := ValidateBlobID(id); err != nil {
		return nil, err
	}

	blobPath, err := fs.getBlobPath(id)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(blobPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("blob not found: %s", id) //nolint:err113 // dynamic error with runtime context
		}

		return nil, fmt.Errorf("failed to open blob: %w", err)
	}

	return f, nil
}

// Delete removes a blob by its identifier.
func (fs *FilesystemStore) Delete(ctx context.Context, id string) error {
	if err := ValidateBlobID(id); err != nil {
		return err
	}

	blobPath, err := fs.getBlobPath(id)
	if err != nil {
		return err
	}
	metaPath := blobPath + ".meta"

	// Remove metadata file (ignore error if not exists)
	_ = os.Remove(metaPath)

	fs.aliases.remove(id)

	if err := os.Remove(blobPath); err != nil {
		if os.IsNotExist(err) {
			return nil // Already deleted
		}

		return fmt.Errorf("failed to delete blob: %w", err)
	}

	return nil
}

// Exists checks if a blob exists.
func (fs *FilesystemStore) Exists(ctx context.Context, id string) (bool, error) {
	if err := ValidateBlobID(id); err != nil {
		return false, err
	}

	blobPath, err := fs.getBlobPath(id)
	if err != nil {
		return false, err
	}

	_, err = os.Stat(blobPath)
	if err == nil {
		return true, nil
	}

	if os.IsNotExist(err) {
		return false, nil
	}

	return false, fmt.Errorf("failed to check blob existence: %w", err)
}

// GetMetadata retrieves metadata for a blob.
func (fs *FilesystemStore) GetMetadata(ctx context.Context, id string) (map[string]string, error) {
	if err := ValidateBlobID(id); err != nil {
		return nil, err
	}

	blobPath, err := fs.getBlobPath(id)
	if err != nil {
		return nil, err
	}

	metaPath := blobPath + ".meta"

	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("metadata not found for blob: %s", id) //nolint:err113 // dynamic error with runtime context
		}

		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	var metadata map[string]string
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	return metadata, nil
}

// List returns a list of blob identifiers with optional filtering.
func (fs *FilesystemStore) List(ctx context.Context, prefix string, limit int, offset int) ([]string, error) {
	var ids []string

	count := 0
	skipped := 0

	err := filepath.Walk(fs.basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			//nolint:nilerr // intentionally skip walk errors
			return nil
		}

		if info.IsDir() || strings.HasSuffix(path, ".meta") {
			return nil
		}

		if strings.HasPrefix(info.Name(), "blob-upload-") && strings.HasSuffix(info.Name(), ".tmp") {
			return nil
		}

		// Reconstruct the blob ID from path
		rel, err := filepath.Rel(fs.basePath, path)
		if err != nil {
			//nolint:nilerr // intentionally skip walk errors
			return nil
		}
		// The ID is the directory prefix + filename: "ab" + "cdef123..." = "abcdef123..."
		parts := strings.SplitN(rel, string(os.PathSeparator), 2)

		var id string
		if len(parts) == 2 {
			id = parts[0] + parts[1]
		} else {
			id = rel
		}

		if prefix != "" && !strings.HasPrefix(id, prefix) {
			return nil
		}

		if skipped < offset {
			skipped++
			return nil
		}

		if limit > 0 && count >= limit {
			return filepath.SkipAll
		}

		ids = append(ids, id)
		count++

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list blobs: %w", err)
	}

	return ids, nil
}

// GetSize returns the size of a blob in bytes.
func (fs *FilesystemStore) GetSize(ctx context.Context, id string) (int64, error) {
	if err := ValidateBlobID(id); err != nil {
		return 0, err
	}

	blobPath, err := fs.getBlobPath(id)
	if err != nil {
		return 0, err
	}

	info, err := os.Stat(blobPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("blob not found: %s", id) //nolint:err113 // dynamic error with runtime context
		}

		return 0, fmt.Errorf("failed to stat blob: %w", err)
	}

	return info.Size(), nil
}

// ExistsBySHA256 finds a blob by its SHA-256 hash.
// Fast path: content-addressed blobs use SHA-256 as the blob ID (O(1) check).
// Fallback: scans metadata files for blobs stored with a non-SHA256 ID.
// Returns the blobID and true if found, or ("", false, nil) if not found.
func (fs *FilesystemStore) ExistsBySHA256(ctx context.Context, sha256hex string) (string, bool, error) {
	if err := ValidateBlobID(sha256hex); err != nil {
		return "", false, err
	}

	// Fast path: content-addressed storage uses SHA-256 as the blob ID.
	exists, err := fs.Exists(ctx, sha256hex)
	if err != nil {
		return "", false, err
	}

	if exists {
		return sha256hex, true, nil
	}

	// Fallback: blobs stored under a non-SHA256 ID, resolved through an index
	// that is built by one directory walk on first use and kept current by
	// Put and Delete (see aliasIndex).
	return fs.aliases.lookup(fs, sha256hex)
}

// PresignURL returns ErrPresignNotSupported; filesystem storage does not support presigned URLs.
func (fs *FilesystemStore) PresignURL(_ context.Context, _ string, _ time.Duration) (string, error) {
	return "", ErrPresignNotSupported
}

// getBlobPath returns the full path for a blob. The id is validated as 64
// lowercase hex characters and the resulting path is verified to stay inside
// basePath, so an untrusted id can never escape the store root.
func (fs *FilesystemStore) getBlobPath(id string) (string, error) {
	if err := ValidateBlobID(id); err != nil {
		return "", err
	}

	// Two-level layout /base/ab/cdef...
	p := filepath.Join(fs.basePath, id[:2], id[2:])

	rel, err := filepath.Rel(fs.basePath, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrInvalidBlobID
	}

	return p, nil
}
