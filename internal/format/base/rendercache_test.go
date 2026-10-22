// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func doc(n int) *Rendered {
	return &Rendered{Body: bytes.Repeat([]byte("a"), n), ContentType: "application/json"}
}

func renderOf(r *Rendered) func(context.Context) (*Rendered, error) {
	return func(context.Context) (*Rendered, error) { return r, nil }
}

func TestRenderCacheRendersOncePerKey(t *testing.T) {
	c := NewRenderCache(1 << 20)

	var renders atomic.Int32

	render := func(context.Context) (*Rendered, error) {
		renders.Add(1)
		return doc(10), nil
	}

	for range 3 {
		if _, err := c.Do(t.Context(), "k", render); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := c.Do(t.Context(), "k2", render); err != nil {
		t.Fatal(err)
	}

	if renders.Load() != 2 {
		t.Errorf("renders = %d, want 2 (one per key)", renders.Load())
	}
}

func TestRenderCacheSharesConcurrentMisses(t *testing.T) {
	c := NewRenderCache(1 << 20)

	var renders atomic.Int32

	release := make(chan struct{})
	started := make(chan struct{})

	render := func(context.Context) (*Rendered, error) {
		if renders.Add(1) == 1 {
			close(started)
		}

		<-release

		return doc(10), nil
	}

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() { _, _ = c.Do(t.Context(), "k", render) })
	}

	<-started
	close(release)
	wg.Wait()

	if renders.Load() != 1 {
		t.Errorf("concurrent misses rendered %d times, want 1", renders.Load())
	}
}

func TestRenderCacheDoesNotCacheErrors(t *testing.T) {
	c := NewRenderCache(1 << 20)
	boom := errors.New("boom")

	if _, err := c.Do(t.Context(), "k", func(context.Context) (*Rendered, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}

	if got, err := c.Do(t.Context(), "k", renderOf(doc(5))); err != nil || len(got.Body) != 5 {
		t.Fatalf("retry after error: %v, %v", got, err)
	}
}

func TestRenderCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := NewRenderCache(400)

	for _, k := range []string{"a", "b", "c"} {
		if _, err := c.Do(t.Context(), k, renderOf(doc(100))); err != nil {
			t.Fatal(err)
		}
	}

	c.Get("a") // a is now newer than b

	if _, err := c.Do(t.Context(), "d", renderOf(doc(100))); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Do(t.Context(), "e", renderOf(doc(100))); err != nil {
		t.Fatal(err)
	}

	if _, ok := c.Get("b"); ok {
		t.Error("least recently used entry b survived")
	}

	if _, ok := c.Get("a"); !ok {
		t.Error("recently used entry a was evicted")
	}
}

func TestRenderCacheSkipsOversizedDocuments(t *testing.T) {
	c := NewRenderCache(1000)

	if _, err := c.Do(t.Context(), "big", renderOf(doc(600))); err != nil {
		t.Fatal(err)
	}

	if c.Len() != 0 {
		t.Error("a document above a quarter of the budget was stored")
	}
}

func TestRenderedServe(t *testing.T) {
	body := strings.Repeat(`{"k":"value"}`, 200)
	r := &Rendered{Body: []byte(body), ContentType: "application/json"}

	serve := func(method, acceptEncoding string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, "/", http.NoBody)
		if acceptEncoding != "" {
			req.Header.Set("Accept-Encoding", acceptEncoding)
		}

		w := httptest.NewRecorder()
		w.Header().Set("ETag", `"v1"`)
		r.Serve(w, req)

		return w
	}

	plain := serve(http.MethodGet, "")
	if plain.Body.String() != body || plain.Header().Get("Content-Encoding") != "" || plain.Header().Get("ETag") != `"v1"` {
		t.Errorf("identity response wrong: encoding %q etag %q", plain.Header().Get("Content-Encoding"), plain.Header().Get("ETag"))
	}

	refused := serve(http.MethodGet, "gzip;q=0, identity")
	if refused.Header().Get("Content-Encoding") != "" {
		t.Error("gzip;q=0 was compressed")
	}

	zipped := serve(http.MethodGet, "br, GZIP;q=0.5")
	if zipped.Header().Get("Content-Encoding") != "gzip" || zipped.Header().Get("ETag") != `W/"v1"` ||
		!strings.Contains(zipped.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("gzip response headers: %v", zipped.Header())
	}

	if zipped.Header().Get("Content-Length") != strconv.Itoa(zipped.Body.Len()) {
		t.Error("Content-Length does not match the compressed body")
	}

	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}

	if got, _ := io.ReadAll(zr); string(got) != body {
		t.Error("gzip body does not decode to the document")
	}

	head := serve(http.MethodHead, "gzip")
	if head.Body.Len() != 0 || head.Header().Get("Content-Length") != zipped.Header().Get("Content-Length") {
		t.Errorf("HEAD: %d body bytes, Content-Length %q", head.Body.Len(), head.Header().Get("Content-Length"))
	}
}

func TestRenderedServeNeverRecompressesOpaqueOrTinyBodies(t *testing.T) {
	for name, r := range map[string]*Rendered{
		"opaque": {Body: bytes.Repeat([]byte{1}, 4096), ContentType: "application/octet-stream", Opaque: true},
		"tiny":   {Body: []byte(`{}`), ContentType: "application/json"},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
			req.Header.Set("Accept-Encoding", "gzip")

			w := httptest.NewRecorder()
			r.Serve(w, req)

			if w.Header().Get("Content-Encoding") != "" || !bytes.Equal(w.Body.Bytes(), r.Body) {
				t.Errorf("served %d bytes with Content-Encoding %q", w.Body.Len(), w.Header().Get("Content-Encoding"))
			}
		})
	}
}

// A degraded rendering answers its request only: the cache does not keep it
// and the response carries no validator.
func TestRenderCacheDoesNotKeepTransientRenderings(t *testing.T) {
	c := NewRenderCache(1 << 20)

	var renders atomic.Int32

	render := func(context.Context) (*Rendered, error) {
		renders.Add(1)

		d := doc(10)
		d.Transient = true

		return d, nil
	}

	var got *Rendered

	for range 2 {
		var err error
		if got, err = c.Do(t.Context(), "k", render); err != nil {
			t.Fatal(err)
		}
	}

	if renders.Load() != 2 || c.Len() != 0 {
		t.Fatalf("renders = %d, cached = %d; want 2 and 0", renders.Load(), c.Len())
	}

	rec := httptest.NewRecorder()
	rec.Header().Set("ETag", `"v1"`)
	got.Serve(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if rec.Header().Get("ETag") != "" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v; want no ETag and no-store", rec.Header())
	}
}
