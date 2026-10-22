// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeS3 accepts single and multipart uploads and discards the bytes.
func fakeS3() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && q.Has("uploads"):
			_, _ = w.Write([]byte(`<InitiateMultipartUploadResult><UploadId>u</UploadId></InitiateMultipartUploadResult>`))
		case r.Method == http.MethodPost && q.Has("uploadId"):
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte(`<CompleteMultipartUploadResult><ETag>"e"</ETag></CompleteMultipartUploadResult>`))
		default:
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("ETag", `"e"`)
		}
	}))
}

// BenchmarkS3PutPeakHeap uploads 3 x 200 MiB concurrently and reports the peak
// heap in use, which is dominated by the SDK's multipart part buffers.
func BenchmarkS3PutPeakHeap(b *testing.B) {
	srv := fakeS3()
	defer srv.Close()

	b.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	store, err := NewS3BlobStore(StorageConfig{
		Endpoint: srv.URL, Region: "us-east-1", Bucket: "b", AccessKey: "a", SecretKey: "s", ForcePathStyle: true,
	})
	if err != nil {
		b.Fatal(err)
	}

	const (
		size    = 200 << 20
		uploads = 3
	)

	for i := 0; i < b.N; i++ {
		runtime.GC()

		var peak atomic.Uint64

		stop := make(chan struct{})

		var sampler sync.WaitGroup

		sampler.Go(func() {
			var ms runtime.MemStats

			for {
				runtime.ReadMemStats(&ms)

				if ms.HeapInuse > peak.Load() {
					peak.Store(ms.HeapInuse)
				}

				select {
				case <-stop:
					return
				case <-time.After(5 * time.Millisecond):
				}
			}
		})

		var wg sync.WaitGroup

		for u := range uploads {
			wg.Go(func() {
				// Distinct content per upload so each has its own digest.
				h := sha256.New()
				_, _ = io.Copy(h, newPatternReader(size, byte(u)))

				if _, err := store.Put(context.Background(), newPatternReader(size, byte(u)), map[string]string{"sha256": hex.EncodeToString(h.Sum(nil))}); err != nil {
					b.Error(err)
				}
			})
		}

		wg.Wait()
		close(stop)
		sampler.Wait()

		b.ReportMetric(float64(peak.Load())/(1<<20), "peak-heap-MiB")
	}
}

// patternReader yields n deterministic bytes without holding them in memory.
type patternReader struct {
	left int
	seed byte
}

func newPatternReader(n int, seed byte) *patternReader { return &patternReader{left: n, seed: seed} }

func (p *patternReader) Read(b []byte) (int, error) {
	if p.left == 0 {
		return 0, io.EOF
	}

	n := min(len(b), p.left)
	for i := range b[:n] {
		b[i] = p.seed + byte(i)
	}

	p.left -= n

	return n, nil
}
