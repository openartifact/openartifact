// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

// BenchmarkS3PutSmall measures the client-side cost of one S3BlobStore.Put
// (spool + verify, existence check, PutObject) against a server that answers
// instantly, so the result is the overhead OpenArtifact adds on top of the
// object store's own latency. B/op shows how much of the body is copied or
// re-read on the way.
func BenchmarkS3PutSmall(b *testing.B) {
	srv := fakeS3()
	defer srv.Close()

	b.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	store, err := NewS3BlobStore(StorageConfig{
		Endpoint: srv.URL, Region: "us-east-1", Bucket: "b", AccessKey: "a", SecretKey: "s", ForcePathStyle: true,
	})
	if err != nil {
		b.Fatal(err)
	}

	for _, size := range []int{1 << 10, 256 << 10, 8 << 20} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			body := bytes.Repeat([]byte{0x5a}, size)
			sum := sha256.Sum256(body)
			meta := map[string]string{"sha256": hex.EncodeToString(sum[:])}

			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()

			for b.Loop() {
				if _, err := store.Put(context.Background(), bytes.NewReader(body), meta); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
