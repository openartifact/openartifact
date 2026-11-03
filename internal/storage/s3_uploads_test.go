// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Tiny parts keep the cutter tests fast: a part is 16 bytes, a remainder of
// at least 5 bytes is a part of its own.
const (
	testPartSize = 16
	testMinPart  = 5
)

var errCutterTest = errors.New("injected failure")

// cutRecorder collects what a cutter stored.
type cutRecorder struct {
	mu    sync.Mutex
	parts map[int][]byte
	tails map[int64][]byte
}

func newCutRecorder() *cutRecorder {
	return &cutRecorder{parts: map[int][]byte{}, tails: map[int64][]byte{}}
}

func (c *cutRecorder) cutter(first int) *partCutter {
	return &partCutter{
		pool: newPartPool(testPartSize), min: testMinPart, inflight: uploadPartsInFlight, first: first,
		put: func(_ context.Context, n int, data []byte) error {
			c.mu.Lock()
			defer c.mu.Unlock()

			c.parts[n] = bytes.Clone(data)

			return nil
		},
		tail: func(_ context.Context, end int64, data []byte) error {
			c.mu.Lock()
			defer c.mu.Unlock()

			c.tails[end] = bytes.Clone(data)

			return nil
		},
	}
}

func testBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}

	return b
}

// Whatever the stream length, the parts in number order followed by the tail are
// exactly head and stream; parts are full except possibly the last, and the
// tail is below the minimum part size and ends where the stream does.
func TestPartCutterSplitsEveryLength(t *testing.T) {
	t.Parallel()

	for head := 1; head <= 4; head += 3 {
		for size := 0; size <= 3*testPartSize+testMinPart+2; size++ {
			t.Run(fmt.Sprintf("head%d/stream%d", head, size), func(t *testing.T) {
				t.Parallel()

				data := testBytes(head + size)
				rec := newCutRecorder()
				c := rec.cutter(3)

				buf := newPartPool(testPartSize).Get().(*partBuf)
				copy(buf.b, data[:head])

				res, err := c.cut(t.Context(), buf, head, bytes.NewReader(data[head:]))
				if err != nil {
					t.Fatal(err)
				}

				if res.total != int64(len(data)) {
					t.Fatalf("total = %d, want %d", res.total, len(data))
				}

				checkCut(t, rec, res, data, 3)
			})
		}
	}
}

func checkCut(t *testing.T, rec *cutRecorder, res cutResult, data []byte, first int) {
	t.Helper()

	var got []byte

	for n := first; n < first+res.parts; n++ {
		part, ok := rec.parts[n]
		if !ok {
			t.Fatalf("part %d missing, have %d parts", n, len(rec.parts))
		}

		if n < first+res.parts-1 && len(part) != testPartSize {
			t.Fatalf("part %d is %d bytes, want %d", n, len(part), testPartSize)
		}

		if len(part) < testMinPart && n < first+res.parts-1 {
			t.Fatalf("part %d is below the minimum", n)
		}

		got = append(got, part...)
	}

	if len(rec.parts) != res.parts {
		t.Fatalf("stored %d parts, reported %d", len(rec.parts), res.parts)
	}

	if int64(len(got)) != res.partBytes {
		t.Fatalf("parts hold %d bytes, reported %d", len(got), res.partBytes)
	}

	if rest := int64(len(data)) - res.partBytes; rest > 0 && rest < testMinPart {
		tail, ok := rec.tails[int64(len(data))]
		if !ok || len(rec.tails) != 1 || int64(len(tail)) != rest {
			t.Fatalf("tail = %v (%d tails), want one of %d bytes ending at %d", tail, len(rec.tails), rest, len(data))
		}

		got = append(got, tail...)
	} else if len(rec.tails) != 0 {
		t.Fatalf("unexpected tails %v for a remainder of %d", rec.tails, rest)
	}

	if !bytes.Equal(got, data) {
		t.Fatal("parts and tail do not add up to the stream")
	}
}

// A reader that fails (also with io.ErrUnexpectedEOF, which io.ReadFull would
// take for a clean end) fails the cut, and every buffer is back in the pool.
func TestPartCutterReaderErrors(t *testing.T) {
	t.Parallel()

	for _, readErr := range []error{errCutterTest, io.ErrUnexpectedEOF} {
		for _, n := range []int{0, 3, testPartSize, 2*testPartSize + 3} {
			t.Run(fmt.Sprintf("%v/%d", readErr, n), func(t *testing.T) {
				t.Parallel()

				rec := newCutRecorder()
				c := rec.cutter(1)
				buf := c.pool.Get().(*partBuf)
				r := io.MultiReader(bytes.NewReader(testBytes(n)), iotest.ErrReader(readErr))

				if _, err := c.cut(t.Context(), buf, 1, r); !errors.Is(err, readErr) {
					t.Fatalf("cut = %v, want %v", err, readErr)
				}

				if len(rec.tails) != 0 {
					t.Fatalf("a failed cut stored a tail: %v", rec.tails)
				}
			})
		}
	}
}

// A part that cannot be sent fails the cut, wherever it is in the stream.
func TestPartCutterPutErrors(t *testing.T) {
	t.Parallel()

	rec := newCutRecorder()
	c := rec.cutter(1)
	c.put = func(_ context.Context, n int, _ []byte) error {
		if n == 2 {
			return errCutterTest
		}

		return nil
	}

	buf := c.pool.Get().(*partBuf)

	_, err := c.cut(t.Context(), buf, 1, bytes.NewReader(testBytes(10*testPartSize)))
	if !errors.Is(err, errCutterTest) {
		t.Fatalf("cut = %v, want the part error", err)
	}
}

// The stream stops being read when it would need a part beyond MaxParts, and
// the upload is reported as too large.
func TestPartCutterTooLarge(t *testing.T) {
	t.Parallel()

	rec := newCutRecorder()
	c := rec.cutter(MaxParts)
	buf := c.pool.Get().(*partBuf)

	r := bytes.NewReader(testBytes(5 * testPartSize))

	_, err := c.cut(t.Context(), buf, 1, r)
	if !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("cut = %v, want ErrUploadTooLarge", err)
	}

	if r.Len() < 3*testPartSize {
		t.Fatalf("%d bytes of the stream were read after the limit", 5*testPartSize-r.Len())
	}

	// The last part allowed is fine.
	rec = newCutRecorder()
	c = rec.cutter(MaxParts)
	buf = c.pool.Get().(*partBuf)

	if _, err := c.cut(t.Context(), buf, 1, bytes.NewReader(testBytes(testPartSize-1))); err != nil {
		t.Fatalf("cut of the last part allowed = %v", err)
	}
}

// No more than inflight parts are sent at once, and the stream is not read
// ahead of them by more than the part being filled.
func TestPartCutterBoundsParts(t *testing.T) {
	t.Parallel()

	rec := newCutRecorder()
	c := rec.cutter(1)

	var current, peak, consumed atomic.Int64

	release := make(chan struct{})
	c.put = func(_ context.Context, _ int, _ []byte) error {
		now := current.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}

		<-release
		current.Add(-1)

		return nil
	}

	src := bytes.NewReader(testBytes(10 * testPartSize))
	r := readerFunc(func(p []byte) (int, error) {
		n, err := src.Read(p)
		consumed.Add(int64(n))

		return n, err
	})

	done := make(chan error, 1)

	go func() {
		buf := c.pool.Get().(*partBuf)
		_, err := c.cut(context.Background(), buf, 1, r)
		done <- err
	}()

	deadline := time.Now().Add(10 * time.Second)

	for current.Load() < uploadPartsInFlight {
		if time.Now().After(deadline) {
			t.Fatalf("only %d parts in flight", current.Load())
		}

		time.Sleep(time.Millisecond)
	}

	// Everything sent plus the buffer being filled; nothing further.
	if got, limit := consumed.Load(), int64((uploadPartsInFlight+1)*testPartSize); got > limit {
		t.Errorf("read %d bytes ahead of the parts in flight, limit %d", got, limit)
	}

	close(release)

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if got := peak.Load(); got != uploadPartsInFlight {
		t.Errorf("peak parts in flight = %d, want %d", got, uploadPartsInFlight)
	}
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestUploadState(t *testing.T) {
	t.Parallel()

	good := []struct {
		state  s3UploadState
		offset int64
		want   string
	}{
		{s3UploadState{}, 0, ""},
		{s3UploadState{}, 1000, ""},
		{s3UploadState{U: "abc", P: 2, B: 32 << 20}, 32 << 20, `{"u":"abc","p":2,"b":33554432}`},
		{s3UploadState{U: "abc", P: 1, B: 16 << 20}, 16<<20 + 7, `{"u":"abc","p":1,"b":16777216}`},
	}

	for _, tc := range good {
		enc := tc.state.String()
		if enc != tc.want {
			t.Errorf("String(%+v) = %q, want %q", tc.state, enc, tc.want)
		}

		got, err := parseUploadState(enc, tc.offset)
		if err != nil || got != tc.state {
			t.Errorf("parseUploadState(%q, %d) = %+v, %v", enc, tc.offset, got, err)
		}
	}

	bad := []struct {
		name   string
		state  string
		offset int64
	}{
		{"not json", "{", 0},
		{"bytes beyond the offset", `{"u":"a","p":1,"b":100}`, 99},
		{"tail of a part or more", `{"u":"a","p":1,"b":10}`, 10 + minUploadPart},
		{"negative bytes", `{"u":"a","p":1,"b":-1}`, 5},
		{"parts without upload", `{"p":1,"b":5}`, 5},
		{"upload without parts", `{"u":"a"}`, 0},
		{"too many parts", `{"u":"a","p":10001,"b":5}`, 5},
	}

	for _, tc := range bad {
		if _, err := parseUploadState(tc.state, tc.offset); !errors.Is(err, ErrUploadDataLost) {
			t.Errorf("%s: parseUploadState = %v, want ErrUploadDataLost", tc.name, err)
		}
	}
}

func TestUploadObjectID(t *testing.T) {
	t.Parallel()

	const id = "0b8f1a62-8a7c-4d2e-9c1e-0123456789ab"

	tests := []struct {
		key  string
		want string
		ok   bool
	}{
		{"uploads/" + id, id, true},
		{"uploads/" + id + ".1048576", id, true},
		{"uploads/" + id + ".0", id, true},
		{"uploads/not-a-uuid", "", false},
		{"uploads/" + id[:35], "", false},
		{"uploads/", "", false},
	}

	for _, tc := range tests {
		got, ok := uploadObjectID(tc.key)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("uploadObjectID(%q) = %q, %v; want %q, %v", tc.key, got, ok, tc.want, tc.ok)
		}
	}

	if got := tailKey("uploads/"+id, 42); got != "uploads/"+id+".42" {
		t.Errorf("tailKey = %q", got)
	}
}

func TestUploadPartsChecksPartsAndSize(t *testing.T) {
	t.Parallel()

	part := func(n int32, size int64) types.Part {
		return types.Part{PartNumber: aws.Int32(n), Size: aws.Int64(size), ETag: aws.String("e")}
	}

	tests := []struct {
		name   string
		parts  []types.Part
		count  int
		offset int64
		ok     bool
	}{
		{"exact", []types.Part{part(1, 10), part(2, 20)}, 2, 30, true},
		{"leftover part beyond count is left out", []types.Part{part(1, 10), part(2, 20), part(3, 99)}, 2, 30, true},
		{"missing part", []types.Part{part(1, 10), part(3, 5)}, 3, 15, false},
		{"wrong sum", []types.Part{part(1, 10), part(2, 20)}, 2, 31, false},
		{"fewer parts than recorded", []types.Part{part(1, 10)}, 2, 10, false},
	}

	for _, tc := range tests {
		completed, err := uploadParts(tc.parts, tc.count, tc.offset)

		switch {
		case tc.ok && (err != nil || len(completed) != tc.count):
			t.Errorf("%s: uploadParts = %d parts, %v", tc.name, len(completed), err)
		case !tc.ok && !errors.Is(err, ErrUploadDataLost):
			t.Errorf("%s: uploadParts = %v, want ErrUploadDataLost", tc.name, err)
		}
	}
}
