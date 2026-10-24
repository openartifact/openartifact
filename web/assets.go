// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

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
// different representations to carry different ETags.
type variant struct {
	body []byte
	etag string
}

type asset struct {
	ctyp      string
	immutable bool
	plain     variant
	// gzip is the precompressed representation; nil for content that is
	// already compressed, binary, or did not shrink.
	gzip *variant
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

func etagOf(body []byte, suffix string) string {
	sum := sha256.Sum256(body)

	return `"` + hex.EncodeToString(sum[:16]) + suffix + `"`
}

func gzipBytes(body []byte) []byte {
	var buf bytes.Buffer

	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil
	}

	if _, err := zw.Write(body); err != nil {
		return nil
	}

	if err := zw.Close(); err != nil {
		return nil
	}

	return buf.Bytes()
}

func loadAssets(sub fs.FS) map[string]*asset {
	assets := make(map[string]*asset)

	_ = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // unreadable entries of the embedded FS are skipped; directories hold no assets
		}
		if d.Name() == ".gitkeep" {
			return nil // placeholder that lets //go:embed compile before the UI is built
		}

		body, readErr := fs.ReadFile(sub, p)
		if readErr != nil {
			return nil //nolint:nilerr // an unreadable embedded file is simply not served
		}

		ctyp := contentTypes[path.Ext(p)]
		if ctyp == "" {
			ctyp = mime.TypeByExtension(path.Ext(p))
		}

		if ctyp == "" {
			ctyp = http.DetectContentType(body)
		}

		assets[p] = newAsset(ctyp, body, strings.HasPrefix(p, "assets/") && hashedName.MatchString(p))

		return nil
	})

	return assets
}

// newAsset hashes body and precompresses it when worthwhile.
func newAsset(ctyp string, body []byte, immutable bool) *asset {
	a := &asset{
		ctyp:      ctyp,
		immutable: immutable,
		plain:     variant{body: body, etag: etagOf(body, "")},
	}

	if compressible(ctyp) {
		if gz := gzipBytes(body); gz != nil && len(gz) < len(body) {
			a.gzip = &variant{body: gz, etag: etagOf(body, "-gzip")}
		}
	}

	return a
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

	v := &a.plain

	if a.gzip != nil {
		// Caches must key compressible assets on the request's encoding even
		// when this particular response is the uncompressed one.
		h.Add("Vary", "Accept-Encoding")

		if acceptsGzip(r.Header.Get("Accept-Encoding")) {
			v = a.gzip
			h.Set("Content-Encoding", "gzip")
		}
	}

	h.Set("ETag", v.etag)

	if httpcache.ETagMatches(r.Header.Get("If-None-Match"), v.etag) {
		h.Del("Content-Encoding")
		w.WriteHeader(http.StatusNotModified)

		return
	}

	h.Set("Content-Length", strconv.Itoa(len(v.body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		//nolint:gosec // G705: embedded build output and the sanitised branded shell, not request data
		_, _ = w.Write(v.body)
	}
}
