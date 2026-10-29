// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/publicurl"
)

// originCase is one way a request can name its origin. Only the peer 10.0.0.1
// is a trusted proxy; requests start from 203.0.113.9.
type originCase struct {
	name, explicit string
	mutate         func(*http.Request)
	want           string // the origin the document's absolute URLs carry
}

func originCases() []originCase {
	return []originCase{
		{
			"explicit BASE_URL wins",
			"https://oa.example/prefix",
			func(r *http.Request) { r.Host = "other.test" },
			"https://oa.example/prefix",
		},
		{"host only", "", func(r *http.Request) { r.Host = "oa.test:8080" }, "http://oa.test:8080"},
		{"forwarded from a trusted peer", "", func(r *http.Request) {
			r.RemoteAddr = "10.0.0.1:4000"
			r.Host = "internal:8080"
			r.Header.Set("X-Forwarded-Host", "registry.example")
			r.Header.Set("X-Forwarded-Proto", "https")
		}, "https://registry.example"},
		{"forwarded from an untrusted peer is ignored", "", func(r *http.Request) {
			r.Host = "real.test"
			r.Header.Set("X-Forwarded-Host", "evil.example")
			r.Header.Set("X-Forwarded-Proto", "https")
		}, "http://real.test"},
	}
}

// serveVia serves path of f's repository through publicurl.Middleware, the way
// the server mounts it, with explicit as BASE_URL.
func serveVia(f *fixture, explicit, path string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	f.svc.baseURL = strings.TrimRight(explicit, "/")

	trusted := func(r *http.Request) bool { return r.RemoteAddr == "10.0.0.1:4000" }
	h := publicurl.Middleware(explicit, "", trusted)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.svc.Serve(w, r, f.repo, path)
	}))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:4000"
	mutate(req)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	return w
}

func TestNPMProxy_TarballOriginIsDerivedAndInTheETag(t *testing.T) {
	for _, tc := range originCases() {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := npmFixture(t)

			w := serveVia(f, tc.explicit, "lib", tc.mutate)
			d := decodeDoc(t, w)

			assert.Equal(t, tc.want+"/repos/npmjs/lib/-/lib-1.0.0.tgz", d.Versions["1.0.0"].Dist.Tarball)

			if tc.explicit == "" {
				assert.Contains(t, w.Header().Get("Vary"), "X-Forwarded-Host")
			}
		})
	}

	f, _ := npmFixture(t)
	a := serveVia(f, "", "lib", func(r *http.Request) { r.Host = "a.test" })
	b := serveVia(f, "", "lib", func(r *http.Request) { r.Host = "b.test" })

	require.NotEmpty(t, a.Header().Get("ETag"))
	assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
	assert.Equal(t, a.Header().Get("ETag"), serveVia(f, "", "lib", func(r *http.Request) { r.Host = "a.test" }).Header().Get("ETag"))

	// The ETag of origin a never revalidates origin b's document.
	w := serveVia(f, "", "lib", func(r *http.Request) {
		r.Host = "b.test"
		r.Header.Set("If-None-Match", a.Header().Get("ETag"))
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestComposerProxy_RootIsPathAbsoluteAndDistOriginIsDerived(t *testing.T) {
	for _, tc := range originCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := composerFixture(t, newFakePackagist(t))

			root := serveVia(f, tc.explicit, "packages.json", tc.mutate)
			prefix := publicurl.PathPrefix(tc.explicit)
			assert.Contains(t, root.Body.String(), `"metadata-url":"`+prefix+`/repos/packagist/p2/%package%.json"`)

			w := serveVia(f, tc.explicit, "p2/acme/lib.json", tc.mutate)
			require.Equal(t, http.StatusOK, w.Code)

			for _, v := range composerServed(t, w.Body.Bytes()) {
				d, ok := v.ZipDist()
				require.True(t, ok)
				assert.True(t, strings.HasPrefix(d.URL, tc.want+"/repos/packagist/dists/acme/lib/"), d.URL)
			}
		})
	}

	f := composerFixture(t, newFakePackagist(t))

	// The root names no origin: the same validator through every host.
	ra := serveVia(f, "", "packages.json", func(r *http.Request) { r.Host = "a.test" })
	rb := serveVia(f, "", "packages.json", func(r *http.Request) { r.Host = "b.test" })
	assert.Equal(t, ra.Header().Get("ETag"), rb.Header().Get("ETag"))

	a := serveVia(f, "", "p2/acme/lib.json", func(r *http.Request) { r.Host = "a.test" })
	b := serveVia(f, "", "p2/acme/lib.json", func(r *http.Request) { r.Host = "b.test" })
	assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
}

func TestAnsibleProxy_LinksOriginIsDerivedAndInTheETag(t *testing.T) {
	for _, tc := range originCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := ansibleFixture(t, newFakeGalaxy())

			w := serveVia(f, tc.explicit, "api/v3/collections/ns/demo/versions/1.0.0/", tc.mutate)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			base := tc.want + "/repos/galaxy/api/v3/"
			assert.Contains(t, w.Body.String(), `"download_url":"`+base+`artifacts/collections/ns-demo-1.0.0.tar.gz"`)
			assert.Contains(t, w.Body.String(), `"href":"`+base+`collections/ns/demo/versions/1.0.0/"`)
		})
	}

	f := ansibleFixture(t, newFakeGalaxy())
	path := "api/v3/collections/ns/demo/versions/1.0.0/"
	a := serveVia(f, "", path, func(r *http.Request) { r.Host = "a.test" })
	b := serveVia(f, "", path, func(r *http.Request) { r.Host = "b.test" })
	require.NotEmpty(t, a.Header().Get("ETag"))
	assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))

	w := serveVia(f, "", path, func(r *http.Request) {
		r.Host = "b.test"
		r.Header.Set("If-None-Match", a.Header().Get("ETag"))
	})
	assert.Equal(t, http.StatusOK, w.Code)
}
