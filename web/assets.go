// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/klauspost/compress/gzip"

	"github.com/openartifact/openartifact/internal/httpcache"
)

const (
	// cacheImmutable is sent for content-hashed build output: the URL changes
	// whenever the bytes do.
	cacheImmutable = "public, max-age=31536000, immutable"
	// cacheRevalidate is sent for everything else (index.html and the SPA
	// fallback, public/ files): stored, but revalidated through the ETag.
	cacheRevalidate = "no-cache"
)

// contentTypes pins the media type of files the platform mime tables get wrong
// or lack (Go has no built-in entry for .webmanifest and a shell-less image has
// no /etc/mime.types), so the install manifest is never sniffed as text/plain.
//
//nolint:gochecknoglobals // read-only table
var contentTypes = map[string]string{
	".webmanifest": "application/manifest+json",
}

// hashedName matches the file names Vite emits under assets/
// (`[name]-[hash][extname]`, see web/vite.config.ts).
var hashedName = regexp.MustCompile(`-[A-Za-z0-9_-]{8,}\.[A-Za-z0-9.]+$`)

// variant is one encoding of an asset with its own validator: RFC 9110 wants
// different representations to carry different ETags. The bytes are not held
// here: open streams them (from the embedded FS, or from memory for the small
// rendered shell), so a built UI is resident in the binary only.
type variant struct {
	etag string
	size int64
	open func() (io.ReadCloser, error)
}

type asset struct {
	ctyp      string
	immutable bool
	// sum is the hex digest the validators of every variant derive from.
	sum   string
	plain variant
	// gzip is the compressed representation; nil for content that is already
	// compressed, binary, or did not shrink. A build that ran
	// scripts/precompress.js embeds it; otherwise it is compressed by the first
	// request that looks (compressOnce), never at startup.
	gzip         *variant
	compressible bool
	compressOnce sync.Once
}

// compressible reports whether a media type is text-like and so worth
// compressing. Images (except SVG), woff/woff2 fonts, archives and other
// binary blobs are already compressed or incompressible.
func compressible(ctyp string) bool {
	mt, _, err := mime.ParseMediaType(ctyp)
	if err != nil {
		return false
	}

	switch {
	case strings.HasPrefix(mt, "text/"):
		return true
	case strings.HasSuffix(mt, "+json"), strings.HasSuffix(mt, "+xml"):
		return true
	}

	switch mt {
	case "application/javascript", "application/json", "application/xml",
		"application/wasm", "image/svg+xml", "image/x-icon", "image/vnd.microsoft.icon",
		"font/ttf", "font/otf", "application/vnd.ms-fontobject", "application/x-font-ttf":
		return true
	}

	return false
}

func etagOf(sum, suffix string) string { return `"` + sum + suffix + `"` }

// gzipWriters holds the compressors: one Writer allocates over a megabyte of
// match tables at BestCompression, so each compression reuses one.
//
//nolint:gochecknoglobals // pool of reusable compressors
var gzipWriters = sync.Pool{New: func() any {
	zw, _ := gzip.NewWriterLevel(io.Discard, gzip.BestCompression) // the level is valid

	return zw
}}

// precompressedExt marks a file whose content is the gzip of the asset named
// without it (scripts/precompress.js). The server embeds only that gzip, so a
// built UI is stored once and streamed: the plain bytes are expanded per
// request for the clients that do not accept gzip.
const precompressedExt = ".gz"

// copyBufSize is the size of the read buffer shared by the startup digests.
const copyBufSize = 32 << 10

// sniffLen is how many leading bytes http.DetectContentType looks at.
const sniffLen = 512

// digest streams r once through buf and returns the validator (the first 16
// bytes of its SHA-256, hex), its length and its first sniffLen bytes.
func digest(r io.Reader, buf []byte) (fileDigest, error) {
	h := sha256.New()
	d := fileDigest{head: make([]byte, 0, sniffLen)}

	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
			d.size += int64(n)

			if room := sniffLen - len(d.head); room > 0 {
				d.head = append(d.head, buf[:min(n, room)]...)
			}
		}

		if rerr == io.EOF {
			break
		}

		if rerr != nil {
			return fileDigest{}, rerr //nolint:wrapcheck // the caller skips the file
		}
	}

	d.sum = hex.EncodeToString(h.Sum(nil)[:16])

	return d, nil
}

// fileDigest is what one pass over a file's plain bytes yields.
type fileDigest struct {
	sum  string // validator: the first 16 bytes of the SHA-256, hex
	size int64
	head []byte // the first sniffLen bytes
}

// readCloser pairs a decompressing reader with the file under it.
type readCloser struct {
	io.Reader

	closers []io.Closer
}

func (c readCloser) Close() error {
	var first error

	for _, cl := range c.closers {
		if err := cl.Close(); err != nil && first == nil {
			first = err
		}
	}

	return first
}

// gunzipFile opens the gzip file p of sub for reading its plain bytes.
func gunzipFile(sub fs.FS, p string) (io.ReadCloser, error) {
	f, err := sub.Open(p)
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller skips the file
	}

	zr, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()

		return nil, err //nolint:wrapcheck // the caller skips the file
	}

	return readCloser{Reader: zr, closers: []io.Closer{zr, f}}, nil
}

// readPlain loads the plain bytes of a small asset (index.html, which the
// branding patches); nil when they cannot be read.
func readPlain(a *asset) []byte {
	rc, err := a.plain.open()
	if err != nil {
		return nil
	}

	defer func() { _ = rc.Close() }()

	b, err := io.ReadAll(rc)
	if err != nil {
		return nil
	}

	return b
}

func bytesOpen(b []byte) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
}

// digestFile digests the plain bytes of file p of sub, expanding it first when
// it is a precompressed one; zr is the decompressor to reuse (created on first use).
func digestFile(sub fs.FS, p string, isGz bool, zr **gzip.Reader, buf []byte) (fileDigest, error) {
	f, err := sub.Open(p)
	if err != nil {
		return fileDigest{}, err //nolint:wrapcheck // the caller skips the file
	}

	defer func() { _ = f.Close() }()

	if !isGz {
		return digest(f, buf)
	}

	if *zr == nil {
		*zr, err = gzip.NewReader(f)
	} else {
		err = (*zr).Reset(f)
	}

	if err != nil {
		return fileDigest{}, err //nolint:wrapcheck // the caller skips the file
	}

	return digest(*zr, buf)
}

func loadAssets(sub fs.FS) map[string]*asset {
	assets := make(map[string]*asset)
	buf := make([]byte, copyBufSize)

	var zr *gzip.Reader // one decompressor, reset for every precompressed file

	_ = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // unreadable entries of the embedded FS are skipped; directories hold no assets
		}
		if d.Name() == ".gitkeep" {
			return nil // placeholder that lets //go:embed compile before the UI is built
		}

		name, isGz := strings.CutSuffix(p, precompressedExt)
		open := func() (io.ReadCloser, error) { return sub.Open(p) }

		if isGz {
			open = func() (io.ReadCloser, error) { return gunzipFile(sub, p) }
		}

		// One pass over the plain bytes gives the validator, the size and the
		// head to sniff; nothing is kept.
		dg, digestErr := digestFile(sub, p, isGz, &zr, buf)
		if digestErr != nil {
			return nil //nolint:nilerr // a corrupt or unreadable embedded file is simply not served
		}

		ctyp := contentTypes[path.Ext(name)]
		if ctyp == "" {
			ctyp = mime.TypeByExtension(path.Ext(name))
		}

		if ctyp == "" {
			ctyp = http.DetectContentType(dg.head)
		}

		a := &asset{
			ctyp:         ctyp,
			immutable:    strings.HasPrefix(name, "assets/") && hashedName.MatchString(name),
			sum:          dg.sum,
			plain:        variant{etag: etagOf(dg.sum, ""), size: dg.size, open: open},
			compressible: compressible(ctyp),
		}

		if isGz {
			a.compressOnce.Do(func() {}) // the build already compressed it

			if info, infoErr := d.Info(); infoErr == nil && info.Size() < dg.size {
				a.gzip = &variant{
					etag: etagOf(dg.sum, "-gzip"), size: info.Size(),
					open: func() (io.ReadCloser, error) { return sub.Open(p) },
				}
			}
		}

		assets[name] = a

		return nil
	})

	return assets
}

// newAsset hashes an in-memory body (the rendered shell); its gzip is made on
// first use like any other.
func newAsset(ctyp string, body []byte, immutable bool) *asset {
	dg, _ := digest(bytes.NewReader(body), make([]byte, copyBufSize)) // a bytes.Reader never fails

	return &asset{
		ctyp: ctyp, immutable: immutable, sum: dg.sum, compressible: compressible(ctyp),
		plain: variant{etag: etagOf(dg.sum, ""), size: dg.size, open: bytesOpen(body)},
	}
}

// gzipVariant returns the compressed representation, or nil when the asset has
// none. A compressible asset the build did not compress is compressed here, once,
// with a pooled writer, and its compressed bytes are what stays resident.
func (a *asset) gzipVariant() *variant {
	a.compressOnce.Do(func() {
		if !a.compressible {
			return
		}

		rc, err := a.plain.open()
		if err != nil {
			return
		}

		defer func() { _ = rc.Close() }()

		var buf bytes.Buffer

		zw, _ := gzipWriters.Get().(*gzip.Writer)
		defer gzipWriters.Put(zw)

		zw.Reset(&buf)

		if _, err := io.Copy(zw, rc); err != nil || zw.Close() != nil || int64(buf.Len()) >= a.plain.size {
			return
		}

		gz := bytes.Clone(buf.Bytes()) // drop the buffer's slack

		a.gzip = &variant{etag: etagOf(a.sum, "-gzip"), size: int64(len(gz)), open: bytesOpen(gz)}
	})

	return a.gzip
}

// acceptsGzip reports whether an Accept-Encoding header allows gzip: an
// explicit gzip entry decides on its own quality; otherwise a "*" entry does.
func acceptsGzip(header string) bool {
	star := false

	for part := range strings.SplitSeq(header, ",") {
		coding, params, _ := strings.Cut(part, ";")
		coding = strings.ToLower(strings.TrimSpace(coding))

		if coding != "gzip" && coding != "x-gzip" && coding != "*" {
			continue
		}

		allowed := qualityOf(params) > 0

		if coding == "*" {
			star = allowed
			continue
		}

		return allowed
	}

	return star
}

// qualityOf extracts the q value of an Accept-Encoding entry's parameters;
// absent means 1 and a malformed value means 0 (not acceptable).
func qualityOf(params string) float64 {
	_, qv, ok := strings.Cut(strings.ReplaceAll(params, " ", ""), "q=")
	if !ok {
		return 1
	}

	q, err := strconv.ParseFloat(qv, 64)
	if err != nil {
		return 0
	}

	return q
}

func serveAsset(w http.ResponseWriter, r *http.Request, a *asset) {
	h := w.Header()
	setSecurityHeaders(h)
	h.Set("Content-Type", a.ctyp)

	if a.immutable {
		h.Set("Cache-Control", cacheImmutable)
	} else {
		h.Set("Cache-Control", cacheRevalidate)
	}

	v, gz := &a.plain, a.gzipVariant()

	if gz != nil {
		// Caches must key compressible assets on the request's encoding even
		// when this particular response is the uncompressed one.
		h.Add("Vary", "Accept-Encoding")

		if acceptsGzip(r.Header.Get("Accept-Encoding")) {
			v = gz
			h.Set("Content-Encoding", "gzip")
		}
	}

	h.Set("ETag", v.etag)

	if httpcache.ETagMatches(r.Header.Get("If-None-Match"), v.etag) {
		h.Del("Content-Encoding")
		w.WriteHeader(http.StatusNotModified)

		return
	}

	h.Set("Content-Length", strconv.FormatInt(v.size, 10))

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)

		return
	}

	body, err := v.open()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	defer func() { _ = body.Close() }()

	w.WriteHeader(http.StatusOK)

	_, _ = io.Copy(w, body)
}
