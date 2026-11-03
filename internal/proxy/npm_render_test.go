// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"

	"github.com/openartifact/openartifact/internal/curation"
	pkgnpm "github.com/openartifact/openartifact/pkg/npm"
)

// listed is the sorted versions of a packument response.
func listed(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	return sortedVersions(decodeGated(t, w.Body.String()))
}

// A rendering is served from the render cache until one of the things it was
// built from changes. Each step asks with the validator of the previous answer,
// which must not be accepted once the document differs.
func TestNPMProxy_CachedRenderingFollowsEveryInput(t *testing.T) {
	f, reg := minAgeFixture(t, 0)
	src := f.useCuration(curation.ModeOff)

	all := []string{"0.9.0", "1.0.0", "1.1.0", "2.0.0", "2.1.0-rc.1"}

	prev := f.do(http.MethodGet, "lib")
	if got := listed(t, prev); !slices.Equal(got, all) {
		t.Fatalf("versions = %v, want %v", got, all)
	}

	// step asks for the packument with the previous answer's validator and checks the versions.
	step := func(name string, want []string) {
		t.Helper()

		etag := prev.Header().Get("ETag")
		w := f.doWith("lib", map[string]string{"If-None-Match": etag})

		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d, want a new document (previous validator %s)", name, w.Code, etag)
		}

		if got := listed(t, w); !slices.Equal(got, want) {
			t.Errorf("%s: versions = %v, want %v", name, got, want)
		}

		if w.Header().Get("ETag") == etag {
			t.Errorf("%s: validator did not change", name)
		}

		// The same request again is the same answer, and is revalidated by it.
		again := f.doWith("lib", nil)
		if !bytes.Equal(again.Body.Bytes(), w.Body.Bytes()) || again.Header().Get("ETag") != w.Header().Get("ETag") {
			t.Errorf("%s: repeat request differs", name)
		}

		if rev := f.doWith("lib", map[string]string{"If-None-Match": w.Header().Get("ETag")}); rev.Code != http.StatusNotModified {
			t.Errorf("%s: revalidation = %d, want 304", name, rev.Code)
		}

		prev = w
	}

	src.set(blockRule("lib", "2.0.0"))
	f.repo.CurationMode = curation.ModeDenylist
	step("curation rule added", []string{"0.9.0", "1.0.0", "1.1.0", "2.1.0-rc.1"})

	src.set(blockRule("lib", "1.1.0"))
	step("curation rule changed", []string{"0.9.0", "1.0.0", "2.0.0", "2.1.0-rc.1"})

	f.repo.CurationMode = curation.ModeOff
	step("curation mode off", all)

	f.repo.MinAgeHours = npmMinAgeGate
	step("minimum age set", []string{"0.9.0", "1.0.0", "1.1.0"})

	f.repo.MinAgeHours = 0
	step("minimum age cleared", all)

	// The upstream publishes a different set and the cached copy expires.
	reg.dropUndated = true

	f.db.expire(f.repo.ID, "lib")
	step("upstream refreshed", []string{"1.0.0", "1.1.0", "2.0.0", "2.1.0-rc.1"})

	// The origin the tarball URLs point at is part of the document.
	f.svc.SetBaseURL("http://other.test")

	w := f.doWith("lib", map[string]string{"If-None-Match": prev.Header().Get("ETag")})
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("http://other.test/repos/npmjs/lib/-/lib-1.0.0.tgz")) {
		t.Errorf("origin changed: status %d: %s", w.Code, w.Body.String())
	}
}

// The full and the abbreviated packument, and a version manifest cut from the
// same upstream copy, are separate cached documents with their own types.
func TestNPMProxy_CachedRenderingsOfOneBlobDoNotMix(t *testing.T) {
	f, _ := npmFixture(t)

	abbr := map[string]string{"Accept": pkgnpm.AbbreviatedContentType}

	for range 2 {
		full := f.doWith("lib", nil)
		short := f.doWith("lib", abbr)
		manifest := f.doWith("lib/1.0.0", nil)

		if full.Header().Get("Content-Type") != "application/json" {
			t.Errorf("full packument type = %q", full.Header().Get("Content-Type"))
		}

		if bytes.Contains(manifest.Body.Bytes(), []byte(`"versions"`)) || !bytes.Contains(manifest.Body.Bytes(), []byte(`"1.0.0"`)) {
			t.Errorf("version manifest is not the manifest: %s", manifest.Body.String())
		}

		if !bytes.Contains(full.Body.Bytes(), []byte(`"versions"`)) || !bytes.Contains(short.Body.Bytes(), []byte(`"versions"`)) {
			t.Errorf("packuments lost their versions: %s / %s", full.Body.String(), short.Body.String())
		}

		if full.Header().Get("ETag") == manifest.Header().Get("ETag") {
			t.Error("a packument and its version manifest share a validator")
		}
	}
}

// Whatever the client's encoding, a large rendering decodes to the same bytes,
// HEAD reports the length of the identity document, and a client holding the
// validator gets a 304.
func TestNPMProxy_LargeRenderingServesEveryEncoding(t *testing.T) {
	doc := benchPackument("https://registry.example", 400, 3600) // over a megabyte

	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, doc)
	})
	f.repo.Format = "npm"
	f.repo.Name = "npmjs"
	f.repo.UpstreamURL = f.upstream.URL
	f.svc.SetBaseURL(oaBase)

	if len(doc) < 1<<20 {
		t.Fatalf("fixture is %d bytes, want over a megabyte", len(doc))
	}

	// Twice: the first request builds the rendering, the second is served from the cache.
	var plain []byte

	for i := range 2 {
		identity := f.doWith("lib", nil)
		if identity.Code != http.StatusOK || identity.Header().Get("Content-Encoding") != "" {
			t.Fatalf("identity request %d: status %d, encoding %q", i, identity.Code, identity.Header().Get("Content-Encoding"))
		}

		if got := identity.Header().Get("Content-Length"); got != strconv.Itoa(identity.Body.Len()) {
			t.Errorf("Content-Length %s, body %d bytes", got, identity.Body.Len())
		}

		if plain != nil && !bytes.Equal(plain, identity.Body.Bytes()) {
			t.Error("the cached rendering differs from the first one")
		}

		plain = identity.Body.Bytes()

		zipped := f.doWith("lib", map[string]string{"Accept-Encoding": "gzip"})
		if zipped.Header().Get("Content-Encoding") != "gzip" || zipped.Body.Len() >= len(plain)/2 {
			t.Fatalf(
				"gzip request %d: encoding %q, %d bytes for %d",
				i,
				zipped.Header().Get("Content-Encoding"),
				zipped.Body.Len(),
				len(plain),
			)
		}

		zr, err := gzip.NewReader(zipped.Body)
		if err != nil {
			t.Fatal(err)
		}

		if got, _ := io.ReadAll(zr); !bytes.Equal(got, plain) {
			t.Errorf("request %d: the gzip form does not decode to the identity document", i)
		}

		if !bytes.Contains(plain, []byte(oaBase+"/repos/npmjs/lib/-/lib-0.0.0.tgz")) {
			t.Errorf("request %d: tarball URLs were not rewritten", i)
		}
	}

	head := httptest.NewRecorder()
	f.svc.Serve(head, httptest.NewRequestWithContext(t.Context(), http.MethodHead, "/", http.NoBody), f.repo, "lib")

	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(len(plain)) {
		t.Errorf(
			"HEAD = %d, %d body bytes, Content-Length %q, want %d",
			head.Code,
			head.Body.Len(),
			head.Header().Get("Content-Length"),
			len(plain),
		)
	}

	etag := f.doWith("lib", nil).Header().Get("ETag")
	if rec := f.doWith("lib", map[string]string{"If-None-Match": etag}); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("revalidation = %d with %d body bytes", rec.Code, rec.Body.Len())
	}
}

// A repeat request is answered from the rendering: parsing and rewriting a
// packument allocates by the thousand, serving the cached one does not.
func TestNPMProxy_RepeatRequestDoesNotRender(t *testing.T) {
	doc := benchPackument("https://registry.example", 200, 2200)

	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, doc)
	})
	f.repo.Format = "npm"
	f.repo.Name = "npmjs"
	f.repo.UpstreamURL = f.upstream.URL
	f.svc.SetBaseURL(oaBase)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.Header.Set("Accept-Encoding", "gzip")

	serve := func() { f.svc.Serve(&discardWriter{header: http.Header{}}, req, f.repo, "lib") }

	serve() // builds the rendering

	if allocs := testing.AllocsPerRun(20, serve); allocs > 500 {
		t.Errorf("a repeat request allocates %.0f times, want a cache hit (rendering costs thousands)", allocs)
	}
}
