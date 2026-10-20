// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

type asset struct {
	body []byte
	etag string
	ctyp string
}

// setSecurityHeaders applies the headers every SPA response carries. The
// frame-ancestors / X-Frame-Options pair prevents clickjacking of the admin UI.
func setSecurityHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; "+
			"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
}

func loadAssets(sub fs.FS) map[string]*asset {
	assets := make(map[string]*asset)
	_ = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // unreadable entries of the embedded FS are skipped; directories hold no assets
		}
		body, readErr := fs.ReadFile(sub, p)
		if readErr != nil {
			return nil //nolint:nilerr // an unreadable embedded file is simply not served
		}
		sum := sha256.Sum256(body)
		ctyp := mime.TypeByExtension(path.Ext(p))
		if ctyp == "" {
			ctyp = http.DetectContentType(body)
		}
		assets[p] = &asset{body: body, etag: `"` + hex.EncodeToString(sum[:16]) + `"`, ctyp: ctyp}
		return nil
	})
	return assets
}

func serveAsset(w http.ResponseWriter, r *http.Request, name string, a *asset) {
	h := w.Header()
	setSecurityHeaders(h)
	h.Set("Content-Type", a.ctyp)
	h.Set("ETag", a.etag)
	if strings.HasPrefix(name, "assets/") {
		// Vite content-hashes everything under assets/.
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	if r.Header.Get("If-None-Match") == a.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(a.body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(a.body)
	}
}

func notFound(w http.ResponseWriter, apiPath bool) {
	setSecurityHeaders(w.Header())
	if apiPath {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found","code":"not_found"}`))
		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}

// Handler serves the embedded SPA. Set OPENARTIFACT_DEV to proxy to the Vite
// dev server instead (development only).
func Handler() http.Handler {
	if os.Getenv("OPENARTIFACT_DEV") != "" {
		log.Printf("web: OPENARTIFACT_DEV is set; proxying UI to http://localhost:5173 (development only)")
		target, _ := url.Parse("http://localhost:5173")
		return httputil.NewSingleHostReverseProxy(target)
	}

	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("web: failed to create sub FS: " + err.Error())
	}
	assets := loadAssets(sub)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			notFound(w, true)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		clean := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if clean == "" {
			clean = "index.html"
		}

		if a, ok := assets[clean]; ok {
			serveAsset(w, r, clean, a)
			return
		}

		// Missing static files (anything under assets/ or with a file
		// extension) must 404 rather than return HTML with a 200.
		if strings.HasPrefix(clean, "assets/") || path.Ext(clean) != "" {
			notFound(w, false)
			return
		}

		index, ok := assets["index.html"]
		if !ok {
			notFound(w, false)
			return
		}
		serveAsset(w, r, "index.html", index)
	})
}
