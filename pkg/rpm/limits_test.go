// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"testing"
)

// The documented limits, spelled out so a test run cannot pass by raising a
// constant: TestLimitConstants pins them to these values.
const (
	wantEntryCap    = 0xffff
	wantArrayCap    = 100_000
	wantFileListCap = 16 << 20
	wantDataCap     = 1 << 24
)

func TestLimitConstants(t *testing.T) {
	if MaxHeaderEntries != wantEntryCap || MaxArrayEntries != wantArrayCap ||
		MaxFileListBytes != wantFileListCap || MaxHeaderDataSize != wantDataCap {
		t.Fatalf("limits changed: entries=%d array=%d filelist=%d data=%d",
			MaxHeaderEntries, MaxArrayEntries, MaxFileListBytes, MaxHeaderDataSize)
	}
}

// craft assembles a header store entry by entry.
type craft struct {
	entries []TagEntry
	data    bytes.Buffer
}

func (c *craft) align4() {
	for c.data.Len()%4 != 0 {
		c.data.WriteByte(0)
	}
}

func (c *craft) str(tag TagType, v string) {
	c.entries = append(c.entries, TagEntry{Tag: tag, Type: TagTypeSTRING, Offset: int32(c.data.Len()), Count: 1})
	c.data.WriteString(v)
	c.data.WriteByte(0)
}

// strArray stores raw NUL-terminated strings but declares count elements.
func (c *craft) strArray(tag TagType, count int32, raw []byte) {
	c.entries = append(c.entries, TagEntry{Tag: tag, Type: TagTypeSTRING_ARRAY, Offset: int32(c.data.Len()), Count: count})
	c.data.Write(raw)
}

func (c *craft) int32s(tag TagType, vals ...int32) {
	c.align4()
	c.entries = append(c.entries, TagEntry{Tag: tag, Type: TagTypeINT32, Offset: int32(c.data.Len()), Count: int32(len(vals))})

	for _, v := range vals {
		_ = binary.Write(&c.data, binary.BigEndian, v)
	}
}

func (c *craft) identity() {
	c.str(RPMTAG_NAME, "bomb")
	c.str(RPMTAG_VERSION, "1")
	c.str(RPMTAG_RELEASE, "1")
	c.str(RPMTAG_ARCH, "noarch")
}

// rpmBytes returns lead + empty signature header + the crafted main header.
func (c *craft) rpmBytes() []byte {
	var buf bytes.Buffer

	buf.Write(buildValidLead())

	sig := buildRawHeader(nil, nil)
	buf.Write(sig)
	buf.Write(make([]byte, PadTo8(int64(len(sig)))))
	buf.Write(buildRawHeader(c.entries, c.data.Bytes()))

	return buf.Bytes()
}

// emptyStrings is n NUL bytes: n empty strings.
func emptyStrings(n int) []byte { return make([]byte, n) }

func allocatedBy(f func()) uint64 {
	var before, after runtime.MemStats

	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)

	return after.TotalAlloc - before.TotalAlloc
}

func TestParseHeaderRejectsExcessIndexEntries(t *testing.T) {
	for _, maxLen := range []int64{wantDataCap, 1 << 30, 0} {
		intro := make([]byte, 16)
		copy(intro, []byte{0x8E, 0xAD, 0xE8, 1})
		binary.BigEndian.PutUint32(intro[8:12], wantEntryCap+1)

		// Only the intro is provided: the limit must trip before any entry is read.
		_, err := ParseHeader(bytes.NewReader(intro), maxLen)
		if !errors.Is(err, ErrHeaderTooLarge) {
			t.Errorf("maxLen=%d: want ErrHeaderTooLarge, got %v", maxLen, err)
		}
	}
}

func TestParseHeaderDefaultsDataLimit(t *testing.T) {
	intro := make([]byte, 16)
	copy(intro, []byte{0x8E, 0xAD, 0xE8, 1})
	binary.BigEndian.PutUint32(intro[12:16], wantDataCap+1)

	if _, err := ParseHeader(bytes.NewReader(intro), 0); !errors.Is(err, ErrHeaderTooLarge) {
		t.Fatalf("maxLen=0 must not mean unlimited: got %v", err)
	}
}

func TestParseRPMBoundsHugeFileList(t *testing.T) {
	const declared = 1 << 21 // 2M one-byte entries in a 2 MiB store

	var c craft

	c.identity()
	c.strArray(RPMTAG_DIRNAMES, 1, []byte("/\x00"))
	c.strArray(RPMTAG_BASENAMES, declared, emptyStrings(declared))

	data := c.rpmBytes()

	var (
		pkg      *Package
		extraLen int
	)

	alloc := allocatedBy(func() {
		var err error

		pkg, err = ParseRPM(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("ParseRPM: %v", err)
		}

		extra, err := pkg.MarshalExtra()
		if err != nil {
			t.Fatalf("MarshalExtra: %v", err)
		}

		extraLen = len(extra)
	})

	if len(pkg.Files) > wantArrayCap {
		t.Fatalf("kept %d files, cap is %d", len(pkg.Files), wantArrayCap)
	}

	if !pkg.FilesTruncated {
		t.Error("FilesTruncated not set although the list was cut")
	}

	if pkg.GetMetadata()["files_truncated"] != true {
		t.Error("metadata does not report the truncation")
	}

	if extraLen > 64<<20 {
		t.Errorf("persisted extra is %d bytes", extraLen)
	}

	if alloc > 256<<20 {
		t.Errorf("parsing a %d-byte package allocated %d MiB", len(data), alloc>>20)
	}
}

// A header that fills the whole 16 MiB data store with empty basenames is the
// reviewed worst case (16.7M entries, ~11 GB before the cap).
func TestParseRPMBoundsMaxSizedFileList(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a 16 MiB header")
	}

	n := wantDataCap - 64

	var c craft

	c.identity()
	c.strArray(RPMTAG_DIRNAMES, 1, []byte("/\x00"))

	// The store must stay below the data limit once the identity strings are in.
	n -= c.data.Len()
	c.strArray(RPMTAG_BASENAMES, int32(n), emptyStrings(n))

	data := c.rpmBytes()

	alloc := allocatedBy(func() {
		pkg, err := ParseRPM(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("ParseRPM: %v", err)
		}

		if len(pkg.Files) != wantArrayCap || !pkg.FilesTruncated {
			t.Fatalf("files=%d truncated=%v", len(pkg.Files), pkg.FilesTruncated)
		}
	})

	if alloc > 512<<20 {
		t.Errorf("allocated %d MiB", alloc>>20)
	}
}

// Every file shares one huge dirname; the entry count alone would let
// len(files) * len(dirname) bytes be materialised.
func TestParseRPMBoundsExpandedPathBytes(t *testing.T) {
	const (
		dirLen = 256 << 10
		nFiles = 2000
	)

	dir := bytes.Repeat([]byte("d"), dirLen-1)
	dir = append(dir, '/', 0)

	bases := bytes.Repeat([]byte("f\x00"), nFiles)

	var c craft

	c.identity()
	c.strArray(RPMTAG_DIRNAMES, 1, dir)
	c.strArray(RPMTAG_BASENAMES, nFiles, bases)
	c.int32s(RPMTAG_DIRINDEXES, make([]int32, nFiles)...)

	pkg, err := ParseRPM(bytes.NewReader(c.rpmBytes()))
	if err != nil {
		t.Fatalf("ParseRPM: %v", err)
	}

	total := 0
	for _, f := range pkg.Files {
		total += len(f.Path)
	}

	if total > wantFileListCap {
		t.Errorf("materialised %d path bytes, cap %d", total, wantFileListCap)
	}

	if len(pkg.Files) >= nFiles || !pkg.FilesTruncated {
		t.Errorf("files=%d truncated=%v: list was not cut", len(pkg.Files), pkg.FilesTruncated)
	}
}

func TestParseRPMRejectsOversizedDependencyList(t *testing.T) {
	const declared = wantArrayCap + 1

	var c craft

	c.identity()
	c.strArray(RPMTAG_REQUIRENAME, declared, emptyStrings(declared))

	_, err := ParseRPM(bytes.NewReader(c.rpmBytes()))
	if !errors.Is(err, ErrHeaderTooLarge) {
		t.Fatalf("want ErrHeaderTooLarge, got %v", err)
	}
}

func TestParseRPMRejectsExcessIndexEntries(t *testing.T) {
	var buf bytes.Buffer

	buf.Write(buildValidLead())

	sig := buildRawHeader(nil, nil)
	buf.Write(sig)
	buf.Write(make([]byte, PadTo8(int64(len(sig)))))

	// Main header claims 1M index entries (16 MiB, within the data limit).
	intro := make([]byte, 16)
	copy(intro, []byte{0x8E, 0xAD, 0xE8, 1})
	binary.BigEndian.PutUint32(intro[8:12], 1<<20)
	buf.Write(intro)

	if _, err := ParseRPM(&buf); !errors.Is(err, ErrHeaderTooLarge) {
		t.Fatalf("want ErrHeaderTooLarge, got %v", err)
	}
}

func TestGetInt32sBoundedByArrayCap(t *testing.T) {
	const declared = wantArrayCap + 10

	h := &Header{
		Data:    make([]byte, declared*4),
		Entries: []TagEntry{{Tag: 1, Type: TagTypeINT32, Offset: 0, Count: declared}},
	}

	vals, ok := h.GetInt32s(1)
	if !ok || len(vals) != wantArrayCap {
		t.Fatalf("ok=%v len=%d, want %d", ok, len(vals), wantArrayCap)
	}

	if h.ArrayLen(1) != declared {
		t.Errorf("ArrayLen = %d, want the declared %d", h.ArrayLen(1), declared)
	}
}
