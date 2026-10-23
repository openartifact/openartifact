// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist holds the Vite build output (untracked). The tracked dist/.gitkeep keeps
// the pattern valid before the UI is built; until then the SPA answers 404.
//
//go:embed all:dist
var distFS embed.FS

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

// Handler serves the embedded SPA. In binaries built with the "dev" tag,
// setting OPENARTIFACT_DEV proxies to the Vite dev server instead (see
// devHandler); release binaries never honour it.
func Handler() http.Handler {
	if h := devHandler(); h != nil {
		return h
	}

	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("web: failed to create sub FS: " + err.Error())
	}
	return newSPAHandler(sub)
}

// newSPAHandler builds the handler for the SPA files in sub. The files are
// hashed and precompressed once, here, never per request.
func newSPAHandler(sub fs.FS) http.Handler {
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
			serveAsset(w, r, a)
			return
		}

		// Missing static files (anything under assets/ or with a file
		// extension) must 404 rather than return HTML with a 200. Nothing
		// under /repos/ is a static file: the RBAC middleware only hands this
		// handler real page navigations there, and the last segment is a
		// package name, which routinely carries a dot (lodash.debounce, a raw
		// docs/hello.txt, com.example.app), so the extension says nothing.
		if strings.HasPrefix(clean, "assets/") || (!strings.HasPrefix(clean, "repos/") && path.Ext(clean) != "") {
			notFound(w, false)
			return
		}

		index, ok := assets["index.html"]
		if !ok {
			notFound(w, false)
			return
		}
		serveAsset(w, r, index)
	})
}
