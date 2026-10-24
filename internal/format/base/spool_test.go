// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"testing/iotest"
)

func spoolPayload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i>>8)
	}

	return b
}

func TestSpoolLimitedRoundTrip(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	for _, tc := range []struct {
		name    string
		size    int
		onDisk  bool
		wantErr error
	}{
		{name: "empty", size: 0},
		{name: "small", size: 100},
		{name: "exactly the memory limit", size: spoolMemoryLimit},
		{name: "one past the memory limit", size: spoolMemoryLimit + 1, onDisk: true},
		{name: "several buffers", size: spoolMemoryLimit + 3<<16 + 17, onDisk: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := spoolPayload(tc.size)
			md5Sum := md5.New()

			s, err := SpoolLimited(bytes.NewReader(payload), int64(tc.size), md5Sum)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()

			if s.Size() != int64(tc.size) {
				t.Errorf("Size = %d, want %d", s.Size(), tc.size)
			}

			if (s.file != nil) != tc.onDisk {
				t.Errorf("on disk = %v, want %v", s.file != nil, tc.onDisk)
			}

			want := sha256.Sum256(payload)
			if s.SHA256Hex() != hex.EncodeToString(want[:]) {
				t.Errorf("SHA256Hex = %s, want %x", s.SHA256Hex(), want)
			}

			wantMD5 := md5.Sum(payload)
			if !bytes.Equal(md5Sum.Sum(nil), wantMD5[:]) {
				t.Error("extra hash did not see the whole body")
			}

			got, err := io.ReadAll(s.Reader())
			if err != nil || !bytes.Equal(got, payload) {
				t.Errorf("Reader content mismatch (err %v, %d bytes)", err, len(got))
			}

			// Readers are independent and seekable.
			r1, r2 := s.Reader(), s.Reader()
			_, _ = r1.Seek(int64(tc.size/2), io.SeekStart)

			rest, _ := io.ReadAll(r1)
			full, _ := io.ReadAll(r2)

			if !bytes.Equal(rest, payload[tc.size/2:]) || !bytes.Equal(full, payload) {
				t.Error("readers share a position")
			}

			all, err := s.Bytes()
			if err != nil || !bytes.Equal(all, payload) {
				t.Errorf("Bytes mismatch (err %v)", err)
			}
		})
	}
}

func TestSpoolLimitedRejectsOversizedBody(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	for _, size := range []int{11, spoolMemoryLimit + 1, 2 * spoolMemoryLimit} {
		limit := int64(10)
		if size > spoolMemoryLimit {
			limit = int64(size - 1)
		}

		counting := &countingReader{r: bytes.NewReader(spoolPayload(size + 4096))}

		s, err := SpoolLimited(counting, limit)
		if !errors.Is(err, ErrContentTooLarge) {
			t.Fatalf("size %d: error = %v, want ErrContentTooLarge", size, err)
		}

		if s != nil {
			t.Fatalf("size %d: spool returned with an error", size)
		}

		if counting.n > limit+1 {
			t.Errorf("size %d: read %d bytes, must stop at limit+1 = %d", size, counting.n, limit+1)
		}

		assertNoSpoolFiles(t, dir)
	}
}

func TestSpoolLimitedTruncatedBody(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	for _, size := range []int{100, 2 * spoolMemoryLimit} {
		// The body ends early with the error net/http reports for a short body.
		body := io.MultiReader(bytes.NewReader(spoolPayload(size)), iotest.ErrReader(io.ErrUnexpectedEOF))

		s, err := SpoolLimited(body, int64(4*spoolMemoryLimit))
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("size %d: error = %v, want io.ErrUnexpectedEOF", size, err)
		}

		if s != nil {
			t.Fatalf("size %d: spool returned with an error", size)
		}

		assertNoSpoolFiles(t, dir)
	}
}

func TestSpoolLimitedRejectsNegativeLimit(t *testing.T) {
	if _, err := SpoolLimited(strings.NewReader(""), -1); !errors.Is(err, ErrNegativeContentLimit) {
		t.Fatalf("error = %v, want ErrNegativeContentLimit", err)
	}
}

func TestSpoolCloseRemovesTempFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	s, err := SpoolLimited(bytes.NewReader(spoolPayload(2*spoolMemoryLimit)), 4*spoolMemoryLimit)
	if err != nil {
		t.Fatal(err)
	}

	name := s.file.Name()
	if _, err := os.Stat(name); err != nil {
		t.Fatalf("spool file missing while open: %v", err)
	}

	s.Close()
	s.Close() // idempotent

	if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("spool file survives Close: %v", err)
	}

	var nilSpool *Spool

	nilSpool.Close()
}

func TestSpoolReadAtBounds(t *testing.T) {
	for _, size := range []int{50, 2 * spoolMemoryLimit} {
		payload := spoolPayload(size)

		s, err := SpoolLimited(bytes.NewReader(payload), int64(size))
		if err != nil {
			t.Fatal(err)
		}

		buf := make([]byte, 10)

		n, err := s.ReadAt(buf, int64(size-4))
		if n != 4 || !errors.Is(err, io.EOF) || !bytes.Equal(buf[:n], payload[size-4:]) {
			t.Errorf("size %d: tail ReadAt = %d, %v", size, n, err)
		}

		if n, err := s.ReadAt(buf, int64(size)); n != 0 || !errors.Is(err, io.EOF) {
			t.Errorf("size %d: ReadAt at end = %d, %v", size, n, err)
		}

		s.Close()
	}
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)

	return n, err
}

func assertNoSpoolFiles(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 0 {
		t.Errorf("temp files left behind: %v", entries)
	}
}
