// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"io"
	"time"
)

var _ ChunkStore = (*OCIBlobStore)(nil)

// The OCI backend is another registry: it has no scratch namespace to hold
// upload chunks, so ChunksFor falls back to a directory on this host.

// ChunksSupported implements ChunkStore.
func (*OCIBlobStore) ChunksSupported() bool { return false }

// PutChunk implements ChunkStore.
func (*OCIBlobStore) PutChunk(context.Context, string, io.Reader) (int64, error) {
	return 0, ErrChunksNotSupported
}

// OpenChunk implements ChunkStore.
func (*OCIBlobStore) OpenChunk(context.Context, string) (io.ReadCloser, error) {
	return nil, ErrChunksNotSupported
}

// DeleteChunk implements ChunkStore.
func (*OCIBlobStore) DeleteChunk(context.Context, string) error { return ErrChunksNotSupported }

// WalkChunks implements ChunkStore.
func (*OCIBlobStore) WalkChunks(context.Context, time.Time, func([]string) error) error {
	return ErrChunksNotSupported
}
