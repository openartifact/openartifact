// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type discardWriter struct {
	header http.Header
	n      int
}

func (w *discardWriter) Header() http.Header { return w.header }
func (w *discardWriter) WriteHeader(int)     {}
func (w *discardWriter) Write(p []byte) (int, error) {
	w.n += len(p)

	return len(p), nil
}

// BenchmarkMetadataChecksum polls maven-metadata.xml.sha1 and .md5 of a
// 200 KB metadata blob (a project with thousands of versions).
func BenchmarkMetadataChecksum(b *testing.B) {
	xml := []byte("<metadata>" + strings.Repeat("<version>1.0.0-SNAPSHOT</version>", 6000) + "</metadata>")
	router, _, _ := newMetadataRouter(b, xml, strings.Repeat("ab", 32))

	for _, suffix := range []string{".sha1", ".md5"} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, metadataPath+suffix, http.NoBody)

		b.Run(suffix, func(b *testing.B) {
			b.ReportAllocs()

			w := &discardWriter{header: http.Header{}}

			for b.Loop() {
				router.ServeHTTP(w, req)
			}

			if w.n == 0 {
				b.Fatal("no body written")
			}
		})
	}
}
