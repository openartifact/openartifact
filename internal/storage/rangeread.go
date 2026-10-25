// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// ErrOffsetOutOfRange is returned by a ranged read whose offset is negative or
// lies beyond the end of the blob.
var ErrOffsetOutOfRange = errors.New("blob offset out of range")

// RangeReader is implemented by stores that can open a blob at a byte offset
// without reading the bytes before it (filesystem seek, S3/GCS/Azure/OCI range
// GET, and the wrappers around them). Use OpenAt rather than asserting it.
type RangeReader interface {
	// GetFrom returns a reader positioned at offset and running to the end of
	// the blob. An offset of 0 is a plain Get. Wraps ErrBlobNotFound for a
	// missing blob and ErrOffsetOutOfRange when offset is past the end (an
	// offset equal to the size may yield an empty reader or that error).
	GetFrom(ctx context.Context, id string, offset int64) (io.ReadCloser, error)
}

// OpenAt opens a blob at a byte offset: through RangeReader when the store has
// it, otherwise by reading and discarding the first offset bytes.
func OpenAt(ctx context.Context, s BlobStore, id string, offset int64) (io.ReadCloser, error) {
	if offset < 0 {
		return nil, ErrOffsetOutOfRange
	}

	if rr, ok := s.(RangeReader); ok {
		return rr.GetFrom(ctx, id, offset)
	}

	rc, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	return skipTo(rc, offset)
}

// skipTo discards the first offset bytes of rc and returns rc, or closes it
// and fails when the blob is shorter.
func skipTo(rc io.ReadCloser, offset int64) (io.ReadCloser, error) {
	if offset == 0 {
		return rc, nil
	}

	n, err := io.CopyN(io.Discard, rc, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		_ = rc.Close()

		return nil, fmt.Errorf("skip to offset %d: %w", offset, err)
	}

	if n < offset {
		_ = rc.Close()

		return nil, fmt.Errorf("%w: %d", ErrOffsetOutOfRange, offset)
	}

	return rc, nil
}

// rangeHeader is the Range header value that reads from offset to the end.
func rangeHeader(offset int64) string {
	return fmt.Sprintf("bytes=%d-", offset)
}
