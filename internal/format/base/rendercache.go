// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"bytes"
	"container/list"
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/klauspost/compress/gzip"
	"golang.org/x/sync/singleflight"

	"github.com/openartifact/openartifact/internal/iocopy"
)

const (
	// renderCacheBytes bounds the rendered metadata bodies (plain and gzipped)
	// kept in memory by the whole process.
	renderCacheBytes = 64 << 20

	// minCompressBytes is the smallest body worth compressing.
	minCompressBytes = 1024

	// compactBytes is the size from which a Compact document is kept only in
	// its gzip form (see Rendered.Compact).
	compactBytes = 1 << 20
)

// Rendered is one immutable rendered metadata document. It is shared between
// requests and must not be modified after it is handed to the cache.
type Rendered struct {
	Body        []byte
	ContentType string

	// Opaque marks a body that is already a compressed file format (such as
	// RubyGems' specs.4.8.gz): it is served byte for byte, never re-encoded
	// and without Content-Encoding.
	Opaque bool

	// Transient marks a degraded rendering (a part of the source could not be
	// read): it answers the requests sharing this render only. It is never
	// cached and goes out as no-store without a validator, so neither this
	// cache nor a client can keep serving it once the source recovers.
	Transient bool

	// Compact lets the cache keep a large (from compactBytes) document only
	// in its gzip form, 5-10x smaller for JSON metadata, so a multi-megabyte
	// document costs a fraction of its size in the byte budget and fits at all.
	// A client that does not accept gzip is served by decompressing it on the
	// fly, which is far cheaper than rendering again. Set it only when the
	// producer never reads Body from the document Do returns: Body is nil there.
	Compact bool

	cache *RenderCache
	key   string

	gzOnce sync.Once
	gz     []byte

	// bodyLen is the length of the identity document of a compacted Rendered,
	// whose Body is gone.
	bodyLen int
}

// compacted reports that the identity form was dropped.
func (r *Rendered) compacted() bool { return r.bodyLen > 0 }

// gzipBytes compresses b into a new buffer.
func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer

	zw, _ := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
	_, _ = zw.Write(b)
	_ = zw.Close()

	return buf.Bytes()
}

// compact returns the form of r the cache keeps: r itself, or for a large
// Compact document a copy that holds the gzip form only.
func (r *Rendered) compact() *Rendered {
	if !r.Compact || r.Opaque || r.Transient || len(r.Body) < compactBytes {
		return r
	}

	c := &Rendered{ContentType: r.ContentType, bodyLen: len(r.Body), gz: gzipBytes(r.Body)}
	c.gzOnce.Do(func() {}) // the gzip form exists already

	return c
}

// gzipped returns the gzip form of the body, compressed once and then reused
// by every request that accepts gzip. The compressed copy is charged to the
// cache budget.
func (r *Rendered) gzipped() []byte {
	r.gzOnce.Do(func() {
		r.gz = gzipBytes(r.Body)

		if r.cache != nil {
			r.cache.grow(r.key, r, int64(len(r.gz)))
		}
	})

	return r.gz
}

// Serve writes the document: gzip-encoded when the client accepts it, plain
// otherwise. Any ETag already set on w (see Revalidate) is kept, downgraded to
// a weak validator on the compressed representation, because the two
// representations are not byte-identical.
func (r *Rendered) Serve(w http.ResponseWriter, req *http.Request) {
	h := w.Header()
	h.Set("Content-Type", r.ContentType)

	if r.Transient {
		h.Del("ETag")
		h.Set("Cache-Control", "no-store")
	}

	if !r.Opaque {
		h.Add("Vary", "Accept-Encoding")
	}

	var (
		body    = r.Body
		length  = len(body)
		inflate bool // body is the compacted gzip form of an identity response
	)

	switch {
	case r.compacted() && acceptsGzip(req):
		body = r.gz
		length = len(body)

		h.Set("Content-Encoding", "gzip")
		weakenETag(h)
	case r.compacted():
		inflate = true
		length = r.bodyLen
	case !r.Opaque && len(body) >= minCompressBytes && acceptsGzip(req):
		body = r.gzipped()
		length = len(body)

		h.Set("Content-Encoding", "gzip")
		weakenETag(h)
	}

	h.Set("Content-Length", strconv.Itoa(length))
	w.WriteHeader(http.StatusOK)

	if req.Method == http.MethodHead {
		return
	}

	if inflate {
		if zr, err := gzip.NewReader(bytes.NewReader(r.gz)); err == nil {
			_, _ = iocopy.Copy(w, zr)
		}

		return
	}

	_, _ = w.Write(body)
}

type renderEntry struct {
	key  string
	val  *Rendered
	size int64
}

// RenderCache is a byte-bounded LRU of rendered metadata documents with
// per-key single-flight rendering. Keys must embed the validator (ETag) of the
// data the document was rendered from, so a changed source can never be served
// from an old entry: an outdated key simply ages out.
type RenderCache struct {
	mu       sync.Mutex
	maxBytes int64
	used     int64
	order    *list.List // front = most recently used
	items    map[string]*list.Element
	flight   singleflight.Group
}

// NewRenderCache returns a cache holding at most maxBytes of documents.
func NewRenderCache(maxBytes int64) *RenderCache {
	return &RenderCache{maxBytes: maxBytes, order: list.New(), items: make(map[string]*list.Element)}
}

// sharedRender is the process-wide cache behind Handler.Rendered and
// MergedRendered.
var sharedRender = NewRenderCache(renderCacheBytes) //nolint:gochecknoglobals // process-wide cache, bounded by renderCacheBytes

// mergedNS separates group merge results from the per-handler namespaces.
const mergedNS = "merged\x00"

// MergedRendered is Handler.Rendered for documents that are not rendered by a
// format handler, such as a group's merge of its members' documents. key must
// identify every input of the rendering (hash the inputs, not their ages).
func MergedRendered(ctx context.Context, key string, render func(ctx context.Context) (*Rendered, error)) (*Rendered, error) {
	return sharedRender.Do(ctx, mergedNS+key, render)
}

// CachedMerged returns the document MergedRendered holds under key, if any,
// without rendering it: a caller that can name a document by a cheap validator
// answers from the cache before collecting what the document is built from.
func CachedMerged(key string) (*Rendered, bool) {
	return sharedRender.Get(mergedNS + key)
}

// Get returns the cached document for key.
func (c *RenderCache) Get(key string) (*Rendered, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return nil, false
	}

	c.order.MoveToFront(el)

	entry, _ := el.Value.(*renderEntry)

	return entry.val, true
}

// Do returns the document for key, rendering it with render on a miss. Callers
// racing on the same missing key share a single render. render runs detached
// from the first caller's cancellation so one aborted request cannot fail the
// others.
func (c *RenderCache) Do(ctx context.Context, key string, render func(ctx context.Context) (*Rendered, error)) (*Rendered, error) {
	if r, ok := c.Get(key); ok {
		return r, nil
	}

	ch := c.flight.DoChan(key, func() (any, error) {
		if r, ok := c.Get(key); ok {
			return r, nil
		}

		r, err := render(context.WithoutCancel(ctx))
		if err != nil {
			return nil, err
		}

		r = r.compact()

		if !r.Transient {
			c.put(key, r)
		}

		return r, nil
	})

	// The render outlives a caller that gives up (others may wait on it), but
	// that caller does not wait for it.
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err //nolint:wrapcheck // the render error is returned unchanged
		}

		r, _ := res.Val.(*Rendered)

		return r, nil
	}
}

func (c *RenderCache) put(key string, r *Rendered) {
	size := int64(len(r.Body) + len(r.gz))

	// A document that would flush a large share of the cache is not worth
	// keeping; it is rendered per request instead.
	if size > c.maxBytes/4 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	r.cache, r.key = c, key

	if el, ok := c.items[key]; ok {
		c.removeLocked(el)
	}

	c.items[key] = c.order.PushFront(&renderEntry{key: key, val: r, size: size})
	c.used += size

	c.evictLocked()
}

// grow charges extra bytes (the lazily built gzip copy) to an entry, unless it
// was evicted or replaced meanwhile.
func (c *RenderCache) grow(key string, r *Rendered, extra int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return
	}

	entry, _ := el.Value.(*renderEntry)
	if entry.val != r {
		return
	}

	entry.size += extra
	c.used += extra

	c.evictLocked()
}

func (c *RenderCache) evictLocked() {
	for c.used > c.maxBytes {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}

		c.removeLocked(oldest)
	}
}

func (c *RenderCache) removeLocked(el *list.Element) {
	entry, _ := el.Value.(*renderEntry)
	c.order.Remove(el)
	delete(c.items, entry.key)
	c.used -= entry.size
}

// Len reports the number of cached documents (tests).
func (c *RenderCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.order.Len()
}

// acceptsGzip reports whether the client accepts a gzip-encoded response. A
// request carrying Range is never compressed: ranges address the identity
// representation.
func acceptsGzip(r *http.Request) bool {
	if r.Header.Get("Range") != "" {
		return false
	}

	for v := range strings.SplitSeq(r.Header.Get("Accept-Encoding"), ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(v), ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			continue
		}

		// gzip;q=0 explicitly refuses it.
		if q, ok := strings.CutPrefix(strings.ReplaceAll(strings.TrimSpace(params), " ", ""), "q="); ok {
			if f, err := strconv.ParseFloat(q, 64); err == nil && f <= 0 {
				return false
			}
		}

		return true
	}

	return false
}

func weakenETag(h http.Header) {
	if et := h.Get("ETag"); et != "" && !strings.HasPrefix(et, "W/") {
		h.Set("ETag", "W/"+et)
	}
}
