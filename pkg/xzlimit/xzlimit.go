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

// maxHeaderPeek covers the stream header plus the largest block header
// ((255+1)*4 bytes).
const maxHeaderPeek = xzStreamHeaderLen + 1024

// NewReader returns an xz decoder over br after rejecting a stream whose first
// block declares a dictionary above MaxDictSize. Nothing is consumed by the
// check; br only has to be at least maxHeaderPeek bytes large.
func NewReader(br *bufio.Reader) (io.Reader, error) {
	head, _ := br.Peek(maxHeaderPeek) // a short peek is left for the decoder to reject

	if err := CheckDictCap(head); err != nil {
		return nil, err
	}

	return xz.NewReader(br)
}

// ErrDictTooLarge is returned for an xz stream declaring an oversized dictionary.
var ErrDictTooLarge = errors.New("xz dictionary size exceeds limit")

var errXZHeader = errors.New("invalid xz block header")

const (
	xzStreamHeaderLen = 12
	xzFilterLZMA2     = 0x21
)

// CheckDictCap reads the first block header of an xz stream and rejects it
// when its LZMA2 filter declares a dictionary larger than MaxDictSize. (The
// ulikunitz/xz reader raises its dictionary to whatever the stream declares.)
// Anything it cannot parse is left to the decoder to reject.
func CheckDictCap(raw []byte) error {
	if len(raw) < xzStreamHeaderLen+2 {
		return nil
	}

	hdr := raw[xzStreamHeaderLen:]
	if hdr[0] == 0 { // index indicator: no blocks
		return nil
	}

	size := (int(hdr[0]) + 1) * 4
	if size > len(hdr) {
		return nil
	}

	hdr = hdr[:size]
	flags := hdr[1]
	pos := 2

	if flags&0x40 != 0 { // compressed size
		n, err := skipVarint(hdr, pos)
		if err != nil {
			return nil //nolint:nilerr // the decoder reports malformed headers
		}

		pos = n
	}

	if flags&0x80 != 0 { // uncompressed size
		n, err := skipVarint(hdr, pos)
		if err != nil {
			return nil //nolint:nilerr // the decoder reports malformed headers
		}

		pos = n
	}

	for range int(flags&0x03) + 1 {
		id, n := binary.Uvarint(hdr[pos:])
		if n <= 0 {
			return nil
		}

		pos += n

		propLen, n := binary.Uvarint(hdr[pos:])
		if n <= 0 || propLen > uint64(len(hdr)-pos-n) { //nolint:gosec // len(hdr)-pos-n is non-negative: n <= len(hdr[pos:])
			return nil
		}

		pos += n
		props := hdr[pos : pos+int(propLen)] //nolint:gosec // propLen <= len(hdr) checked above
		pos += int(propLen)                  //nolint:gosec // propLen <= len(hdr) checked above

		if id == xzFilterLZMA2 && len(props) == 1 {
			if c := lzma2DictSize(props[0]); c > MaxDictSize {
				return fmt.Errorf("%w: %d bytes", ErrDictTooLarge, c)
			}
		}
	}

	return nil
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
