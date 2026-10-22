// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
)

// BenchmarkFilesystemPutDuplicate measures re-storing a blob that is already
// present, the common case for shared layers and re-uploads.
func BenchmarkFilesystemPutDuplicate(b *testing.B) {
	fs, err := NewFilesystemStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}

	const size = 64 << 20

	h := sha256.New()
	_, _ = io.Copy(h, newPatternReader(size, 1))
	meta := func() map[string]string { return map[string]string{"sha256": hex.EncodeToString(h.Sum(nil))} }

	if _, err := fs.Put(context.Background(), newPatternReader(size, 1), meta()); err != nil {
		b.Fatal(err)
	}

	b.SetBytes(size)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := fs.Put(context.Background(), newPatternReader(size, 1), meta()); err != nil {
			b.Fatal(err)
		}
	}
}
