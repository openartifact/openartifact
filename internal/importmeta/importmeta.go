// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package importmeta lets administrators preserve origin metadata when
// migrating artifacts from another repository manager.
//
// An upload may carry the header X-OA-Import-Created-At (RFC 3339) holding the
// time the artifact was originally created. The header is honored only for
// principals holding the global "manage" permission; for everyone else it is
// silently ignored, so ordinary clients can never back-date or forward-date
// their uploads. An authorized request with a malformed or future timestamp is
// rejected with 400.
//
// Middleware resolves the header once per request and stores the verdict in the
// request context; upload handlers read it with CreatedAt.
package importmeta

import (
	"context"
	"net/http"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/httpx"
	"github.com/openartifact/openartifact/internal/middleware"
)

// HeaderCreatedAt carries the origin creation time of an uploaded artifact.
const HeaderCreatedAt = "X-Oa-Import-Created-At"

// maxClockSkew is how far into the future a timestamp may lie before it is
// rejected; it absorbs clock drift between the source and this server.
const maxClockSkew = time.Minute

type ctxKey struct{}

// Authorizer is the part of auth.Evaluator the middleware needs.
type Authorizer interface {
	Evaluate(ctx context.Context, principal auth.Principal, repoName, artifactPath, action string) (bool, error)
}

// WithCreatedAt returns a context carrying an origin creation time. It is for
// the middleware and for tests; request handlers only read it.
func WithCreatedAt(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, ctxKey{}, t.UTC())
}

// CreatedAt returns the origin creation time authorized for this request, or
// the zero time when there is none and the upload should be stamped "now".
func CreatedAt(ctx context.Context) time.Time {
	t, _ := ctx.Value(ctxKey{}).(time.Time)
	return t
}

// parse validates an origin timestamp against now.
func parse(value string, now time.Time) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil || t.Unix() <= 0 || t.After(now.Add(maxClockSkew)) {
		return time.Time{}, false
	}

	return t.UTC(), true
}

// Middleware resolves X-OA-Import-Created-At for every request that carries it.
// It must run after authentication so the principal is in the context.
func Middleware(authz Authorizer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			value := r.Header.Get(HeaderCreatedAt)
			if value == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Never let the raw header reach a handler.
			r.Header.Del(HeaderCreatedAt)

			p := middleware.PrincipalFromContext(r.Context())
			if p == nil || p.IsAnonymous {
				next.ServeHTTP(w, r)
				return
			}

			manage, err := authz.Evaluate(r.Context(), *p, "", "", "manage")
			if err != nil {
				httpx.WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "auth evaluation failed")
				return
			}

			if !manage {
				next.ServeHTTP(w, r)
				return
			}

			t, ok := parse(value, time.Now())
			if !ok {
				httpx.WriteError(w, http.StatusBadRequest, "INVALID_INPUT", HeaderCreatedAt+" must be an RFC 3339 timestamp that is not in the future")
				return
			}

			next.ServeHTTP(w, r.WithContext(WithCreatedAt(r.Context(), t)))
		})
	}
}
