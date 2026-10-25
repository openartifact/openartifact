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
	"testing"
)

// hidden strips the RangeReader of the store it wraps.
type hidden struct{ BlobStore }

func TestOpenAt(t *testing.T) {
	t.Parallel()

	fs, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	content := []byte("0123456789")
	sum := sha256.Sum256(content)

	id, err := fs.Put(context.Background(), bytes.NewReader(content), map[string]string{"sha256": hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}

	for name, store := range map[string]BlobStore{"filesystem seek": fs, "skip fallback": hidden{fs}} {
		for _, tt := range []struct {
			offset  int64
			want    string
			wantErr error
		}{
			{0, "0123456789", nil},
			{4, "456789", nil},
			{9, "9", nil},
			{10, "", nil},
			{11, "", ErrOffsetOutOfRange},
			{-1, "", ErrOffsetOutOfRange},
		} {
			rc, err := OpenAt(context.Background(), store, id, tt.offset)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("%s: OpenAt(%d) error = %v, want %v", name, tt.offset, err, tt.wantErr)
				}

				continue
			}

			if err != nil {
				t.Fatalf("%s: OpenAt(%d): %v", name, tt.offset, err)
			}

			got, _ := io.ReadAll(rc)
			_ = rc.Close()

			if string(got) != tt.want {
				t.Errorf("%s: OpenAt(%d) = %q, want %q", name, tt.offset, got, tt.want)
			}
		}

		if _, err := OpenAt(context.Background(), store, hex.EncodeToString(make([]byte, 32)), 3); !errors.Is(err, ErrBlobNotFound) {
			t.Errorf("%s: missing blob error = %v, want ErrBlobNotFound", name, err)
		}
	}
}
