// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"
)

// filesystemUploadsDir is the directory of resumable uploads under the blob
// root. Blob listing and the alias scan skip it, like the staging directory.
const filesystemUploadsDir = "uploads"

// isReservedRootDir reports whether rel, a directory under the blob root, holds
// no blobs (staging area, uploads, upload spool).
func isReservedRootDir(rel string) bool {
	return rel == filesystemStagingDir || rel == filesystemUploadsDir || rel == FilesystemSpoolDir
}

var _ UploadStore = (*FilesystemStore)(nil)

// uploads is the upload store of this blob root: <basePath>/uploads/<id>.
func (fs *FilesystemStore) uploads() dirUploadStore {
	return dirUploadStore{dir: filepath.Join(fs.basePath, filesystemUploadsDir)}
}

// UploadsSupported implements UploadStore. The uploads live on the blob
// filesystem, which every replica sharing the blob root sees.
func (fs *FilesystemStore) UploadsSupported() bool { return true }

// AppendUpload implements UploadStore.
func (fs *FilesystemStore) AppendUpload(
	ctx context.Context,
	id string,
	at UploadPosition,
	r io.Reader,
	commit func(ctx context.Context, state string, n int64) error,
) error {
	return fs.uploads().AppendUpload(ctx, id, at, r, commit)
}

// CompleteUpload implements UploadStore.
func (fs *FilesystemStore) CompleteUpload(ctx context.Context, id string, at UploadPosition) (StagedObject, error) {
	return fs.uploads().CompleteUpload(ctx, id, at)
}

// PromoteUpload implements UploadStore: the upload file is on the blob
// filesystem, so the verified object is hard-linked into place (no copy) with
// the ".meta" sidecar Put writes.
func (fs *FilesystemStore) PromoteUpload(_ context.Context, id, sha256hex, version string, metadata map[string]string) error {
	if err := ValidateBlobID(sha256hex); err != nil {
		return err
	}

	blobPath, err := fs.getBlobPath(sha256hex)
	if err != nil {
		return err
	}

	if _, statErr := os.Stat(blobPath); statErr == nil {
		return nil
	}

	path, err := fs.uploads().path(id)
	if err != nil {
		return err
	}

	src, err := openVerifiedFile(path, version)
	if err != nil {
		return err
	}

	defer func() { _ = src.Close() }()

	return linkIntoBlob(src, path, blobPath, sha256hex, metadata)
}

// OpenUpload implements UploadStore.
func (fs *FilesystemStore) OpenUpload(ctx context.Context, id, version string) (io.ReadCloser, error) {
	return fs.uploads().OpenUpload(ctx, id, version)
}

// DeleteUpload implements UploadStore.
func (fs *FilesystemStore) DeleteUpload(ctx context.Context, id string) error {
	return fs.uploads().DeleteUpload(ctx, id)
}

// WalkUploads implements UploadStore.
func (fs *FilesystemStore) WalkUploads(ctx context.Context, olderThan time.Time, fn func(ids []string) error) error {
	return fs.uploads().WalkUploads(ctx, olderThan, fn)
}
