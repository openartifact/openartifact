// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/httpcache"
)

// validatedMembers fakes members that answer like the real ones: a request
// with If-None-Match revalidates against the member's current ETag before the
// document is produced. reads counts the documents actually delivered.
type validatedMembers struct {
	prefix string // makes the validators unique to one test: the render cache is process-wide

	mu    sync.Mutex
	docs  map[string]string // member name -> document; absent: the member answers 404
	rev   map[string]int
	fail  map[string]bool // member answers 503 when asked for the document itself
	reads atomic.Int32
}

var memberSeq atomic.Int32 //nolint:gochecknoglobals // unique validator prefixes across tests

func newValidatedMembers() *validatedMembers {
	return &validatedMembers{
		prefix: "t" + strconv.Itoa(int(memberSeq.Add(1))) + "-",
		docs:   map[string]string{},
		rev:    map[string]int{},
		fail:   map[string]bool{},
	}
}

// set publishes doc as member's document; "" takes it away.
func (v *validatedMembers) set(name, doc string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.rev[name]++

	if doc == "" {
		delete(v.docs, name)
		return
	}

	v.docs[name] = doc
}

func (v *validatedMembers) setFailing(name string, failing bool) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.fail[name] = failing
}

func (v *validatedMembers) backend(denied ...string) Backend {
	return Backend{
		Authorize: func(_ *http.Request, m *models.Repository, _ string) bool {
			for _, d := range denied {
				if d == m.Name {
					return false
				}
			}

			return true
		},
		ServeMember: func(w http.ResponseWriter, r *http.Request, m *models.Repository, _ string) {
			v.mu.Lock()
			doc, ok := v.docs[m.Name]
			etag := `"` + v.prefix + m.Name + "-" + strconv.Itoa(v.rev[m.Name]) + `"`
			failing := v.fail[m.Name]
			v.mu.Unlock()

			if !ok {
				http.NotFound(w, r)
				return
			}

			w.Header().Set("ETag", etag)

			if httpcache.ETagMatches(r.Header.Get("If-None-Match"), etag) {
				w.WriteHeader(http.StatusNotModified)
				return
			}

			if failing {
				http.Error(w, "down", http.StatusServiceUnavailable)
				return
			}

			v.reads.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, doc)
		},
	}
}

func (f *fixture) serveWith(v *validatedMembers, path string, header map[string]string, denied ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	for k, val := range header {
		req.Header.Set(k, val)
	}

	w := httptest.NewRecorder()
	f.h.Serve(w, req, f.grp, path, v.backend(denied...))

	return w
}

// A client that holds the validator of the merge revalidates without any
// member delivering or anything being merged, and a repeat of a merge already
// seen is served without asking a member for its document either.
func TestNPMGroup_ValidatorFromMembersNeedsNoDocuments(t *testing.T) {
	f := npmGroup(t)
	v := newValidatedMembers()
	v.set("hosted", packument(t, "hosted", "lib", map[string]string{"1.0.0": "AAAA"}, map[string]string{"latest": "1.0.0"}))
	v.set("mirror", packument(t, "mirror", "lib", map[string]string{"2.0.0": "BBB2"}, map[string]string{"latest": "2.0.0"}))

	first := f.serveWith(v, "lib", nil)
	if first.Code != http.StatusOK || first.Header().Get("ETag") == "" {
		t.Fatalf("first = %d, etag %q: %s", first.Code, first.Header().Get("ETag"), first.Body.String())
	}

	if got := v.reads.Load(); got != 2 {
		t.Fatalf("documents read for the first merge = %d, want 2", got)
	}

	etag := first.Header().Get("ETag")

	if rec := f.serveWith(v, "lib", map[string]string{"If-None-Match": etag}); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("revalidation = %d with %d body bytes, want an empty 304", rec.Code, rec.Body.Len())
	}

	again := f.serveWith(v, "lib", nil)
	if again.Code != http.StatusOK || !bytes.Equal(again.Body.Bytes(), first.Body.Bytes()) || again.Header().Get("ETag") != etag {
		t.Fatalf("repeat = %d, etag %q, body differs: %v", again.Code, again.Header().Get("ETag"), !bytes.Equal(again.Body.Bytes(), first.Body.Bytes()))
	}

	if got := v.reads.Load(); got != 2 {
		t.Errorf("documents read after a 304 and a cache hit = %d, want still 2", got)
	}
}

// Whatever changes a member's document changes the merge: a member that
// publishes, one that gains the package, one that loses it.
func TestNPMGroup_MemberChangeInvalidatesTheMerge(t *testing.T) {
	f := npmGroup(t)
	v := newValidatedMembers()
	v.set("hosted", packument(t, "hosted", "lib", map[string]string{"1.0.0": "AAAA"}, map[string]string{"latest": "1.0.0"}))

	first := f.serveWith(v, "lib", nil)
	etag := first.Header().Get("ETag")

	if strings.Contains(first.Body.String(), `"2.0.0"`) {
		t.Fatalf("unexpected version in %s", first.Body.String())
	}

	// The proxy member gains the package.
	v.set("mirror", packument(t, "mirror", "lib", map[string]string{"2.0.0": "BBB2"}, map[string]string{"next": "2.0.0"}))

	after := f.serveWith(v, "lib", map[string]string{"If-None-Match": etag})
	if after.Code != http.StatusOK || after.Header().Get("ETag") == etag || !strings.Contains(after.Body.String(), `"2.0.0"`) {
		t.Fatalf("after the mirror gained the package: %d, etag %q: %s", after.Code, after.Header().Get("ETag"), after.Body.String())
	}

	// The hosted member publishes.
	etag = after.Header().Get("ETag")
	v.set("hosted", packument(t, "hosted", "lib", map[string]string{"1.0.0": "AAAA", "1.1.0": "AAA1"}, map[string]string{"latest": "1.1.0"}))

	published := f.serveWith(v, "lib", map[string]string{"If-None-Match": etag})
	if published.Code != http.StatusOK || !strings.Contains(published.Body.String(), `"1.1.0"`) {
		t.Fatalf("after the hosted publish: %d: %s", published.Code, published.Body.String())
	}

	// The package is unpublished from the proxy member again.
	etag = published.Header().Get("ETag")
	v.set("mirror", "")

	gone := f.serveWith(v, "lib", map[string]string{"If-None-Match": etag})
	if gone.Code != http.StatusOK || strings.Contains(gone.Body.String(), `"2.0.0"`) {
		t.Fatalf("after the mirror lost the package: %d: %s", gone.Code, gone.Body.String())
	}
}

// A merge made while a member failed lacks that member's versions: it is never
// kept under the validator of the complete merge, so the next request after the
// member recovers gets the complete document.
func TestNPMGroup_PartialMergeIsNotCachedUnderTheMembersValidators(t *testing.T) {
	f := npmGroup(t)
	v := newValidatedMembers()
	v.set("hosted", packument(t, "hosted", "lib", map[string]string{"1.0.0": "AAAA"}, map[string]string{"latest": "1.0.0"}))
	v.set("mirror", packument(t, "mirror", "lib", map[string]string{"2.0.0": "BBB2"}, map[string]string{"next": "2.0.0"}))

	v.setFailing("mirror", true)

	partial := f.serveWith(v, "lib", nil)
	if partial.Code != http.StatusOK || strings.Contains(partial.Body.String(), `"2.0.0"`) {
		t.Fatalf("partial merge = %d: %s", partial.Code, partial.Body.String())
	}

	if partial.Header().Get("ETag") != "" || partial.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("partial merge carries validators: etag %q, cache-control %q", partial.Header().Get("ETag"), partial.Header().Get("Cache-Control"))
	}

	v.setFailing("mirror", false)

	complete := f.serveWith(v, "lib", nil)
	if !strings.Contains(complete.Body.String(), `"2.0.0"`) || complete.Header().Get("ETag") == "" {
		t.Fatalf("after recovery: etag %q: %s", complete.Header().Get("ETag"), complete.Body.String())
	}
}

// Merges are cached per set of readable members and per variant: a caller who
// cannot read a member never gets what a caller who can has made the cache hold,
// nor the other variant's document.
func TestNPMGroup_CacheSeparatesMemberSetsAndVariants(t *testing.T) {
	f := npmGroup(t)
	v := newValidatedMembers()
	v.set("hosted", packument(t, "hosted", "lib", map[string]string{"1.0.0": "AAAA"}, map[string]string{"latest": "1.0.0"}))
	v.set("mirror", packument(t, "mirror", "lib", map[string]string{"2.0.0": "BBB2"}, map[string]string{"next": "2.0.0"}))

	all := f.serveWith(v, "lib", nil)
	if !strings.Contains(all.Body.String(), `"2.0.0"`) {
		t.Fatalf("both members readable: %s", all.Body.String())
	}

	narrow := f.serveWith(v, "lib", nil, "mirror")
	if strings.Contains(narrow.Body.String(), `"2.0.0"`) || narrow.Header().Get("ETag") == all.Header().Get("ETag") {
		t.Errorf("a caller who cannot read the mirror got etag %q: %s", narrow.Header().Get("ETag"), narrow.Body.String())
	}

	// The same document asked for as the install document is another entry.
	abbreviated := f.serveWith(v, "lib", map[string]string{"Accept": "application/vnd.npm.install-v1+json"})
	if abbreviated.Header().Get("ETag") == all.Header().Get("ETag") {
		t.Errorf("abbreviated and full requests share the validator %q", all.Header().Get("ETag"))
	}

	// A version manifest is cut from the merge and cached on its own.
	manifest := f.serveWith(v, "lib/2.0.0", nil)
	if manifest.Code != http.StatusOK || strings.Contains(manifest.Body.String(), `"versions"`) || manifest.Header().Get("ETag") == all.Header().Get("ETag") {
		t.Errorf("version manifest = %d, etag %q: %s", manifest.Code, manifest.Header().Get("ETag"), manifest.Body.String())
	}

	if missing := f.serveWith(v, "lib/9.9.9", nil); missing.Code != http.StatusNotFound || missing.Header().Get("ETag") != "" {
		t.Errorf("missing version = %d, etag %q, want 404 without validator", missing.Code, missing.Header().Get("ETag"))
	}
}

func TestNPMGroup_ValidatedMergeGzipAndHead(t *testing.T) {
	f := npmGroup(t)
	v := newValidatedMembers()

	versions := map[string]string{}
	for i := range 40 {
		versions["1.0."+strconv.Itoa(i)] = "M" + strconv.Itoa(i)
	}

	v.set("hosted", packument(t, "hosted", "lib", versions, map[string]string{"latest": "1.0.39"}))

	plain := f.serveWith(v, "lib", nil)

	zipped := f.serveWith(v, "lib", map[string]string{"Accept-Encoding": "gzip"})
	if zipped.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(zipped.Header().Get("ETag"), "W/") {
		t.Fatalf("gzip request: encoding %q, etag %q", zipped.Header().Get("Content-Encoding"), zipped.Header().Get("ETag"))
	}

	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}

	if got, _ := io.ReadAll(zr); !bytes.Equal(got, plain.Body.Bytes()) {
		t.Error("the compressed merge differs from the plain one")
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodHead, "/", http.NoBody)
	head := httptest.NewRecorder()
	f.h.Serve(head, req, f.grp, "lib", v.backend())

	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(plain.Body.Len()) {
		t.Errorf("HEAD = %d with %d body bytes, Content-Length %q", head.Code, head.Body.Len(), head.Header().Get("Content-Length"))
	}
}

// BenchmarkNPMGroupValidated serves a group of two members of n versions each
// that answer with validators: the repeat request of a client that holds the
// merge (304), and the first-time request of one that does not (200).
func BenchmarkNPMGroupValidated(b *testing.B) {
	for _, n := range []int{100, 2000} {
		f := newFixture(b, "npm", member("hosted", "npm", "hosted"), member("mirror", "npm", "proxy"))
		f.h.SetBaseURL(npmBase)

		v := newValidatedMembers()
		v.set("hosted", benchPackument("hosted", n))
		v.set("mirror", benchPackument("mirror", n))

		be := v.backend()
		warm := httptest.NewRequestWithContext(b.Context(), http.MethodGet, "/", http.NoBody)
		rec := httptest.NewRecorder()
		f.h.Serve(rec, warm, f.grp, "lib", be)

		etag := rec.Header().Get("ETag")
		if etag == "" {
			b.Fatal("the merge has no validator")
		}

		for _, c := range []struct {
			name   string
			header map[string]string
			want   int
		}{
			{"304", map[string]string{"If-None-Match": etag}, http.StatusNotModified},
			{"200gzip", map[string]string{"Accept-Encoding": "gzip"}, http.StatusOK},
		} {
			b.Run(fmt.Sprintf("versions=%d/%s", n, c.name), func(b *testing.B) {
				b.ReportAllocs()

				req := httptest.NewRequestWithContext(b.Context(), http.MethodGet, "/", http.NoBody)
				for k, val := range c.header {
					req.Header.Set(k, val)
				}

				for b.Loop() {
					w := &discardWriter{header: http.Header{}}
					f.h.Serve(w, req, f.grp, "lib", be)

					if w.code != c.want {
						b.Fatalf("status %d, want %d", w.code, c.want)
					}
				}
			})
		}
	}
}
