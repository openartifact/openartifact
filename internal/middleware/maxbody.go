// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import "net/http"

// MaxBodyMiddleware caps every request body at limit bytes. Reads past the cap
// fail with *http.MaxBytesError (handlers answer 413) and the connection is
// closed. A non-positive limit disables the cap. It is the backstop behind the
// per-handler limits, so no endpoint can be made to buffer or spool an
// unbounded body.
func MaxBodyMiddleware(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if limit <= 0 {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}

			if r.Body != nil && r.Body != http.NoBody {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}

			next.ServeHTTP(w, r)
		})
	}
}
