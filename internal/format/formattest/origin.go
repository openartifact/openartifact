// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formattest

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/importmeta"
)

// AtOrigin wraps a route registration so every request it serves carries the
// origin creation time an X-OA-Import-Created-At header from a `manage`
// principal would have resolved to (see importmeta). It lets format tests
// exercise the import path without rebuilding the auth chain. A zero origin
// returns register unchanged (an ordinary upload).
func AtOrigin(register func(chi.Router), origin time.Time) func(chi.Router) {
	if origin.IsZero() {
		return register
	}

	return func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(importmeta.WithCreatedAt(req.Context(), origin)))
			})
		})
		register(r)
	}
}

// Origin returns the fixed historical timestamp the import tests use.
func Origin() time.Time { return time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC) }
