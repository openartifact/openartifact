// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"os"
	"sync"

	"github.com/openartifact/openartifact/internal/iocopy"
)

var errNegativeOffset = errors.New("negative offset")

// spoolMemoryLimit is how much of a body a Spool keeps in memory; a larger body
// is moved to a temp file so a 500 MB upload never costs 500 MB (plus the
// regrowth garbage of an append-built buffer) of heap.
const spoolMemoryLimit = 1 << 20

//nolint:gochecknoglobals // sync.Pool of read-ahead buffers, same reason
var headPool = sync.Pool{New: func() any {
	b := make([]byte, spoolMemoryLimit+1)
	return &b
}}

// Spool is a request body read once, with its SHA-256 and size known, held in
// memory when small and in an unlinked-on-Close temp file otherwise. Parsers
// and the blob store read it back through Reader/ReaderAt, so handlers that
// need random access (zip, tar) or several passes never hold the whole body in
// memory. Close is mandatory.
type Spool struct {
	mem    []byte
	file   *os.File
	size   int64
	sha256 string
}

// SpoolLimited reads r to its end, keeping at most maxBytes: a longer body fails
// with ErrContentTooLarge (reading no further than maxBytes+1 bytes), a read
// error is returned as is. Each extra hash receives every byte, so callers can
// get MD5/SHA-1 in the same pass. The SHA-256 is available from SHA256Hex.
func SpoolLimited(r io.Reader, maxBytes int64, extra ...hash.Hash) (*Spool, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("%w: %d", ErrNegativeContentLimit, maxBytes)
	}

	limit := maxBytes
	if limit < math.MaxInt64 {
		limit++
	}

	sum := sha256.New()
	writers := make([]io.Writer, 0, 1+len(extra))
	writers = append(writers, sum)

	for _, h := range extra {
		writers = append(writers, h)
	}

	tee := io.TeeReader(io.LimitReader(r, limit), io.MultiWriter(writers...))
	s := &Spool{}

	// Fill a pooled buffer first; a body that ends within it never touches the
	// disk and costs one exact-size copy.
	hp, _ := headPool.Get().(*[]byte)
	defer headPool.Put(hp)

	head := (*hp)[:min(spoolMemoryLimit+1, limit)]

	n, eof, err := readFill(tee, head)
	if err != nil {
		return nil, err
	}

	if eof || n <= spoolMemoryLimit {
		s.mem = bytes.Clone(head[:n])
		s.size = int64(n)
	} else if err := s.spill(head[:n], tee); err != nil {
		s.Close()
		return nil, err
	}

	if s.size > maxBytes {
		s.Close()
		return nil, fmt.Errorf("%w: limit is %d bytes", ErrContentTooLarge, maxBytes)
	}

	s.sha256 = hex.EncodeToString(sum.Sum(nil))

	return s, nil
}

// readFill reads until buf is full or r ends; eof reports that r ended. Unlike
// io.ReadFull it does not turn a source's own io.ErrUnexpectedEOF into a short
// read, so a truncated body stays an error.
func readFill(r io.Reader, buf []byte) (int, bool, error) {
	n := 0

	for n < len(buf) {
		m, rerr := r.Read(buf[n:])
		n += m

		if errors.Is(rerr, io.EOF) {
			return n, true, nil
		}

		if rerr != nil {
			return n, false, rerr
		}
	}

	return n, false, nil
}

// spill moves the already-read head and the rest of src into a temp file.
func (s *Spool) spill(head []byte, src io.Reader) error {
	f, err := os.CreateTemp("", "oa-spool-*")
	if err != nil {
		return fmt.Errorf("create spool file: %w", err)
	}

	s.file = f

	if _, err := f.Write(head); err != nil {
		return fmt.Errorf("write spool file: %w", err)
	}

	rest, err := iocopy.Copy(f, src)
	if err != nil {
		return err
	}

	s.size = int64(len(head)) + rest

	return nil
}

// Size is the number of body bytes.
func (s *Spool) Size() int64 { return s.size }

// SHA256Hex is the lower-case hex SHA-256 of the body.
func (s *Spool) SHA256Hex() string { return s.sha256 }

// ReadAt implements io.ReaderAt.
func (s *Spool) ReadAt(p []byte, off int64) (int, error) {
	if s.file != nil {
		return s.file.ReadAt(p, off)
	}

	if off < 0 {
		return 0, errNegativeOffset
	}

	if off >= int64(len(s.mem)) {
		return 0, io.EOF
	}

	n := copy(p, s.mem[off:])
	if n < len(p) {
		return n, io.EOF
	}

	return n, nil
}

// Reader returns an independent reader over the whole body, positioned at its
// start; it also implements io.Seeker and io.ReaderAt.
func (s *Spool) Reader() *io.SectionReader {
	return io.NewSectionReader(s, 0, s.size)
}

// Bytes returns the body as one slice: the memory copy itself (do not modify)
// when the body is small, otherwise it reads the file back. Only call it for
// bodies a handler already knows are small (descriptors, manifests).
func (s *Spool) Bytes() ([]byte, error) {
	if s.file == nil {
		return s.mem, nil
	}

	out := make([]byte, s.size)
	if _, err := s.file.ReadAt(out, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read spool file: %w", err)
	}

	return out, nil
}

// Close releases the spool and removes its temp file. Safe on nil and twice.
func (s *Spool) Close() {
	if s == nil || s.file == nil {
		return
	}

	_ = s.file.Close()
	_ = os.Remove(s.file.Name())
	s.file = nil
	s.mem = nil
}
