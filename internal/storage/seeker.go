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

var errBadSeek = errors.New("invalid seek")

// BlobSeeker is an io.ReadSeekCloser over a stored blob of a known size. A Seek
// costs nothing; the next Read opens the blob at the new position through
// OpenAt, so a filesystem store seeks and an object store issues a ranged GET
// instead of reading the bytes before the position. It is what lets
// http.ServeContent serve Range requests from any BlobStore.
type BlobSeeker struct {
	ctx   context.Context //nolint:containedctx // bound to the request that serves the blob
	store BlobStore
	id    string
	size  int64

	pos    int64         // where the next Read starts
	cur    io.ReadCloser // open reader, or nil
	curPos int64         // where cur is positioned
}

// NewBlobSeeker returns a seeker over blob id of the given size. It opens
// nothing until the first Read.
func NewBlobSeeker(ctx context.Context, store BlobStore, id string, size int64) *BlobSeeker {
	return &BlobSeeker{ctx: ctx, store: store, id: id, size: size}
}

// Open opens the blob at the current position now, so a missing blob is
// reported before any response header is written. Reading from that position
// afterwards reuses the reader.
func (s *BlobSeeker) Open() error {
	if s.cur != nil || s.pos >= s.size {
		return nil
	}

	rc, err := OpenAt(s.ctx, s.store, s.id, s.pos)
	if err != nil {
		return err
	}

	s.cur, s.curPos = rc, s.pos

	return nil
}

// Read implements io.Reader.
func (s *BlobSeeker) Read(p []byte) (int, error) {
	if s.pos >= s.size {
		return 0, io.EOF
	}

	if s.cur != nil && s.curPos != s.pos {
		_ = s.cur.Close()
		s.cur = nil
	}

	if s.cur == nil {
		if err := s.Open(); err != nil {
			return 0, err
		}
	}

	n, err := s.cur.Read(p)
	s.pos += int64(n)
	s.curPos += int64(n)

	return n, err
}

// Seek implements io.Seeker.
func (s *BlobSeeker) Seek(offset int64, whence int) (int64, error) {
	var abs int64

	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = s.pos + offset
	case io.SeekEnd:
		abs = s.size + offset
	default:
		return 0, fmt.Errorf("%w: whence %d", errBadSeek, whence)
	}

	if abs < 0 {
		return 0, fmt.Errorf("%w: negative position", errBadSeek)
	}

	s.pos = abs

	return abs, nil
}

// Close releases the open reader, if any.
func (s *BlobSeeker) Close() error {
	if s.cur == nil {
		return nil
	}

	err := s.cur.Close()
	s.cur = nil

	return err
}
