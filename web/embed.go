// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync/atomic"

	"github.com/openartifact/openartifact/internal/branding"
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
func Handler(src ShellSource) http.Handler {
	if h := devHandler(); h != nil {
		return h
	}

	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("web: failed to create sub FS: " + err.Error())
	}
	return newSPAHandler(sub, src)
}

// ShellSource supplies the effective branding the app shell is rendered with.
// With a nil source the shell is served exactly as built.
type ShellSource interface {
	Snapshot(ctx context.Context) *branding.Snapshot
}

// shell is the branded index.html and web manifest of one branding snapshot,
// hashed and precompressed once per change, never per request.
type shell struct {
	etag     string
	index    *asset
	manifest *asset
}

// shellFor returns the branded shell for the current snapshot, rebuilding it
// only when the branding changed.
func shellFor(ctx context.Context, src ShellSource, cur *atomic.Pointer[shell], rawIndex []byte) *shell {
	snap := src.Snapshot(ctx)

	if c := cur.Load(); c != nil && c.etag == snap.ETag {
		return c
	}

	c := &shell{
		etag:     snap.ETag,
		manifest: newAsset("application/manifest+json", branding.Manifest(snap), false),
	}

	if rawIndex != nil {
		c.index = newAsset("text/html; charset=utf-8", branding.PatchIndex(rawIndex, snap), false)
	}

	cur.Store(c)

	return c
}

// newSPAHandler builds the handler for the SPA files in sub. The files are
// hashed and precompressed once, here, never per request. With a ShellSource,
// index.html (and the SPA fallback) and /manifest.webmanifest are rendered
// with the effective branding.
func newSPAHandler(sub fs.FS, src ShellSource) http.Handler {
	assets := loadAssets(sub)

	var (
		rawIndex []byte
		current  atomic.Pointer[shell]
	)

	if a, ok := assets["index.html"]; ok {
		rawIndex = a.plain.body
	}

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

		if src != nil && clean == "manifest.webmanifest" {
			serveAsset(w, r, shellFor(r.Context(), src, &current, rawIndex).manifest)
			return
		}

		if src != nil && clean == "index.html" && rawIndex != nil {
			serveAsset(w, r, shellFor(r.Context(), src, &current, rawIndex).index)
			return
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
		// docs/hello.txt, com.example.app), so the extension says nothing. The
		// same goes for /bundles/NAME/VERSION, whose version is usually semver
		// (1.0.0).
		if strings.HasPrefix(clean, "assets/") || (!strings.HasPrefix(clean, "repos/") && !strings.HasPrefix(clean, "bundles/") && path.Ext(clean) != "") {
			notFound(w, false)
			return
		}

		index, ok := assets["index.html"]
		if !ok {
			notFound(w, false)
			return
		}

		if src != nil {
			index = shellFor(r.Context(), src, &current, rawIndex).index
		}

		serveAsset(w, r, index)
	})
}
