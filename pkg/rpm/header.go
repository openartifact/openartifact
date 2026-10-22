// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Sentinel errors for RPM header parsing.
var (
	ErrInvalidHeaderMagic = errors.New("rpm: header magic")
	ErrInvalidHeader      = errors.New("rpm: invalid header")
	ErrHeaderTooLarge     = errors.New("rpm: header too large")
)

// Hard limits that bound the memory a hostile header can make the parser use.
const (
	// MaxHeaderEntries is rpm's own cap on index entries (hdrchkTags: fewer
	// than 0x10000), enforced regardless of the caller's byte limit.
	MaxHeaderEntries = 0xffff
	// MaxHeaderDataSize is the default (and ParseRPM's) cap on a header's
	// data store (rpm's hdrchkData: below 16 MiB).
	MaxHeaderDataSize = 1 << 24
	// MaxArrayEntries bounds how many elements of one array tag the getters
	// decode. A 16 MiB data store can otherwise declare ~16M one-byte
	// elements, which later code amplifies into gigabytes of structs.
	MaxArrayEntries = 100_000
)

const (
	TagTypeNULL         int32 = 0
	TagTypeCHAR         int32 = 1
	TagTypeINT8         int32 = 2
	TagTypeINT16        int32 = 3
	TagTypeINT32        int32 = 4
	TagTypeINT64        int32 = 5
	TagTypeSTRING       int32 = 6
	TagTypeBIN          int32 = 7
	TagTypeSTRING_ARRAY int32 = 8
	TagTypeI18NSTRING   int32 = 9
)

type TagEntry struct {
	Tag    int32
	Type   int32
	Offset int32
	Count  int32
}

type Header struct {
	Version int8
	NIndex  int32
	HSize   int32
	Entries []TagEntry
	Data    []byte
}

func ParseHeader(r io.Reader, maxLen int64) (*Header, error) {
	var intro [16]byte
	if _, err := io.ReadFull(r, intro[:]); err != nil {
		return nil, fmt.Errorf("rpm: reading header intro: %w", err)
	}

	if intro[0] != 0x8E || intro[1] != 0xAD || intro[2] != 0xE8 {
		return nil, fmt.Errorf("%w: got [%#x,%#x,%#x], want [0x8e,0xad,0xe8]",
			ErrInvalidHeaderMagic, intro[0], intro[1], intro[2])
	}

	version := int8(intro[3])                             //nolint:gosec // RPM binary format requires specific integer widths
	nindex := int32(binary.BigEndian.Uint32(intro[8:12])) //nolint:gosec // RPM binary format requires specific integer widths
	hsize := int32(binary.BigEndian.Uint32(intro[12:16])) //nolint:gosec // RPM binary format requires specific integer widths

	if nindex < 0 || hsize < 0 {
		return nil, fmt.Errorf("%w: nindex=%d, hsize=%d", ErrInvalidHeader, nindex, hsize)
	}

	if maxLen <= 0 {
		maxLen = MaxHeaderDataSize
	}

	if nindex > MaxHeaderEntries || int64(nindex) > maxLen/16 || int64(hsize) > maxLen {
		return nil, fmt.Errorf("%w: nindex=%d, hsize=%d, max=%d", ErrHeaderTooLarge, nindex, hsize, maxLen)
	}

	entries := make([]TagEntry, nindex)

	entryBuf := make([]byte, 16)
	for i := range nindex {
		if _, err := io.ReadFull(r, entryBuf); err != nil {
			return nil, fmt.Errorf("rpm: reading header entry %d: %w", i, err)
		}

		entries[i] = TagEntry{
			Tag:    int32(binary.BigEndian.Uint32(entryBuf[0:4])),   //nolint:gosec // RPM binary format requires specific integer widths
			Type:   int32(binary.BigEndian.Uint32(entryBuf[4:8])),   //nolint:gosec // RPM binary format requires specific integer widths
			Offset: int32(binary.BigEndian.Uint32(entryBuf[8:12])),  //nolint:gosec // RPM binary format requires specific integer widths
			Count:  int32(binary.BigEndian.Uint32(entryBuf[12:16])), //nolint:gosec // RPM binary format requires specific integer widths
		}
	}

	data := make([]byte, hsize)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("rpm: reading header data: %w", err)
	}

	return &Header{
		Version: version,
		NIndex:  nindex,
		HSize:   hsize,
		Entries: entries,
		Data:    data,
	}, nil
}

func (h *Header) findEntry(tag int32) *TagEntry {
	for i := range h.Entries {
		if h.Entries[i].Tag == tag {
			return &h.Entries[i]
		}
	}

	return nil
}

func (h *Header) GetString(tag int32) (string, bool) {
	entry := h.findEntry(tag)
	if entry == nil {
		return "", false
	}

	if entry.Type != TagTypeSTRING && entry.Type != TagTypeI18NSTRING {
		return "", false
	}

	if entry.Offset < 0 || int(entry.Offset) >= len(h.Data) {
		return "", false
	}

	data := h.Data[entry.Offset:]

	end := 0
	for end < len(data) && data[end] != 0 {
		end++
	}

	return string(data[:end]), true
}

func (h *Header) GetInt32s(tag int32) ([]int32, bool) {
	entry := h.findEntry(tag)
	if entry == nil {
		return nil, false
	}

	if entry.Type != TagTypeINT32 {
		return nil, false
	}

	if entry.Offset < 0 || entry.Count < 0 || int64(entry.Offset)+int64(entry.Count)*4 > int64(len(h.Data)) {
		return nil, false
	}

	n := min(entry.Count, MaxArrayEntries)

	result := make([]int32, n)
	for i := range n {
		off := int(entry.Offset) + int(i)*4
		result[i] = int32(binary.BigEndian.Uint32(h.Data[off : off+4])) //nolint:gosec // RPM binary format requires specific integer widths
	}

	return result, true
}

func (h *Header) GetUint32(tag int32) (uint32, bool) {
	vals, ok := h.GetInt32s(tag)
	if !ok || len(vals) == 0 {
		return 0, false
	}

	return uint32(vals[0]), true //nolint:gosec // RPM binary format requires specific integer widths
}

func (h *Header) GetInt64(tag int32) (int64, bool) {
	entry := h.findEntry(tag)
	if entry == nil {
		return 0, false
	}

	if entry.Type != TagTypeINT64 {
		return 0, false
	}

	if entry.Offset < 0 || int64(entry.Offset)+8 > int64(len(h.Data)) {
		return 0, false
	}

	off := int(entry.Offset)

	return int64(binary.BigEndian.Uint64(h.Data[off : off+8])), true //nolint:gosec // RPM binary format requires specific integer widths
}

func (h *Header) GetStringArray(tag int32) ([]string, bool) {
	entry := h.findEntry(tag)
	if entry == nil {
		return nil, false
	}

	if entry.Type != TagTypeSTRING_ARRAY && entry.Type != TagTypeI18NSTRING {
		return nil, false
	}

	if entry.Offset < 0 || entry.Count < 0 || int(entry.Offset) >= len(h.Data) {
		return nil, false
	}

	data := h.Data[entry.Offset:]

	limit := int(min(entry.Count, MaxArrayEntries))

	var result []string

	start := 0

	for i := range data {
		if len(result) >= limit {
			break
		}

		if data[i] == 0 {
			result = append(result, string(data[start:i]))
			start = i + 1
		}
	}

	return result, true
}

// ArrayLen returns the element count declared by the tag's index entry
// (0 when absent). It is the untruncated count: the array getters stop at
// MaxArrayEntries, so callers compare against it to detect truncation.
func (h *Header) ArrayLen(tag int32) int {
	entry := h.findEntry(tag)
	if entry == nil || entry.Count < 0 {
		return 0
	}

	return int(entry.Count)
}

// GetNumber returns an INT32 (unsigned) or INT64 tag as a non-negative int64.
func (h *Header) GetNumber(tag int32) (int64, bool) {
	if v, ok := h.GetInt64(tag); ok && v >= 0 {
		return v, true
	}

	if v, ok := h.GetUint32(tag); ok {
		return int64(v), true
	}

	return 0, false
}

// firstNumber returns the first present numeric tag of tags, else 0.
func (h *Header) firstNumber(tags ...int32) int64 {
	for _, tag := range tags {
		if v, ok := h.GetNumber(tag); ok {
			return v
		}
	}

	return 0
}

func (h *Header) TotalSize() int64 {
	return 16 + int64(h.NIndex)*16 + int64(h.HSize)
}

func PadTo8(total int64) int64 {
	rem := total % 8
	if rem == 0 {
		return 0
	}

	return 8 - rem
}
