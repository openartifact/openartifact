// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/publicurl"
	pkgcomposer "github.com/openartifact/openartifact/pkg/composer"
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

// serveVia serves path through publicurl.Middleware with explicit as BASE_URL.
func serveVia(h *Handler, f *fixture, be Backend, explicit, path string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	h.SetBaseURL(explicit)

	trusted := func(r *http.Request) bool { return r.RemoteAddr == "10.0.0.1:4000" }
	mw := publicurl.Middleware(explicit, "", trusted)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.Serve(w, r, f.grp, path, be)
	}))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:4000"
	mutate(req)

	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	return w
}

func TestNPMGroup_TarballOriginIsDerivedAndInTheETag(t *testing.T) {
	for _, tc := range originCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := npmGroup(t)
			f.set(
				"hosted",
				"lib",
				200,
				packument(t, "hosted", "lib", map[string]string{"1.0.0": "AAAA"}, map[string]string{"latest": "1.0.0"}),
			)

			w := serveVia(f.h, f, f.backend(), tc.explicit, "lib", tc.mutate)
			require.Equal(t, http.StatusOK, w.Code)

			got := decodeMerged(t, w.Body.Bytes())
			assert.Equal(t, tc.want+"/repos/all/lib/-/pkg-1.0.0.tgz", got.Versions["1.0.0"]["dist"].(map[string]any)["tarball"])
		})
	}

	f := npmGroup(t)
	f.set("hosted", "lib", 200, packument(t, "hosted", "lib", map[string]string{"1.0.0": "AAAA"}, map[string]string{"latest": "1.0.0"}))

	at := func(host string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
		return serveVia(f.h, f, f.backend(), "", "lib", func(r *http.Request) {
			r.Host = host
			for _, m := range mutate {
				m(r)
			}
		})
	}

	a, b := at("a.test"), at("b.test")
	require.NotEmpty(t, a.Header().Get("ETag"))
	assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
	assert.Equal(t, http.StatusOK, at("b.test", func(r *http.Request) { r.Header.Set("If-None-Match", a.Header().Get("ETag")) }).Code)
	assert.Equal(
		t,
		http.StatusNotModified,
		at("a.test", func(r *http.Request) { r.Header.Set("If-None-Match", a.Header().Get("ETag")) }).Code,
	)
}

func TestComposerGroup_RootIsPathAbsoluteAndDistOriginIsDerived(t *testing.T) {
	for _, tc := range originCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := composerGroup(t, true)
			f.set("hosted", "p2/acme/lib.json", 200, composerDoc(t, "hosted", [2]string{"1.0.0", "h100"}))

			root := serveVia(f.h, f, f.backend(), tc.explicit, "packages.json", tc.mutate)
			assert.Contains(t, root.Body.String(), `"metadata-url":"`+publicurl.PathPrefix(tc.explicit)+`/repos/all/p2/%package%.json"`)

			w := serveVia(f.h, f, f.backend(), tc.explicit, "p2/acme/lib.json", tc.mutate)
			require.Equal(t, http.StatusOK, w.Code)
			assert.Equal(
				t,
				tc.want+"/repos/all/"+pkgcomposer.DistPath("acme/lib", "1.0.0", "h100"),
				composerVersions(t, w.Body.Bytes())["1.0.0"],
			)
		})
	}

	f := composerGroup(t, true)
	f.set("hosted", "p2/acme/lib.json", 200, composerDoc(t, "hosted", [2]string{"1.0.0", "h100"}))

	host := func(h string) func(*http.Request) { return func(r *http.Request) { r.Host = h } }

	// The root names no origin: the same validator through every host.
	ra := serveVia(f.h, f, f.backend(), "", "packages.json", host("a.test"))
	rb := serveVia(f.h, f, f.backend(), "", "packages.json", host("b.test"))
	require.NotEmpty(t, ra.Header().Get("ETag"))
	assert.Equal(t, ra.Header().Get("ETag"), rb.Header().Get("ETag"))

	a := serveVia(f.h, f, f.backend(), "", "p2/acme/lib.json", host("a.test"))
	b := serveVia(f.h, f, f.backend(), "", "p2/acme/lib.json", host("b.test"))
	require.NotEmpty(t, a.Header().Get("ETag"))
	assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
}

func TestAnsibleGroup_LinksOriginIsDerivedAndInTheETag(t *testing.T) {
	setup := func() *ansibleFixture {
		a := newAnsibleFixture(t, true, ansibleMembers()...)
		a.add("hosted", &ansibleFake{collections: map[string][]string{"acme.lib": {"1.0.0"}}, pageSize: 5})

		return a
	}

	const versions = "api/v3/collections/acme/lib/versions/"

	for _, tc := range originCases() {
		t.Run(tc.name, func(t *testing.T) {
			a := setup()
			w := serveVia(a.f.h, a.f, a.backend(), tc.explicit, versions, tc.mutate)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), `"href":"`+tc.want+`/repos/all/api/v3/collections/acme/lib/versions/1.0.0/"`)

			v3 := serveVia(a.f.h, a.f, a.backend(), tc.explicit, "api/v3/", tc.mutate)
			assert.Contains(t, v3.Body.String(), `"artifacts":"`+tc.want+`/repos/all/api/v3/artifacts/collections/"`)
		})
	}

	a := setup()
	host := func(h string) func(*http.Request) { return func(r *http.Request) { r.Host = h } }

	ra := serveVia(a.f.h, a.f, a.backend(), "", "api/", host("a.test"))
	rb := serveVia(a.f.h, a.f, a.backend(), "", "api/", host("b.test"))
	require.NotEmpty(t, ra.Header().Get("ETag"))
	assert.Equal(t, ra.Header().Get("ETag"), rb.Header().Get("ETag"), "the root document names no origin")

	x := serveVia(a.f.h, a.f, a.backend(), "", versions, host("a.test"))
	y := serveVia(a.f.h, a.f, a.backend(), "", versions, host("b.test"))
	require.NotEmpty(t, x.Header().Get("ETag"))
	assert.NotEqual(t, x.Header().Get("ETag"), y.Header().Get("ETag"))
}
