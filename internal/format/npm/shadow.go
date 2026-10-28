// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/middleware"
)

// isShadowedPackageName reports names that are valid npm package names that another format
// registers as a static route on the shared /repos/{name} subrouter (cargo
// /config.json, helm /charts and /index.yaml, deb/rpm /public.key). chi
// prefers the static segment, so without the guard `npm view charts` against
// an npm repository would be answered by helm's handler.
func isShadowedPackageName(name string) bool {
	switch name {
	case "charts", "index.yaml", "config.json", "public.key":
		return true
	}

	return false
}

// ShadowGuard is chi middleware for the shared /repos/{name} subrouter. It
// serves GET/HEAD requests for a shadowed package name from the npm handlers
// when {name} is an npm repository, and passes everything else on untouched.
// The router must run it before the per-format routes.
func (h *HTTPHandler) ShadowGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}

		_, tail := middleware.RepoAndPath(r.URL.Path)
		pkg, version, ok := shadowedPackagePath(tail)

		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		rctx := chi.RouteContext(r.Context())
		if rctx == nil {
			next.ServeHTTP(w, r)
			return
		}

		repo, err := h.ResolveRepo(r.Context(), rctx.URLParam("name"))
		if err != nil || repo == nil || repo.Format != "npm" {
			next.ServeHTTP(w, r)
			return
		}

		rctx.URLParams.Add("pkg", pkg)

		if version == "" {
			h.handlePackument(w, r)
			return
		}

		rctx.URLParams.Add("version", version)
		h.handleVersionManifest(w, r)
	})
}

// shadowedPackagePath reports whether tail ("charts", "charts/1.0.0") is the
// packument or version-manifest path of a shadowed package name.
func shadowedPackagePath(tail string) (string, string, bool) {
	tail = strings.Trim(tail, "/")
	pkg, version, _ := strings.Cut(tail, "/")

	if !isShadowedPackageName(pkg) || strings.Contains(version, "/") {
		return "", "", false
	}

	return pkg, version, true
}
