// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// benchPackument builds a packument of n versions in the shape of a real one
// (about 1 KiB of manifest per version).
func benchPackument(member string, n int) string {
	var sb strings.Builder

	sb.WriteString(`{"name":"lib","description":"bench","dist-tags":{"latest":"1.0.0"},"time":{"modified":"m","created":"c"},"versions":{`)

	for i := range n {
		if i > 0 {
			sb.WriteByte(',')
		}

		v := fmt.Sprintf("%d.%d.%d", i/10000, (i/100)%100, i%100)
		fmt.Fprintf(&sb, `%q:{"name":"lib","version":%q,"description":"An example package used for benchmarking merges","main":"dist/index.js",`+
			`"dependencies":{"lodash":"^4.17.21","express":"^4.18.2","zod":"^3.22.0"},"devDependencies":{"typescript":"^5.3.0","jest":"^29.7.0"},`+
			`"dist":{"tarball":"https://%s.example/lib/-/lib-%s.tgz","integrity":"sha512-%088d","shasum":"%040d"}}`, v, v, member, v, i, i)
	}

	sb.WriteString(`}}`)

	return sb.String()
}

// BenchmarkNPMGroupPackument serves a group packument merged from a hosted
// member and a proxy member of n versions each.
func BenchmarkNPMGroupPackument(b *testing.B) {
	for _, n := range []int{100, 1000} {
		f := newFixture(b, "npm", member("hosted", "npm", "hosted"), member("mirror", "npm", "proxy"))
		f.h.SetBaseURL(npmBase)
		f.set("hosted", "lib", 200, benchPackument("hosted", n))
		f.set("mirror", "lib", 200, benchPackument("mirror", n))

		b.Run(fmt.Sprintf("versions=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			req := httptest.NewRequestWithContext(b.Context(), http.MethodGet, "/", http.NoBody)
			w := &discardWriter{header: http.Header{}}

			for b.Loop() {
				f.h.Serve(w, req, f.grp, "lib", f.backend())
			}

			if w.n < n*100 {
				b.Fatalf("wrote %d bytes", w.n)
			}
		})
	}
}

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
