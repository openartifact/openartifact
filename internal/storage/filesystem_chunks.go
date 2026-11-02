// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"io"
	"path/filepath"
	"time"
)

// filesystemChunksDir is the directory of upload chunks under the blob root.
// Blob listing and the alias scan skip it, like the staging directory.
const filesystemChunksDir = "uploads"

// isReservedRootDir reports whether rel, a directory under the blob root, holds
// no blobs (staging area, upload chunks).
func isReservedRootDir(rel string) bool {
	return rel == filesystemStagingDir || rel == filesystemChunksDir
}

var _ ChunkStore = (*FilesystemStore)(nil)

// chunks is the chunk store of this blob root: <basePath>/uploads/<uuid>.
func (fs *FilesystemStore) chunks() ChunkStore {
	return NewDirChunkStore(filepath.Join(fs.basePath, filesystemChunksDir))
}

// ChunksSupported implements ChunkStore. The chunks live on the blob
// filesystem, which every replica sharing the blob root sees.
func (fs *FilesystemStore) ChunksSupported() bool { return true }

// PutChunk implements ChunkStore.
func (fs *FilesystemStore) PutChunk(ctx context.Context, id string, r io.Reader) (int64, error) {
	return fs.chunks().PutChunk(ctx, id, r)
}

// OpenChunk implements ChunkStore.
func (fs *FilesystemStore) OpenChunk(ctx context.Context, id string) (io.ReadCloser, error) {
	return fs.chunks().OpenChunk(ctx, id)
}

// DeleteChunk implements ChunkStore.
func (fs *FilesystemStore) DeleteChunk(ctx context.Context, id string) error {
	return fs.chunks().DeleteChunk(ctx, id)
}

// WalkChunks implements ChunkStore.
func (fs *FilesystemStore) WalkChunks(ctx context.Context, olderThan time.Time, fn func(ids []string) error) error {
	return fs.chunks().WalkChunks(ctx, olderThan, fn)
}
