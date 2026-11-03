// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The seeker serves any byte range of a blob, in any order, with or without
// the store's ranged reads, and http.ServeContent can serve Range requests
// from it.
func TestBlobSeeker(t *testing.T) {
	t.Parallel()

	fs, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	content := bytes.Repeat([]byte("0123456789"), 100)
	sum := sha256.Sum256(content)

	id, err := fs.Put(context.Background(), bytes.NewReader(content), map[string]string{"sha256": hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}

	for name, store := range map[string]BlobStore{"filesystem seek": fs, "skip fallback": hidden{fs}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := NewBlobSeeker(context.Background(), store, id, int64(len(content)))
			defer func() { _ = s.Close() }()

			if end, err := s.Seek(0, io.SeekEnd); err != nil || end != int64(len(content)) {
				t.Fatalf("Seek end = %d, %v", end, err)
			}

			for _, r := range []struct{ off, n int }{{0, 10}, {500, 20}, {3, 5}, {990, 10}, {0, 1000}} {
				if _, err := s.Seek(int64(r.off), io.SeekStart); err != nil {
					t.Fatal(err)
				}

				got := make([]byte, r.n)
				if _, err := io.ReadFull(s, got); err != nil {
					t.Fatalf("read %d at %d: %v", r.n, r.off, err)
				}

				if !bytes.Equal(got, content[r.off:r.off+r.n]) {
					t.Errorf("read %d at %d = %q", r.n, r.off, got)
				}
			}

			if n, err := s.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
				t.Errorf("read at the end = %d, %v; want EOF", n, err)
			}

			if _, err := s.Seek(-1, io.SeekStart); err == nil {
				t.Error("a negative position is accepted")
			}

			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
			req.Header.Set("Range", "bytes=15-24")
			http.ServeContent(rec, req, "", time.Time{}, NewBlobSeeker(context.Background(), store, id, int64(len(content))))

			if rec.Code != http.StatusPartialContent || rec.Body.String() != string(content[15:25]) {
				t.Errorf("ServeContent range = %d %q", rec.Code, rec.Body.String())
			}
		})
	}

	missing := NewBlobSeeker(context.Background(), fs, "0000000000000000000000000000000000000000000000000000000000000000", 10)
	if err := missing.Open(); !errors.Is(err, ErrBlobNotFound) {
		t.Errorf("Open of a missing blob = %v, want ErrBlobNotFound", err)
	}
}
