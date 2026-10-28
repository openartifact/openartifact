// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Guard returns chi middleware for the shared /repos/{name} subrouter, for a
// format whose paths clash with the wildcard routes the other formats register
// at the same depth. A request is claimed (answered by serve) when classify
// recognises the path below the repository and {name} is a repository of the
// format, and passed on untouched otherwise. Mount it before the per-format
// routes.
func Guard[R any](
	h *Handler,
	format string,
	classify func(path string) (R, bool),
	serve func(w http.ResponseWriter, r *http.Request, repoName string, route R),
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rctx := chi.RouteContext(r.Context())
			if rctx == nil {
				next.ServeHTTP(w, r)
				return
			}

			rt, ok := classify(rctx.RoutePath)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			name := rctx.URLParam("name")

			repo, err := h.ResolveRepo(r.Context(), name)
			if err != nil || repo == nil || repo.Format != format {
				next.ServeHTTP(w, r)
				return
			}

			serve(w, r, name, rt)
		})
	}
}
