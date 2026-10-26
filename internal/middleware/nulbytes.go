// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/httpx"
)

// RejectNULBytes answers 400 to a request whose path or query string carries a
// NUL byte. NUL is never meaningful in a URL, and Postgres text cannot store
// it: reaching a query it surfaces as a database error (500) from whichever
// endpoint happens to bind the value.
func RejectNULBytes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.IndexByte(r.URL.Path, 0) >= 0 || strings.IndexByte(r.URL.RawQuery, 0) >= 0 ||
			strings.Contains(r.URL.RawQuery, "%00") {
			httpx.WriteJSON(w, http.StatusBadRequest, map[string]string{
				"error": "request URL must not contain NUL bytes", "code": "bad_request",
			})

			return
		}

		next.ServeHTTP(w, r)
	})
}
