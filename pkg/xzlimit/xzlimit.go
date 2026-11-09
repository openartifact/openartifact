// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package xzlimit guards the ulikunitz/xz decoder against streams that declare
// an oversized dictionary: it allocates whatever the header asks for, up to
// 4 GiB, before reading any data.
package xzlimit

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/ulikunitz/xz"
)

// MaxDictSize bounds the LZ window (xz dictionary / zstd window) an untrusted
// stream may declare. The decoders allocate it up front, so a tiny crafted
// stream could otherwise demand gigabytes; 64 MiB covers `xz -9`.
const MaxDictSize = 64 << 20

// NewReader returns an xz decoder over br. Every block header of the stream is
// inspected before the decoder sees it, and a block declaring a dictionary
// above MaxDictSize is rejected with ErrDictTooLarge. The decoder is limited to
// a single stream (trailing data or a second stream is an error), because the
// decoder raises its dictionary to whatever each later stream or block declares.
func NewReader(br *bufio.Reader) (io.Reader, error) {
	// Vet the first block eagerly so a hostile header fails here, not on first Read.
	head, _ := br.Peek(maxHeaderPeek) // a short peek is left for the decoder to reject
	if err := CheckDictCap(head); err != nil {
		return nil, err
	}

	return xz.ReaderConfig{SingleStream: true}.NewReader(&guard{r: br, s: newScanner()})
}

// ErrDictTooLarge is returned for an xz stream declaring an oversized dictionary.
var ErrDictTooLarge = errors.New("xz dictionary size exceeds limit")

var errXZHeader = errors.New("invalid xz block header")

// maxHeaderPeek covers the stream header plus the largest block header
// ((255+1)*4 bytes).
const maxHeaderPeek = xzStreamHeaderLen + 1024

const (
	xzStreamHeaderLen = 12
	xzFilterLZMA2     = 0x21
)

// CheckDictCap walks the blocks of the (first) xz stream in raw and rejects it
// when any block's LZMA2 filter declares a dictionary larger than MaxDictSize.
// Anything it cannot parse is left to the decoder to reject. It does not
// inspect further streams; use NewReader for untrusted streaming input.
func CheckDictCap(raw []byte) error {
	s := newScanner()

	return s.feed(raw)
}

// guard feeds everything the decoder reads through the scanner first, so a
// block header is vetted before the decoder can act on it.
type guard struct {
	r io.Reader
	s scanner
}

func (g *guard) Read(p []byte) (int, error) {
	n, err := g.r.Read(p)
	if n > 0 {
		if serr := g.s.feed(p[:n]); serr != nil {
			return 0, serr
		}
	}

	return n, err
}

type scanState int

const (
	stStreamHeader scanState = iota // 12 bytes
	stBlockFirst                    // size byte of a block header, or 0 = index
	stBlockHeader                   // rest of the block header
	stChunkControl                  // LZMA2 control byte
	stChunkHeader                   // LZMA2 chunk header after the control byte
	stSkip                          // discard skip bytes, then go to after
	stDone                          // index reached or unparseable: decoder takes over
)

// scanner is a streaming parser that tracks block boundaries of one xz stream.
// LZMA2 chunk headers carry their sizes, so blocks can be walked without
// decoding.
type scanner struct {
	st    scanState
	after scanState
	buf   []byte
	want  int
	skip  int64

	checkLen int   // size of the integrity check field
	hdrLen   int   // current block header size
	blockLen int64 // block header + compressed bytes seen so far
	ctl      byte  // current LZMA2 control byte
}

func newScanner() scanner {
	return scanner{st: stStreamHeader, want: xzStreamHeaderLen}
}

func (s *scanner) gather(st scanState, n int) {
	s.st, s.want, s.buf = st, n, s.buf[:0]
}

func (s *scanner) skipThen(n int64, next scanState) {
	s.st, s.skip, s.after = stSkip, n, next
	if n == 0 {
		s.st = next
	}
}

func (s *scanner) feed(p []byte) error {
	for len(p) > 0 && s.st != stDone {
		switch s.st {
		case stSkip:
			n := min(s.skip, int64(len(p)))
			p = p[n:]
			s.skip -= n

			if s.skip == 0 {
				s.st = s.after
			}

			continue
		case stBlockFirst, stChunkControl:
			s.buf = append(s.buf[:0], p[0])
			p = p[1:]
		default:
			take := min(s.want-len(s.buf), len(p))
			s.buf = append(s.buf, p[:take]...)
			p = p[take:]

			if len(s.buf) < s.want {
				continue
			}
		}

		if err := s.step(); err != nil {
			return err
		}
	}

	return nil
}

// step runs once the bytes of the current state are in s.buf.
func (s *scanner) step() error {
	switch s.st {
	case stStreamHeader:
		n, ok := checkFieldLen(s.buf[7]) // check type from the stream flags
		if !ok {
			s.st = stDone // unsupported: the decoder rejects it

			return nil
		}

		s.checkLen = n
		s.st = stBlockFirst
	case stBlockFirst:
		if s.buf[0] == 0 { // index: no more blocks
			s.st = stDone

			return nil
		}

		s.hdrLen = (int(s.buf[0]) + 1) * 4
		s.blockLen = int64(s.hdrLen)
		size := s.buf[0]
		s.gather(stBlockHeader, s.hdrLen)
		s.buf = append(s.buf, size)
	case stBlockHeader:
		last, err := checkBlockHeader(s.buf)
		if err != nil {
			return err
		}

		if last != xzFilterLZMA2 {
			s.st = stDone // the decoder only supports LZMA2

			return nil
		}

		s.st = stChunkControl
	case stChunkControl:
		s.ctl = s.buf[0]
		s.blockLen++

		switch {
		case s.ctl == 0:
			pad := (4 - s.blockLen%4) % 4
			s.skipThen(pad+int64(s.checkLen), stBlockFirst)
		case s.ctl == 1 || s.ctl == 2:
			s.gather(stChunkHeader, 2)
		case s.ctl >= 0x80:
			n := 4
			if s.ctl >= 0xc0 {
				n = 5
			}

			s.gather(stChunkHeader, n)
		default:
			s.st = stDone // invalid control byte: the decoder rejects it
		}
	case stChunkHeader:
		var data int64

		if s.ctl <= 2 {
			data = int64(s.buf[0])<<8 | int64(s.buf[1]) + 1
		} else {
			data = int64(s.buf[2])<<8 | int64(s.buf[3]) + 1
		}

		s.blockLen += int64(len(s.buf)) + data
		s.skipThen(data, stChunkControl)
	}

	return nil
}

// checkBlockHeader parses a whole block header (size byte included) and
// returns the ID of its last filter, rejecting a dictionary above MaxDictSize.
// Malformed headers return id 0 and no error: the decoder reports them.
func checkBlockHeader(hdr []byte) (uint64, error) {
	flags := hdr[1]
	pos := 2

	if flags&0x40 != 0 { // compressed size
		n, err := skipVarint(hdr, pos)
		if err != nil {
			return 0, nil //nolint:nilerr // the decoder reports malformed headers
		}

		pos = n
	}

	if flags&0x80 != 0 { // uncompressed size
		n, err := skipVarint(hdr, pos)
		if err != nil {
			return 0, nil //nolint:nilerr // the decoder reports malformed headers
		}

		pos = n
	}

	var id uint64

	for range int(flags&0x03) + 1 {
		var n int

		id, n = binary.Uvarint(hdr[pos:])
		if n <= 0 {
			return 0, nil
		}

		pos += n

		propLen, n := binary.Uvarint(hdr[pos:])
		if n <= 0 || propLen > uint64(len(hdr)-pos-n) { //nolint:gosec // len(hdr)-pos-n is non-negative: n <= len(hdr[pos:])
			return 0, nil
		}

		pos += n
		props := hdr[pos : pos+int(propLen)] //nolint:gosec // propLen <= len(hdr) checked above
		pos += int(propLen)                  //nolint:gosec // propLen <= len(hdr) checked above

		if id == xzFilterLZMA2 && len(props) == 1 {
			if c := lzma2DictSize(props[0]); c > MaxDictSize {
				return id, fmt.Errorf("%w: %d bytes", ErrDictTooLarge, c)
			}
		}
	}

	return id, nil
}

// checkFieldLen maps an xz check type to its integrity field size.
func checkFieldLen(typ byte) (int, bool) {
	switch typ {
	case 0x00:
		return 0, true
	case 0x01:
		return 4, true
	case 0x04:
		return 8, true
	case 0x0a:
		return 32, true
	}

	return 0, false
}

func skipVarint(b []byte, pos int) (int, error) {
	if pos >= len(b) {
		return 0, errXZHeader
	}

	_, n := binary.Uvarint(b[pos:])
	if n <= 0 {
		return 0, errXZHeader
	}

	return pos + n, nil
}

// lzma2DictSize decodes the LZMA2 dictionary-size property byte.
func lzma2DictSize(b byte) int64 {
	if b >= 40 {
		return 1<<32 - 1
	}

	return int64(2|(b&1)) << (b/2 + 11)
}
