// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/httpcompress"
)

// apiPrefix is the control-plane API: JSON documents, 84-90% smaller encoded.
const apiPrefix = "/api/v1/"

// CompressAPI zstd- or gzip-encodes the JSON documents of the control-plane API
// (/api/v1/**) for clients that accept it; see httpcompress.Middleware for the
// rules. Everything else passes through untouched. Repository routes (/repos,
// /v2) serve artifacts and are never compressed here: the formats' metadata
// handlers opt in one by one (base.Compress). Downloads under /api/v1 (a
// Content-Disposition response: artifact and export downloads) are left alone
// by the middleware itself.
//
// Mount it innermost on the authenticated router, so the status-recording
// writer of the outer layers stays the one the handlers see.
func CompressAPI(next http.Handler) http.Handler {
	compressed := httpcompress.Middleware(next)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, apiPrefix) {
			compressed.ServeHTTP(w, r)

			return
		}

		next.ServeHTTP(w, r)
	})
}
