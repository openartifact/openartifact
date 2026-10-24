// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"math/rand/v2"
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/format/formattest"
)

// benchChart builds a chart whose payload file is incompressible, so the
// uploaded .tgz is about size bytes.
func benchChart(b *testing.B, size int) []byte {
	b.Helper()

	var buf bytes.Buffer

	gw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	tw := tar.NewWriter(gw)

	yaml := []byte(sampleChartYAML)
	_ = tw.WriteHeader(&tar.Header{Name: "mychart/Chart.yaml", Mode: 0o644, Size: int64(len(yaml))})
	_, _ = tw.Write(yaml)

	payload := make([]byte, size)
	rand.NewChaCha8([32]byte{2}).Read(payload)

	_ = tw.WriteHeader(&tar.Header{Name: "mychart/files/payload.bin", Mode: 0o644, Size: int64(len(payload))})
	_, _ = tw.Write(payload)
	_ = tw.Close()
	_ = gw.Close()

	return buf.Bytes()
}

// BenchmarkUploadChart POSTs one chart through the handler against in-memory
// fakes whose blob store drops the bytes.
func BenchmarkUploadChart(b *testing.B) {
	for _, size := range []int{64 << 10, 4 << 20, 32 << 20} {
		chart := benchChart(b, size)

		b.Run(fmt.Sprintf("size=%dKiB", size>>10), func(b *testing.B) {
			db := newHelmDB()
			h := NewHTTPHandler(formattest.NewLogger(b), db, formattest.NewDiscardBlobStore(), "http://localhost")

			b.ReportAllocs()
			b.SetBytes(int64(len(chart)))

			for b.Loop() {
				clear(db.charts)

				w := formattest.Do(h.RegisterRepoRoutes,
					formattest.Request(http.MethodPost, "/repos/myrepo/charts", bytes.NewReader(chart)))
				if w.Code != http.StatusCreated {
					b.Fatalf("status = %d: %s", w.Code, w.Body.String())
				}
			}
		})
	}
}
