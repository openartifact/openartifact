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

// staleUploadTempAge is how old a blob-upload-*.tmp file must be before the
// startup sweep removes it: anything younger may belong to a live Put.
const staleUploadTempAge = time.Hour

// sweepStaleUploadTemps removes temp files a crashed Put left in basePath.
// Best effort: List skips them, so nothing else would ever reclaim them.
func sweepStaleUploadTemps(basePath string, minAge time.Duration) {
	matches, err := filepath.Glob(filepath.Join(basePath, "blob-upload-*.tmp"))
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-minAge)

	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.Mode().IsRegular() && info.ModTime().Before(cutoff) {
			_ = os.Remove(m)
		}
	}
}

// NewFilesystemStore creates a new filesystem blob store.
func NewFilesystemStore(basePath string) (*FilesystemStore, error) {
	// Create base path if it doesn't exist
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base path: %w", err)
	}

	sweepStaleUploadTemps(basePath, staleUploadTempAge)

	return &FilesystemStore{
		basePath: basePath,
	}, nil
}

// Put stores a blob and returns its identifier.
// The blob ID is the computed SHA-256 of the content; if metadata contains a
// "sha256" key it must equal it, else ErrDigestMismatch is returned and nothing is stored.
// An already-present blob is left untouched.
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

	// The fsync is deferred until the blob is known to be new: a duplicate
	// Put (the common case for shared base layers and re-uploads) otherwise
	// pays the dominant cost of the whole call for bytes it then discards.
	computedSHA256 := hex.EncodeToString(hasher.Sum(nil))

	// The blob is content-addressed: a caller-supplied digest must be the
	// digest of the bytes just written, otherwise nothing becomes visible.
	id := computedSHA256

	if sha, ok := metadata["sha256"]; ok && sha != "" && sha != computedSHA256 {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)

		return "", fmt.Errorf("%w: claimed %s, computed %s", ErrDigestMismatch, sha, computedSHA256)
	}

	blobPath, err := fs.getBlobPath(id)
	if err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)

		return "", err
	}

	dir := filepath.Dir(blobPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to create blob directory: %w", err)
	}

	if metadata != nil {
		metadata["_size"] = strconv.FormatInt(written, 10)
		metadata["_sha256"] = computedSHA256
	}

	// Verified content already present stays untouched: never replace an
	// object other repositories resolve through the same address.
	if _, statErr := os.Stat(blobPath); statErr == nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)

		return id, nil
	}

	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)

		return "", fmt.Errorf("failed to sync blob file: %w", err)
	}

	_ = tmpFile.Close()

	if err := os.Rename(tmpPath, blobPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to move blob to final path: %w", err)
	}

	// The rename is only durable once the directory entry is flushed; without
	// this a crash can lose a blob the database already references.
	if err := syncDir(dir); err != nil {
		return "", fmt.Errorf("failed to sync blob directory: %w", err)
	}

	if metadata != nil {
		metaData, err := json.Marshal(metadata)
		if err != nil {
			return "", fmt.Errorf("failed to encode blob metadata: %w", err)
		}

		if err := writeFileAtomic(dir, blobPath+".meta", metaData); err != nil {
			return "", fmt.Errorf("failed to write blob metadata: %w", err)
		}
	}

	return id, nil
}

// syncDir flushes a directory's entries to disk.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open directory: %w", err)
	}

	if err := d.Sync(); err != nil {
		_ = d.Close()

		return fmt.Errorf("sync directory: %w", err)
	}

	return d.Close()
}

// writeFileAtomic writes data to a temp file in dir and renames it over path,
// so a reader never sees a partial file. The temp name matches the pattern
// listings skip.
func writeFileAtomic(dir, path string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "blob-upload-meta-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)

		return fmt.Errorf("write: %w", err)
	}

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)

		return fmt.Errorf("chmod: %w", err)
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)

		return fmt.Errorf("close: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)

		return fmt.Errorf("rename: %w", err)
	}

	return nil
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
			return nil, fmt.Errorf("%w: %s", ErrBlobNotFound, id)
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

// List returns up to limit blob ids in ascending order that sort after token
// (the last id of the previous page). Directories that sort entirely before
// the token are skipped, so the cost of a page does not grow with the number
// of blobs already listed.
func (fs *FilesystemStore) List(ctx context.Context, token string, limit int) ([]string, string, error) {
	if limit <= 0 {
		limit = 1000
	}

	var ids []string

	more := false

	err := filepath.WalkDir(fs.basePath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			//nolint:nilerr // intentionally skip walk errors
			return nil
		}

		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}

		rel, rerr := filepath.Rel(fs.basePath, path)
		if rerr != nil {
			//nolint:nilerr // intentionally skip walk errors
			return nil
		}

		if d.IsDir() {
			// Two-character shard dirs sorting before the token's shard hold no id after it.
			if token != "" && rel != "." && !strings.Contains(rel, string(os.PathSeparator)) && rel < token[:min(len(rel), len(token))] {
				return filepath.SkipDir
			}

			return nil
		}

		name := d.Name()
		if strings.HasSuffix(name, ".meta") || (strings.HasPrefix(name, "blob-upload-") && strings.HasSuffix(name, ".tmp")) {
			return nil
		}

		// The ID is the directory prefix + filename: "ab" + "cdef123..." = "abcdef123..."
		id := strings.ReplaceAll(rel, string(os.PathSeparator), "")
		if id <= token || !isBlobID(id) {
			return nil
		}

		if len(ids) >= limit {
			more = true
			return filepath.SkipAll
		}

		ids = append(ids, id)

		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("failed to list blobs: %w", err)
	}

	next := ""
	if more && len(ids) > 0 {
		next = ids[len(ids)-1]
	}

	return ids, next, nil
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
			return 0, fmt.Errorf("%w: %s", ErrBlobNotFound, id)
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

// isBlobID reports whether id is a well-formed blob id.
func isBlobID(id string) bool { return ValidateBlobID(id) == nil }
