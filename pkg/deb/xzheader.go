// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// maxControlDictSize bounds the LZ window (xz dictionary / zstd window) a
// control archive may declare. The decoders allocate it up front, so a tiny
// crafted stream could otherwise demand gigabytes; 64 MiB covers `xz -9`.
const maxControlDictSize = 64 << 20

// ErrXZDictTooLarge is returned for an xz stream declaring an oversized dictionary.
var ErrXZDictTooLarge = errors.New("xz dictionary size exceeds limit")

var errXZHeader = errors.New("invalid xz block header")

const (
	xzStreamHeaderLen = 12
	xzFilterLZMA2     = 0x21
)

// checkXZDictCap reads the first block header of an xz stream and rejects it
// when its LZMA2 filter declares a dictionary larger than maxControlDictSize. (The
// ulikunitz/xz reader raises its dictionary to whatever the stream declares.)
// Anything it cannot parse is left to the decoder to reject.
func checkXZDictCap(raw []byte) error {
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
			if c := lzma2DictSize(props[0]); c > maxControlDictSize {
				return fmt.Errorf("%w: %d bytes", ErrXZDictTooLarge, c)
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
