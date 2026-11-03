// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// benchPackument builds an upstream packument of n versions of perVersion
// bytes of manifest each, in the shape of a real registry document.
func benchPackument(host string, n, perVersion int) string {
	var sb strings.Builder

	sb.WriteString(`{"_id":"lib","name":"lib","description":"bench","dist-tags":{"latest":"1.0.0"},"versions":{`)

	pad := strings.Repeat("x", max(perVersion-900, 0))

	for i := range n {
		if i > 0 {
			sb.WriteByte(',')
		}

		v := fmt.Sprintf("%d.%d.%d", i/10000, (i/100)%100, i%100)
		fmt.Fprintf(
			&sb,
			`%q:{"name":"lib","version":%q,"description":"An example package used for benchmarking","main":"dist/index.js",`+
				`"dependencies":{"lodash":"^4.17.21","express":"^4.18.2","zod":"^3.22.0"},"devDependencies":{"typescript":"^5.3.0"},`+
				`"readme":%q,"_npmUser":{"name":"u","email":"u@example.test"},`+
				`"dist":{"tarball":%q,"integrity":"sha512-%088d","shasum":"%040d"}}`,
			v, v, pad, host+"/lib/-/lib-"+v+".tgz", i, i,
		)
	}

	sb.WriteString(`},"time":{"modified":"2024-01-01T00:00:00.000Z","created":"2020-01-01T00:00:00.000Z"}}`)

	return sb.String()
}

// BenchmarkNPMProxyPackument serves a cached upstream packument: the hit path
// of a proxy npm repository, for a lodash-sized (about 250 KB) and a react-sized
// (about 7 MB) document, with a client that accepts gzip, one that does not,
// and one that revalidates.
func BenchmarkNPMProxyPackument(b *testing.B) {
	for _, tc := range []struct {
		name       string
		n, perVers int
	}{
		{"lodash-250KB", 115, 2200},
		{"react-7MB", 2000, 3600},
	} {
		doc := benchPackument("https://registry.example", tc.n, tc.perVers)

		f := newFixture(b, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, doc)
		})
		f.repo.Format = "npm"
		f.repo.Name = "npmjs"
		f.repo.UpstreamURL = f.upstream.URL
		f.svc.SetBaseURL(oaBase)

		first := f.doWith("lib", nil)
		if first.Code != http.StatusOK {
			b.Fatalf("warm-up: status %d: %s", first.Code, first.Body.String())
		}

		etag := first.Header().Get("ETag")

		for _, v := range []struct {
			name   string
			header map[string]string
			want   int
		}{
			{"gzip", map[string]string{"Accept-Encoding": "gzip"}, http.StatusOK},
			{"identity", nil, http.StatusOK},
			{"304", map[string]string{"If-None-Match": etag}, http.StatusNotModified},
		} {
			b.Run(tc.name+"/"+v.name, func(b *testing.B) {
				b.ReportAllocs()

				req := httptest.NewRequestWithContext(b.Context(), http.MethodGet, "/", http.NoBody)
				for k, val := range v.header {
					req.Header.Set(k, val)
				}

				for b.Loop() {
					w := &discardWriter{header: http.Header{}}
					f.svc.Serve(w, req, f.repo, "lib")

					if w.code != v.want {
						b.Fatalf("status %d, want %d", w.code, v.want)
					}
				}
			})
		}
	}
}

// discardWriter counts what a handler writes.
type discardWriter struct {
	header http.Header
	code   int
	n      int
}

func (w *discardWriter) Header() http.Header { return w.header }
func (w *discardWriter) WriteHeader(code int) { w.code = code }
func (w *discardWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}

	w.n += len(p)

	return len(p), nil
}
