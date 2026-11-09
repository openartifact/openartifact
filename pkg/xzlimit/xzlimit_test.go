// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package xzlimit

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/ulikunitz/xz"
)

func xzWithDict(t *testing.T, dictCap int) []byte {
	t.Helper()

	return xzWithPayload(t, dictCap, []byte("hello"))
}

func xzWithPayload(t *testing.T, dictCap int, payload []byte) []byte {
	t.Helper()

	var buf bytes.Buffer

	w, err := xz.WriterConfig{DictCap: dictCap}.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestCheckDictCap(t *testing.T) {
	if err := CheckDictCap(xzWithDict(t, 8<<20)); err != nil {
		t.Errorf("8 MiB dictionary rejected: %v", err)
	}

	err := CheckDictCap(xzWithDict(t, 512<<20))
	if !errors.Is(err, ErrDictTooLarge) {
		t.Errorf("512 MiB dictionary: err = %v, want ErrDictTooLarge", err)
	}

	// Truncated or non-xz input is left for the decoder to reject.
	if err := CheckDictCap([]byte("not xz")); err != nil {
		t.Errorf("garbage: %v", err)
	}
}

func TestLZMA2DictSize(t *testing.T) {
	cases := map[byte]int64{0: 4096, 1: 6144, 2: 8192, 18: 2 << 20, 22: 8 << 20, 24: 16 << 20, 40: 1<<32 - 1}
	for b, want := range cases {
		if got := lzma2DictSize(b); got != want {
			t.Errorf("lzma2DictSize(%d) = %d, want %d", b, got, want)
		}
	}
}

func xzBlocks(t *testing.T, dicts ...int) []byte {
	t.Helper()

	// Build one stream with several blocks by hand: reuse single-block streams'
	// block bodies, splicing them under the first stream header.
	// Incompressible first block, larger than the eager peek window.
	noise := make([]byte, 4096)
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range noise {
		noise[i] = byte(rng.Uint32())
	}

	first := xzWithPayload(t, dicts[0], noise)
	hdrEnd := xzStreamHeaderLen
	// Block = from hdrEnd to the index indicator; find it via the stream footer.
	idxSize := (int(first[len(first)-8]) | int(first[len(first)-7])<<8 | int(first[len(first)-6])<<16 | int(first[len(first)-5])<<24 + 1) * 4
	blocks := first[hdrEnd : len(first)-12-idxSize]

	out := append([]byte{}, first[:hdrEnd]...)
	out = append(out, blocks...)

	for _, d := range dicts[1:] {
		o := xzWithDict(t, d)
		isz := (int(o[len(o)-8]) | int(o[len(o)-7])<<8 | int(o[len(o)-6])<<16 | int(o[len(o)-5])<<24 + 1) * 4
		out = append(out, o[hdrEnd:len(o)-12-isz]...)
	}

	// Index/footer of the first stream no longer match the block count, which is
	// fine: the guard rejects before the decoder reaches them.
	return append(out, first[len(first)-12-idxSize:]...)
}

func TestNewReaderChecksEveryBlock(t *testing.T) {
	big := xzBlocks(t, 8<<20, 512<<20)
	if err := CheckDictCap(big); !errors.Is(err, ErrDictTooLarge) {
		t.Errorf("CheckDictCap second block: err = %v, want ErrDictTooLarge", err)
	}

	// Large first block pushes the hostile block past the eager peek, so only the
	// streaming guard can catch it.
	r, err := NewReader(bufio.NewReaderSize(bytes.NewReader(big), 64<<10))
	if err == nil {
		_, err = io.ReadAll(r)
	}

	if !errors.Is(err, ErrDictTooLarge) {
		t.Errorf("NewReader second block: err = %v, want ErrDictTooLarge", err)
	}
}

func mustReader(t *testing.T, b []byte) io.Reader {
	t.Helper()

	r, err := NewReader(bufio.NewReaderSize(bytes.NewReader(b), 64<<10))
	if err != nil {
		t.Fatal(err)
	}

	return r
}

func TestNewReaderSingleStream(t *testing.T) {
	good := xzWithDict(t, 8<<20)

	got, err := io.ReadAll(mustReader(t, good))
	if err != nil || string(got) != "hello" {
		t.Fatalf("single stream: %q, %v", got, err)
	}

	// A second stream with an oversized dictionary must not be decoded.
	cat := append(append([]byte{}, good...), xzWithDict(t, 512<<20)...)

	if _, err := io.ReadAll(mustReader(t, cat)); err == nil {
		t.Error("concatenated streams accepted")
	}
}

func TestHostileBlockBeyondPeekWindow(t *testing.T) {
	big := xzBlocks(t, 8<<20, 512<<20)
	if err := CheckDictCap(big[:maxHeaderPeek]); err != nil {
		t.Fatalf("hostile block unexpectedly inside the peek window: %v", err)
	}
}
