// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/format/base"
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
// serves GET/HEAD requests for a shadowed package name, and the version
// document of any package ("/pkg/1.0.0", "/@scope/pkg/1.0.0": the two- and
// three-segment shapes that other formats' routes take over, conda's
// /{platform}/{filename} and terraform's /{mod}/{system}/{version}), from the
// npm handlers when {name} is an npm repository, and passes everything else on
// untouched. The router must run it before the per-format routes.
func (h *HTTPHandler) ShadowGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}

		_, tail := middleware.RepoAndPath(r.URL.Path)
		pkg, version, ok := shadowedPackagePath(tail)
		if !ok {
			pkg, version, ok = versionManifestPath(tail)
		}

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
		if err != nil || repo == nil {
			next.ServeHTTP(w, r)
			return
		}

		rctx.URLParams.Add("pkg", pkg)

		if version == "" {
			h.handlePackument(w, r)
			return
		}

		rctx.URLParams.Add("version", version)
		base.Compress(h.handleVersionManifest)(w, r)
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

// versionManifestPath reports whether tail is the version-document path of a
// package: "pkg/1.0.0", or "@scope/pkg/1.0.0" (the scope slash arrives
// unescaped when the client sent "%2f" and the path was decoded). The tarball
// paths ("pkg/-/file.tgz") and the "-" API namespace are not version documents.
func versionManifestPath(tail string) (string, string, bool) {
	segs := strings.Split(strings.Trim(tail, "/"), "/")

	var pkg, version string

	switch {
	case len(segs) == 2 && !strings.HasPrefix(segs[0], "@"):
		pkg, version = segs[0], segs[1]
	case len(segs) == 3 && strings.HasPrefix(segs[0], "@"):
		pkg, version = segs[0]+"/"+segs[1], segs[2]
	default:
		return "", "", false
	}

	if pkg == "" || version == "" || strings.HasPrefix(pkg, "-") || strings.Contains(pkg, "/-") ||
		version == "-" {
		return "", "", false
	}

	return pkg, version, true
}
