// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package iocopy

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"testing/iotest"
)

func TestCopyMatchesIOCopy(t *testing.T) {
	payload := payloadOf(5*bufSize + 123)

	var got bytes.Buffer

	// readFromWriter has a ReaderFrom that must not be used.
	n, err := Copy(readFromWriter{&got}, io.LimitReader(bytes.NewReader(payload), int64(len(payload))))
	if err != nil || n != int64(len(payload)) || !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("Copy = %d, %v", n, err)
	}

	f, err := os.CreateTemp(t.TempDir(), "src")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	_, _ = f.Write(payload)
	_, _ = f.Seek(0, io.SeekStart)

	got.Reset()

	if n, err := Copy(&got, f); err != nil || n != int64(len(payload)) || !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("Copy(file) = %d, %v", n, err)
	}
}

func TestCopyPropagatesErrors(t *testing.T) {
	boom := errors.New("boom")

	if _, err := Copy(io.Discard, iotest.ErrReader(boom)); !errors.Is(err, boom) {
		t.Fatalf("read error = %v, want %v", err, boom)
	}

	if _, err := Copy(failingWriter{boom}, strings.NewReader("data")); !errors.Is(err, boom) {
		t.Fatalf("write error = %v, want %v", err, boom)
	}
}

type readFromWriter struct{ w io.Writer }

func (r readFromWriter) Write(p []byte) (int, error) { return r.w.Write(p) }

func (readFromWriter) ReadFrom(io.Reader) (int64, error) {
	panic("pooled Copy must not use ReaderFrom")
}

type failingWriter struct{ err error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.err }

func payloadOf(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i>>8)
	}

	return b
}

// plainWriter has neither ReadFrom nor WriteTo, like a response writer behind
// middleware; plainReader hides WriteTo like a storage client's body.
type plainWriter struct{}

func (plainWriter) Write(p []byte) (int, error) { return len(p), nil }

// BenchmarkCopy compares io.Copy, which allocates a 32 KiB buffer per call, with
// the pooled Copy for a blob download-sized body.
func BenchmarkCopy(b *testing.B) {
	for _, tc := range []struct {
		name string
		size int
	}{{"16KiB", 16 << 10}, {"1MiB", 1 << 20}} {
		payload := payloadOf(tc.size)

		for _, impl := range []struct {
			name string
			copy func(io.Writer, io.Reader) (int64, error)
		}{{"io.Copy", io.Copy}, {"iocopy.Copy", Copy}} {
			b.Run(tc.name+"/"+impl.name, func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(tc.size))

				for b.Loop() {
					if _, err := impl.copy(plainWriter{}, struct{ io.Reader }{bytes.NewReader(payload)}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
