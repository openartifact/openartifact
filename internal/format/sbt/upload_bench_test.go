// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbt

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/format/formattest"
)

// BenchmarkUpload PUTs one artifact of the given size through the handler
// (read, hash, license check, quota, store, row) against in-memory fakes whose
// blob store drops the bytes, so allocations are the handler's own.
func BenchmarkUpload(b *testing.B) {
	for _, size := range []int{64 << 10, 4 << 20, 32 << 20} {
		body := make([]byte, size)
		rand.NewChaCha8([32]byte{1}).Read(body)

		b.Run(fmt.Sprintf("size=%dKiB", size>>10), func(b *testing.B) {
			db := &sbtDB{DB: formattest.NewDB()}
			h := NewHTTPHandler(formattest.NewLogger(b), db, formattest.NewDiscardBlobStore(), "http://localhost")

			b.ReportAllocs()
			b.SetBytes(int64(size))

			for b.Loop() {
				db.rows = nil

				w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, artifactURL, bytes.NewReader(body)))
				if w.Code != http.StatusCreated {
					b.Fatalf("status = %d: %s", w.Code, w.Body.String())
				}
			}
		})
	}
}
